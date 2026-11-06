package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const checkpointFormat = 1

// checkpointInterval is how many lamports a fold from the root may run past
// the newest checkpoint entry before it writes a new one.
var checkpointInterval model.Lamport = 2000

// checkpointGeneration tags an entry with the fold semantics and op vocabulary
// of the binary that wrote it, as the fold cache tags its entries, so a binary
// that would fold the covered history differently never seeds from it.
var checkpointGeneration = foldCacheSubdir + " " + foldCacheGeneration

// checkpointFile is the one file of a checkpoint commit: the newest entry
// first, then the entry it replaced, kept as the seed for a merge whose side
// forked before the newest frontier.
type checkpointFile struct {
	Format  int               `json:"format"`
	Entries []checkpointEntry `json:"entries"`
}

// checkpointEntry is the snapshot Fold makes of Frontier's whole history,
// every commit of which has a lamport of at most CoversLamport.
type checkpointEntry struct {
	Generation    string          `json:"generation"`
	Frontier      model.SHA       `json:"frontier"`
	CoversLamport model.Lamport   `json:"covers_lamport"`
	StateKind     model.Kind      `json:"state_kind"`
	State         json.RawMessage `json:"state"`
}

// checkpointRef names ref's checkpoint ref when ref's kind keeps checkpoints:
// ledgers, the one kind whose chains grow to tens of thousands of commits.
func checkpointRef(ref string) (refs.Ref, string, bool) {
	parsed, err := refs.Parse(ref)
	if err != nil || parsed.Kind != model.KindLedger {
		return refs.Ref{}, "", false
	}
	return parsed, refs.CheckpointOf(ref), true
}

// readCheckpoint returns the checkpoint at cpRef and its commit, or a zero file
// and an empty sha when there is none. A commit that holds no file this binary
// decodes reads as one with no entries, since a checkpoint only ever speeds a
// fold up.
func (s *Store) readCheckpoint(ctx context.Context, cpRef string) (checkpointFile, model.SHA, error) {
	sha, err := s.Repo.Tip(ctx, cpRef)
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return checkpointFile{}, "", nil
	}
	if err != nil {
		return checkpointFile{}, "", err
	}
	var file checkpointFile
	data, err := s.Repo.ReadCheckpointCommit(ctx, sha)
	if err != nil || json.Unmarshal(data, &file) != nil {
		return checkpointFile{}, sha, nil
	}
	return file, sha, nil
}

func (s *Store) hasCheckpoint(ctx context.Context, ref string) (bool, error) {
	_, cpRef, ok := checkpointRef(ref)
	if !ok {
		return false, nil
	}
	_, err := s.Repo.Tip(ctx, cpRef)
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return false, nil
	}
	return err == nil, err
}

// AdoptCheckpoints moves each local checkpoint ref in fetched, keyed to the
// tracking ref a fetch just wrote the remote's copy to, onto the remote's copy
// unless the local one's newest entry this binary can seed from covers more:
// that one is a write this clone has not pushed yet. A tie takes the remote's,
// so clones converge.
func (s *Store) AdoptCheckpoints(ctx context.Context, fetched map[string]string) error {
	for local, tracking := range fetched {
		ours, oursSHA, err := s.readCheckpoint(ctx, local)
		if err != nil {
			return err
		}
		theirs, theirsSHA, err := s.readCheckpoint(ctx, tracking)
		if err != nil {
			return err
		}
		if theirsSHA == "" || theirsSHA == oursSHA || (oursSHA != "" && ours.newest() > theirs.newest()) {
			continue
		}
		if err := s.RecordsGit.UpdateRef(ctx, local, theirsSHA, oursSHA); err != nil && !errors.Is(err, gitcmd.ErrCASMismatch) {
			return err
		}
	}
	return nil
}

