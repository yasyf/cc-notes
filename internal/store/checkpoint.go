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
// unless the local one's newest entry covers more: that one is a write this
// clone has not pushed yet. A tie takes the remote's, so clones converge.
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
		if theirsSHA == "" || theirsSHA == oursSHA || (oursSHA != "" && ours.covers() > theirs.covers()) {
			continue
		}
		if err := s.RecordsGit.UpdateRef(ctx, local, theirsSHA, oursSHA); err != nil && !errors.Is(err, gitcmd.ErrCASMismatch) {
			return err
		}
	}
	return nil
}

func (f checkpointFile) covers() model.Lamport {
	if f.Format != checkpointFormat || len(f.Entries) == 0 {
		return 0
	}
	return f.Entries[0].CoversLamport
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

// writeCheckpoint records snapshot, a fold from the root of head's whole
// history, as the newest entry of ref's checkpoint once that history's top
// lamport runs checkpointInterval past the newest entry this binary can seed
// from. Only a fold from the root writes one, so a bad entry never seeds the
// next. A history holding a checkpoint commit, or ops this binary skipped,
// writes none. A writer that loses the race to another leaves the winner's.
func (s *Store) writeCheckpoint(ctx context.Context, ref string, head model.SHA, top model.Lamport, snapshot model.Snapshot) error {
	_, cpRef, ok := checkpointRef(ref)
	if !ok || snapshot.Meta().SkippedOps > 0 {
		return nil
	}
	file, old, err := s.readCheckpoint(ctx, cpRef)
	if err != nil || file.Format > checkpointFormat {
		return err
	}
	usable := file.usable()
	var newest model.Lamport
	if len(usable) > 0 {
		newest = usable[0].CoversLamport
	}
	if top < newest || top-newest < checkpointInterval {
		return nil
	}
	state, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	entries := []checkpointEntry{{Generation: checkpointGeneration, Frontier: head, CoversLamport: top, StateKind: snapshot.Meta().Kind, State: state}}
	if len(usable) > 0 {
		entries = append(entries, usable[0])
	}
	data, err := json.Marshal(checkpointFile{Format: checkpointFormat, Entries: entries})
	if err != nil {
		return err
	}
	sha, err := s.Repo.WriteCheckpointCommit(ctx, data)
	if err != nil {
		return err
	}
	if err := s.RecordsGit.UpdateRef(ctx, cpRef, sha, old); !errors.Is(err, gitcmd.ErrCASMismatch) {
		return err
	}
	return nil
}
