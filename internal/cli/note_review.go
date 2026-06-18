package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const (
	noteStaleAfterEnv     = "CC_NOTES_NOTE_STALE_AFTER"
	noteStaleAfterConfig  = "cc-notes.noteStaleAfter"
	defaultNoteStaleAfter = 90 * 24 * time.Hour
)

// Note review verdicts. A note carries at most one: precedence is
// EXPIRED > UNVERIFIED > DRIFTED > STALE (DANGLING reported separately for
// broken supersede edges).
const (
	verdictExpired    = "EXPIRED"
	verdictUnverified = "UNVERIFIED"
	verdictDrifted    = "DRIFTED"
	verdictStale      = "STALE"
	verdictDangling   = "DANGLING"
)

// reviewedNote pairs a note with its computed review verdict.
type reviewedNote struct {
	note    model.Note
	verdict string
}

// noteStaleAfter resolves the note staleness threshold with precedence
// env > git config > 90d: CC_NOTES_NOTE_STALE_AFTER overrides the last
// cc-notes.noteStaleAfter git config value (mirrors leaseTTL).
func noteStaleAfter(ctx context.Context, g gitcmd.Git) (time.Duration, error) {
	if value, ok := os.LookupEnv(noteStaleAfterEnv); ok {
		return parseDuration(value)
	}
	values, err := g.ConfigGetAll(ctx, noteStaleAfterConfig)
	if err != nil {
		return 0, err
	}
	if len(values) > 0 {
		return parseDuration(values[len(values)-1])
	}
	return defaultNoteStaleAfter, nil
}

// resolveNoteStaleAfter returns the flag value when set, otherwise the
// configured threshold.
func resolveNoteStaleAfter(ctx context.Context, g gitcmd.Git, flag string) (time.Duration, error) {
	if flag != "" {
		return parseDuration(flag)
	}
	return noteStaleAfter(ctx, g)
}

// resolveHead returns the commit HEAD points at, or "" when HEAD is unborn (a
// repository with no commits yet). An unborn HEAD means there is no live
// content to witness or drift-check against.
func resolveHead(ctx context.Context, s *store.Store) (model.SHA, error) {
	head, err := s.Repo.Tip(ctx, "HEAD")
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return head, nil
}