// newest returns the lamport the newest entry this binary can seed from
// covers, or zero when it can seed from none.
func (f checkpointFile) newest() model.Lamport {
	usable := f.usable()
	if len(usable) == 0 {
		return 0
	}
	return usable[0].CoversLamport
}

func (f checkpointFile) usable() []checkpointEntry {
	if f.Format != checkpointFormat {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(f.Entries), func(e checkpointEntry) bool { return e.Generation != checkpointGeneration })
}

// foldFromCheckpoint folds head from the newest entry of ref's checkpoint whose
// history provably sorts before everything else head holds, reading only the
// commits above it. ok is false when ref keeps no checkpoint or no entry
// qualifies, and the caller folds from the root.
func (s *Store) foldFromCheckpoint(ctx context.Context, ref string, head model.SHA) (_ model.Snapshot, top model.Lamport, ok bool, _ error) {
	parsed, cpRef, ok := checkpointRef(ref)
	if !ok {
		return nil, 0, false, nil
	}
	file, _, err := s.readCheckpoint(ctx, cpRef)
	if err != nil {
		return nil, 0, false, err
	}
	for _, entry := range file.usable() {
		snapshot, top, ok, err := s.resumeFrom(ctx, parsed, head, entry)
		if err != nil || ok {
			return snapshot, top, ok, err
		}
	}
	return nil, 0, false, nil
}

// resumeFrom folds head from entry when entry's history A is a prefix of
// Linearize's order of head's history. The walk above CoversLamport reads the
// suffix U, which holds no commit of A since every commit of A is at or below
// it. Every parent the walk stops at must be in A, and the frontier must be one
// of them, so head's history is exactly A and U. Each commit of A then ranks
// below each commit of U, A is closed under parents, and Linearize emits A
// first in A's own order, so Resume over U equals Fold over all of it.
func (s *Store) resumeFrom(ctx context.Context, parsed refs.Ref, head model.SHA, entry checkpointEntry) (model.Snapshot, model.Lamport, bool, error) {
	if entry.StateKind != parsed.Kind {
		return nil, 0, false, nil
	}
	suffix, stops, err := s.Repo.ReadSuffix(ctx, head, entry.CoversLamport)
	if errors.Is(err, gitobj.ErrIncompleteChain) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if _, ok := stops[entry.Frontier]; !ok {
		return nil, 0, false, nil
	}
	if slices.ContainsFunc(suffix, func(c model.PackCommit) bool { return slices.ContainsFunc(c.Pack.Ops, isCheckpoint) }) {
		return nil, 0, false, nil
	}
	reached, err := s.Repo.Reaches(ctx, entry.Frontier, stops)
	if errors.Is(err, gitobj.ErrIncompleteChain) {
		return nil, 0, false, nil
	}
	if err != nil || !reached {
		return nil, 0, false, err
	}
	state, err := entry.StateKind.DecodeSnapshot(entry.State)
	if err != nil || state.EntityID() != parsed.ID || state.Meta().Head != entry.Frontier {
		return nil, 0, false, nil
	}
	if len(suffix) == 0 {
		return state, entry.CoversLamport, true, nil
	}
	snapshot, err := fold.Resume(state, suffix)
	if err != nil {
		return nil, 0, false, nil
	}
	return snapshot, max(entry.CoversLamport, topLamport(suffix)), true, nil
}

// checkpointLags are how far below the newest commit a checkpoint's two
// frontiers sit, newest first. A merge seeds from an entry only when its side
// forked above the frontier, and a writer that has not synced yet holds a side
// that forked below the newest commit, so both frontiers trail it: the first
// past most such sides, the second past the deepest the drive ledgers show.
func checkpointLags() []model.Lamport {
	return []model.Lamport{checkpointInterval / 4, checkpointInterval + checkpointInterval/4}
}

// checkpointCandidate is one entry a fold from the root offers ref's
// checkpoint: state is Fold over frontier's whole history, whose top lamport
// is covers.
type checkpointCandidate struct {
	frontier model.SHA
	covers   model.Lamport
	state    model.Snapshot
}

