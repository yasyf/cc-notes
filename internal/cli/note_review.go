package cli

import (
	"context"
	"errors"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// Note review verdicts. A note carries at most one: precedence is
// EXPIRED > UNVERIFIED > DRIFTED > STALE. DANGLING has no CLI alias — only
// notes.Client folds supersede edges. The CLI verdict strings alias the
// notes.Verdict constants that own the vocabulary.
const (
	verdictExpired    = string(notes.VerdictExpired)
	verdictUnverified = string(notes.VerdictUnverified)
	verdictDrifted    = string(notes.VerdictDrifted)
	verdictStale      = string(notes.VerdictStale)
)

// freshEntity carries the freshness-relevant fields a Note and a Doc share —
// anchors, content witness, last-verify time, the out-of-date flag, and
// supersede edges — so one verdict/drift implementation serves both kinds.
// model.Note and model.Doc both expose these as fields; the cli-local adapter
// (freshFromNote/freshFromDoc) projects them without colliding with any method.
type freshEntity struct {
	Anchors      []model.Anchor
	Witness      []model.AnchorWitness
	VerifiedAt   int64
	StaleAt      int64
	SupersededBy []model.EntityID
}

// freshFromNote projects a note onto its freshness fields.
func freshFromNote(n model.Note) freshEntity {
	return freshEntity{
		Anchors:      n.Anchors,
		Witness:      n.Witness,
		VerifiedAt:   n.VerifiedAt,
		StaleAt:      n.StaleAt,
		SupersededBy: n.SupersededBy,
	}
}

// freshFromDoc projects a doc onto its freshness fields.
func freshFromDoc(d model.Doc) freshEntity {
	return freshEntity{
		Anchors:      d.Anchors,
		Witness:      d.Witness,
		VerifiedAt:   d.VerifiedAt,
		StaleAt:      d.StaleAt,
		SupersededBy: d.SupersededBy,
	}
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

// verdictOf computes the single review verdict for fe against live content at
// head, returning "" when fresh. Precedence is
// EXPIRED > UNVERIFIED > DRIFTED > STALE; dangling supersede edges are surfaced
// separately, by notes.Client's ReviewNotes/ReviewDocs. An unborn HEAD skips
// drift detection.
// When worktree is true, path anchors drift-check against the on-disk
// working-tree file rather than the committed blob at head. noteVerdict and
// docVerdict are thin projections onto this shared core.
func verdictOf(ctx context.Context, s *store.Store, head model.SHA, fe freshEntity, now time.Time, staleAfter time.Duration, worktree bool) (string, error) {
	if fe.StaleAt != 0 {
		return verdictExpired, nil
	}
	if fe.VerifiedAt == 0 {
		return verdictUnverified, nil
	}
	if head != "" || worktree {
		drifted, err := driftedOf(ctx, s, head, fe, worktree)
		if err != nil {
			return "", err
		}
		if drifted {
			return verdictDrifted, nil
		}
	}
	if now.Sub(time.Unix(fe.VerifiedAt, 0)) > staleAfter {
		return verdictStale, nil
	}
	return "", nil
}

// driftedOf reports whether any witnessed anchor no longer matches live content
// at head: a path or directory whose content oid changed or vanished (a
// directory's witness is its tree oid, so any change under the subtree drifts),
// or a commit no longer reachable from head. Anchors without a recorded witness
// are not drift-checked. When worktree is true, a path anchor's live oid is the
// on-disk working-tree blob (WorktreeBlobOID), so an uncommitted edit drifts the
// entity; directory and commit anchors keep their HEAD-based check.
func driftedOf(ctx context.Context, s *store.Store, head model.SHA, fe freshEntity, worktree bool) (bool, error) {
	byAnchor := witnessIndex(fe.Witness)
	for _, a := range fe.Anchors {
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
			sha, err := s.Git.ResolveCommit(ctx, a.Value)
			if errors.Is(err, gitcmd.ErrRevNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			reachable, err := s.Repo.IsAncestor(ctx, sha, head)
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

// noteVerdict computes the single review verdict for n against live content at
// head, returning "" when the note is fresh. See verdictOf for precedence and
// the worktree semantics.
func noteVerdict(ctx context.Context, s *store.Store, head model.SHA, n model.Note, now time.Time, staleAfter time.Duration, worktree bool) (string, error) {
	return verdictOf(ctx, s, head, freshFromNote(n), now, staleAfter, worktree)
}

// docVerdict computes the single review verdict for d against live content at
// head, returning "" when the doc is fresh. A doc carries the same verdict set
// and precedence as a note. See verdictOf.
func docVerdict(ctx context.Context, s *store.Store, head model.SHA, d model.Doc, now time.Time, staleAfter time.Duration, worktree bool) (string, error) {
	return verdictOf(ctx, s, head, freshFromDoc(d), now, staleAfter, worktree)
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
