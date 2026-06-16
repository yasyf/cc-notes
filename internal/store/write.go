package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
)

// Create roots a new entity chain: ops must begin with the create op, which
// the caller stamps with a fresh nonce. The pack is written at lamport 1 as
// a parentless commit whose sha becomes the entity id, then the entity ref —
// refs.Note for a note, refs.Task for a task — is
// created atomically: a ref that already exists fails with
// gitcmd.ErrCASMismatch. Notes and tasks share a flat namespace keyed by
// entity id. The pack is validated and folded before the ref is created, so
// a bad op never publishes. It returns the folded snapshot.
func (s *Store) Create(ctx context.Context, ops []model.Op) (model.Snapshot, error) {
	if len(ops) == 0 {
		return nil, errors.New("create: no ops")
	}
	var kind string
	var refFor func(model.EntityID) string
	switch ops[0].(type) {
	case model.CreateNote:
		kind, refFor = "note", refs.Note
	case model.CreateTask:
		kind, refFor = "task", refs.Task
	default:
		return nil, fmt.Errorf("create: first op is %s, want create_note or create_task", ops[0].OpKind())
	}
	pack, err := roundTrip(model.Pack{Lamport: 1, Ops: ops})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	sig, actor, err := s.signature(ctx)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	sha, err := s.Repo.WriteOpsCommit(ctx, nil, sig, "cc-notes: "+kind+" create", pack)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	root := model.PackCommit{SHA: sha, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
	snapshot, err := fold.Fold([]model.PackCommit{root})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	if err := s.Git.UpdateRef(ctx, refFor(model.EntityID(sha)), sha, ""); err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	return snapshot, nil
}

// Append extends the chain at ref with one commit carrying ops, at lamport
// max(chain)+1, under ref compare-and-swap: a lost race re-reads the chain
// and retries with jittered backoff, and exhausting maxAttempts fails
// wrapping ErrContended. The pack is validated and folded before the ref
// moves, so a bad op never publishes. It returns the new folded snapshot.
func (s *Store) Append(ctx context.Context, ref string, ops []model.Op) (model.Snapshot, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("append to %s: no ops", ref)
	}
	kinds := make([]string, len(ops))
	for i, op := range ops {
		kinds[i] = op.OpKind()
	}
	message := "cc-notes: " + strings.Join(kinds, " ")
	validated, err := roundTrip(model.Pack{Lamport: 1, Ops: ops})
	if err != nil {
		return nil, fmt.Errorf("append to %s: %w", ref, err)
	}
	name, email, err := s.actor(ctx)
	if err != nil {
		return nil, fmt.Errorf("append to %s: %w", ref, err)
	}
	actor := model.Actor(name + " <" + email + ">")
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			if err := Backoff(ctx, attempt); err != nil {
				return nil, fmt.Errorf("append to %s: %w", ref, err)
			}
		}
		tip, err := s.Repo.Tip(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		chain, err := s.Repo.ReadChain(ctx, tip)
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		pack := model.Pack{Lamport: nextLamport(chain), Ops: validated.Ops}
		sig := gitobj.Signature{Name: name, Email: email, When: s.now()}
		sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{tip}, sig, message, pack)
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		commit := model.PackCommit{SHA: sha, Parents: []model.SHA{tip}, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
		snapshot, err := fold.Fold(append(chain, commit))
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		switch err := s.Git.UpdateRef(ctx, ref, sha, tip); {
		case err == nil:
			return snapshot, nil
		case errors.Is(err, gitcmd.ErrCASMismatch):
			lastErr = err
		default:
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
	}
	return nil, fmt.Errorf("append to %s: %w: %w", ref, ErrContended, lastErr)
}

// Merge writes the union merge commit joining ours and theirs — two tips of
// the same entity — carrying an empty-ops pack at lamport max(both chains)+1,
// then compare-and-swaps ref from ours to the merge. The union is folded
// before the ref moves, so merging two different entities never publishes. A
// lost race fails with gitcmd.ErrCASMismatch; the caller (internal/sync)
// re-reads the tips and redoes the merge.
func (s *Store) Merge(ctx context.Context, ref string, ours, theirs model.SHA) (model.SHA, error) {
	ourChain, err := s.Repo.ReadChain(ctx, ours)
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	theirChain, err := s.Repo.ReadChain(ctx, theirs)
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	seen := make(map[model.SHA]bool, len(ourChain)+len(theirChain))
	combined := make([]model.PackCommit, 0, len(ourChain)+len(theirChain)+1)
	for _, chain := range [][]model.PackCommit{ourChain, theirChain} {
		for _, c := range chain {
			if !seen[c.SHA] {
				seen[c.SHA] = true
				combined = append(combined, c)
			}
		}
	}
	pack := model.Pack{Lamport: nextLamport(combined)}
	sig, actor, err := s.signature(ctx)
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{ours, theirs}, sig, "cc-notes: merge", pack)
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	merge := model.PackCommit{SHA: sha, Parents: []model.SHA{ours, theirs}, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
	if _, err := fold.Fold(append(combined, merge)); err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	if err := s.Git.UpdateRef(ctx, ref, sha, ours); err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	return sha, nil
}