// checkpointDue reports whether a fold from the root of a history whose top
// lamport is top owes ref a checkpoint: ref keeps them and top runs
// checkpointInterval past the newest entry this binary can seed from.
func (s *Store) checkpointDue(ctx context.Context, ref string, top model.Lamport) (bool, error) {
	_, cpRef, ok := checkpointRef(ref)
	if !ok {
		return false, nil
	}
	file, _, err := s.readCheckpoint(ctx, cpRef)
	if err != nil || file.Format > checkpointFormat {
		return false, err
	}
	newest := file.newest()
	return top >= newest && top-newest >= checkpointInterval, nil
}

// checkpointFromChain records ref's checkpoint from chain, head's whole
// history folded from the root, when one is due. Each frontier is head's
// first-parent ancestor one of checkpointLags lamports down, and its state is
// Fold over that frontier's own history. A history holding a checkpoint commit
// records none.
func (s *Store) checkpointFromChain(ctx context.Context, ref string, head model.SHA, chain []model.PackCommit) error {
	top := topLamport(chain)
	if fold.ExtendFloor(chain) != 0 {
		return nil
	}
	due, err := s.checkpointDue(ctx, ref, top)
	if err != nil || !due {
		return err
	}
	byID := make(map[model.SHA]model.PackCommit, len(chain))
	for _, c := range chain {
		byID[c.SHA] = c
	}
	var entries []checkpointCandidate
	for _, lag := range checkpointLags() {
		if top < lag {
			break
		}
		frontier := byID[head]
		for frontier.Pack.Lamport > top-lag && len(frontier.Parents) > 0 {
			frontier = byID[frontier.Parents[0]]
		}
		covered := []model.PackCommit{frontier}
		seen := map[model.SHA]bool{frontier.SHA: true}
		for i := 0; i < len(covered); i++ {
			for _, parent := range covered[i].Parents {
				if !seen[parent] {
					seen[parent] = true
					covered = append(covered, byID[parent])
				}
			}
		}
		state, err := fold.Fold(covered)
		if err != nil {
			return nil
		}
		entries = append(entries, checkpointCandidate{frontier: frontier.SHA, covers: topLamport(covered), state: state})
	}
	return s.recordCheckpoint(ctx, ref, entries)
}

// recordCheckpoint makes entries, newest first, ref's checkpoint. Only a fold
// from the root records one, so a bad entry never seeds the next. Nothing is
// recorded when the newest entry covers no more than the newest usable one,
// or when any state skipped ops. A writer that loses the race to another
// leaves the winner's.
func (s *Store) recordCheckpoint(ctx context.Context, ref string, entries []checkpointCandidate) error {
	_, cpRef, ok := checkpointRef(ref)
	if !ok || len(entries) == 0 {
		return nil
	}
	file, old, err := s.readCheckpoint(ctx, cpRef)
	if err != nil || file.Format > checkpointFormat || entries[0].covers <= file.newest() {
		return err
	}
	written := checkpointFile{Format: checkpointFormat}
	for _, entry := range entries {
		if entry.state.Meta().SkippedOps > 0 {
			return nil
		}
		state, err := json.Marshal(entry.state)
		if err != nil {
			return err
		}
		written.Entries = append(written.Entries, checkpointEntry{Generation: checkpointGeneration, Frontier: entry.frontier, CoversLamport: entry.covers, StateKind: entry.state.Meta().Kind, State: state})
	}
	encoded, err := json.Marshal(written)
	if err != nil {
		return err
	}
	sha, err := s.Repo.WriteCheckpointCommit(ctx, encoded)
	if err != nil {
		return err
	}
	if err := s.RecordsGit.UpdateRef(ctx, cpRef, sha, old); !errors.Is(err, gitcmd.ErrCASMismatch) {
		return err
	}
	return nil
}
