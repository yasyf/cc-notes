package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// Create roots a new entity chain from ops (beginning with the create op). An
// exact duplicate of a live entity of the same kind writes nothing and returns
// a *DuplicateError carrying the survivor; the scan is best-effort and takes no
// lock, so two truly concurrent creates can still both land.
func (s *Store) Create(ctx context.Context, ops []model.Op) (model.Snapshot, error) {
	return s.create(ctx, ops, true)
}

// CreateExact roots an entity without content deduplication. The caller must
// provide its durable create nonce and enforce idempotency before calling.
func (s *Store) CreateExact(ctx context.Context, ops []model.Op) (model.Snapshot, error) {
	return s.create(ctx, ops, false)
}

func (s *Store) create(ctx context.Context, ops []model.Op, deduplicate bool) (model.Snapshot, error) {
	if len(ops) == 0 {
		return nil, errors.New("create: no ops")
	}
	create, ok := ops[0].(model.CreateOp)
	if !ok {
		return nil, fmt.Errorf("create: first op is %s, want create_note, create_task, create_sprint, create_project, create_doc, create_log, create_runbook, or create_investigation", ops[0].OpKind())
	}
	kind := create.CreateKind()
	pack, err := roundTrip(model.Pack{Lamport: 1, Session: session(), Ops: ops})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	if deduplicate {
		existing, err := s.findDuplicate(ctx, kind, pack)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", kind, err)
		}
		if existing != nil {
			return nil, &DuplicateError{Kind: kind, Existing: existing}
		}
	}
	sig, actor, err := s.signature(ctx)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	sha, err := s.Repo.WriteOpsCommit(ctx, nil, sig, "cc-notes: "+string(kind)+" create", pack)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	root := model.PackCommit{SHA: sha, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
	snapshot, err := fold.Fold([]model.PackCommit{root})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	if err := s.Git.UpdateRef(ctx, refs.For(kind, model.EntityID(sha)), sha, ""); err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	s.cache.put(sha, snapshot)
	return snapshot, nil
}

// Append extends the chain at ref with one commit carrying ops under ref
// compare-and-swap with bounded retries, returning the new folded snapshot.
// Exhausting the retries fails wrapping ErrContended.
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
		pack := model.Pack{Lamport: nextLamport(chain), Session: session(), Ops: validated.Ops}
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
			s.cache.put(sha, snapshot)
			return snapshot, nil
		case errors.Is(err, gitcmd.ErrCASMismatch):
			lastErr = err
		default:
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
	}
	return nil, fmt.Errorf("append to %s: %w: %w", ref, ErrContended, lastErr)
}

// Compact collapses ref's chain into a checkpoint commit under ref
// compare-and-swap, preserving the entity id and folded State. It returns the
// post-compaction snapshot; exhausting the retries fails wrapping ErrContended.
func (s *Store) Compact(ctx context.Context, ref string) (model.Snapshot, error) {
	name, email, err := s.actor(ctx)
	if err != nil {
		return nil, fmt.Errorf("compact %s: %w", ref, err)
	}
	actor := model.Actor(name + " <" + email + ">")
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			if err := Backoff(ctx, attempt); err != nil {
				return nil, fmt.Errorf("compact %s: %w", ref, err)
			}
		}
		tip, err := s.Repo.Tip(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		chain, err := s.Repo.ReadChain(ctx, tip)
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		snap, err := fold.Fold(chain)
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		coversShas := make([]model.SHA, len(chain))
		for i, c := range chain {
			coversShas[i] = c.SHA
		}
		checkpoint := model.Checkpoint{
			EntityID:      snap.EntityID(),
			State:         snap,
			CoversLamport: nextLamport(chain) - 1,
			CoversShas:    coversShas,
		}
		pack, err := roundTrip(model.Pack{Lamport: nextLamport(chain), Session: session(), Ops: []model.Op{checkpoint}})
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		sig := gitobj.Signature{Name: name, Email: email, When: s.now()}
		sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{tip}, sig, "cc-notes: compact", pack)
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		commit := model.PackCommit{SHA: sha, Parents: []model.SHA{tip}, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
		snapshot, err := fold.Fold(append(chain, commit))
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		switch err := s.Git.UpdateRef(ctx, ref, sha, tip); {
		case err == nil:
			s.cache.put(sha, snapshot)
			return snapshot, nil
		case errors.Is(err, gitcmd.ErrCASMismatch):
			lastErr = err
		default:
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
	}
	return nil, fmt.Errorf("compact %s: %w: %w", ref, ErrContended, lastErr)
}

// Merge writes the union merge commit joining ours and theirs — two tips of the
// same entity — then compare-and-swaps ref from ours to it. A lost race fails
// with gitcmd.ErrCASMismatch.
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
	pack := model.Pack{Lamport: nextLamport(combined), Session: session()}
	sig, actor, err := s.signature(ctx)
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{ours, theirs}, sig, "cc-notes: merge", pack)
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	merge := model.PackCommit{SHA: sha, Parents: []model.SHA{ours, theirs}, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
	merged, err := fold.Fold(append(combined, merge))
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	if err := s.Git.UpdateRef(ctx, ref, sha, ours); err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	s.cache.put(sha, merged)
	return sha, nil
}