// buildWitness computes the per-anchor content witness for anchors against
// head: a path anchor's content oid and a directory anchor's tree oid (both
// skipped when HEAD is unborn or the path is absent), and a commit anchor's
// own oid. Branch anchors carry no witness. The result tracks anchor order, so
// the folded witness order is deterministic.
func buildWitness(ctx context.Context, s *store.Store, head model.SHA, anchors []model.Anchor) ([]model.AnchorWitness, error) {
	var witness []model.AnchorWitness
	for _, a := range anchors {
		switch a.Kind {
		case model.AnchorPath, model.AnchorDir:
			if head == "" {
				continue
			}
			oid, err := s.Git.PathOID(ctx, string(head), a.Value)
			if errors.Is(err, gitcmd.ErrPathNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			witness = append(witness, model.AnchorWitness{Anchor: a, OID: model.SHA(oid)})
		case model.AnchorCommit:
			witness = append(witness, model.AnchorWitness{Anchor: a, OID: model.SHA(a.Value)})
		case model.AnchorBranch:
			// Branch anchors are not witnessed and not drift-checked.
		}
	}
	return witness, nil
}

// witnessIndex maps each witnessed anchor to its witness for O(1) lookup.
func witnessIndex(witness []model.AnchorWitness) map[model.Anchor]model.AnchorWitness {
	m := make(map[model.Anchor]model.AnchorWitness, len(witness))
	for _, w := range witness {
		m[w.Anchor] = w
	}
	return m
}

// noteVerdict computes the single review verdict for n against live content at
// head, returning "" when the note is fresh. Precedence is
// EXPIRED > UNVERIFIED > DRIFTED > STALE; dangling supersede edges are surfaced
// separately by reviewNotes. An unborn HEAD skips drift detection. When
// worktree is true, path anchors drift-check against the on-disk working-tree
// file rather than the committed blob at head.
func noteVerdict(ctx context.Context, s *store.Store, head model.SHA, n model.Note, now time.Time, staleAfter time.Duration, worktree bool) (string, error) {
	if n.StaleAt != 0 {
		return verdictExpired, nil
	}
	if n.VerifiedAt == 0 {
		return verdictUnverified, nil
	}
	if head != "" || worktree {
		drifted, err := noteDrifted(ctx, s, head, n, worktree)
		if err != nil {
			return "", err
		}
		if drifted {
			return verdictDrifted, nil
		}
	}
	if now.Sub(time.Unix(n.VerifiedAt, 0)) > staleAfter {
		return verdictStale, nil
	}
	return "", nil
}

// noteDrifted reports whether any witnessed anchor no longer matches live
// content at head: a path or directory whose content oid changed or vanished
// (a directory's witness is its tree oid, so any change under the subtree
// drifts), or a commit no longer reachable from head. Anchors without a
// recorded witness are not drift-checked. When worktree is true, a path
// anchor's live oid is the on-disk working-tree blob (WorktreeBlobOID), so an
// uncommitted edit drifts the note; directory and commit anchors keep their
// HEAD-based check.
func noteDrifted(ctx context.Context, s *store.Store, head model.SHA, n model.Note, worktree bool) (bool, error) {
	byAnchor := witnessIndex(n.Witness)
	for _, a := range n.Anchors {
		w, ok := byAnchor[a]
		if !ok {
			continue
		}
		switch a.Kind {
		case model.AnchorPath, model.AnchorDir:
			oid, err := liveAnchorOID(ctx, s, head, a, worktree)
			if errors.Is(err, gitcmd.ErrPathNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			if model.SHA(oid) != w.OID {
				return true, nil
			}
		case model.AnchorCommit:
			reachable, err := s.Repo.IsAncestor(ctx, model.SHA(a.Value), head)
			if errors.Is(err, gitobj.ErrCommitNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			if !reachable {
				return true, nil
			}
		}
	}
	return false, nil
}

// liveAnchorOID resolves the current content oid of a path or directory anchor.
// A path anchor under worktree mode reads the on-disk working-tree blob
// (WorktreeBlobOID), surfacing an uncommitted edit as drift; otherwise, and
// always for a directory anchor, it reads the committed object at head
// (PathOID). A missing path wraps gitcmd.ErrPathNotFound either way.
func liveAnchorOID(ctx context.Context, s *store.Store, head model.SHA, a model.Anchor, worktree bool) (string, error) {
	if worktree && a.Kind == model.AnchorPath {
		return s.Git.WorktreeBlobOID(ctx, a.Value)
	}
	return s.Git.PathOID(ctx, string(head), a.Value)
}

// reviewNotes folds the review set (non-deleted, including superseded for
// dangling detection) and returns each flagged note with its verdict. A
// non-superseded note carries its content verdict (UNVERIFIED/DRIFTED/STALE);
// a superseded note is surfaced only when its edge dangles — it points at a
// note that has been tombstoned. Fresh notes are dropped. Order follows
// ListNotes: creation time then id.
func reviewNotes(ctx context.Context, s *store.Store, head model.SHA, now time.Time, staleAfter time.Duration) ([]reviewedNote, error) {
	all, err := s.ListNotes(ctx, false, true)
	if err != nil {
		return nil, err
	}
	exists := make(map[model.EntityID]bool, len(all))
	for _, n := range all {
		exists[n.ID] = true
	}
	var reviewed []reviewedNote
	for _, n := range all {
		if len(n.SupersededBy) > 0 {
			if supersedeDangling(n, exists) {
				reviewed = append(reviewed, reviewedNote{note: n, verdict: verdictDangling})
			}
			continue
		}
		verdict, err := noteVerdict(ctx, s, head, n, now, staleAfter, false)
		if err != nil {
			return nil, err
		}
		if verdict != "" {
			reviewed = append(reviewed, reviewedNote{note: n, verdict: verdict})
		}
	}
	return reviewed, nil
}

// supersedeDangling reports whether any of n's supersede targets has been
// tombstoned — absent from the live (non-deleted) set. A chain whose target is
// itself superseded but still live is valid, not dangling.
func supersedeDangling(n model.Note, exists map[model.EntityID]bool) bool {
	for _, target := range n.SupersededBy {
		if !exists[target] {
			return true
		}
	}
	return false
}

// reverseSupersedes returns the ids of notes that supersede id, sorted: the
// reverse of the supersede edge, computed at read.
func reverseSupersedes(ctx context.Context, s *store.Store, id model.EntityID) ([]model.EntityID, error) {
	all, err := s.ListNotes(ctx, false, true)
	if err != nil {
		return nil, err
	}
	var out []model.EntityID
	for _, n := range all {
		if slices.Contains(n.SupersededBy, id) {
			out = append(out, n.ID)
		}
	}
	slices.Sort(out)
	return out, nil
}

// noteReviewCount counts the notes needing review against live content.
func noteReviewCount(ctx context.Context, s *store.Store, head model.SHA, now time.Time, staleAfter time.Duration) (int, error) {
	reviewed, err := reviewNotes(ctx, s, head, now, staleAfter)
	if err != nil {
		return 0, err
	}
	return len(reviewed), nil
}
