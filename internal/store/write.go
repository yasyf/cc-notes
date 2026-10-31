package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// Create roots a new entity chain from ops (beginning with the create op). An
// exact duplicate of a live entity of the same kind writes nothing and returns
// a *DuplicateError carrying the survivor. The scan covers only entities whose
// fold this clone has cached, which includes everything it wrote or listed, and
// reads no chain from git, so a cold cache never makes a write fold the whole
// kind. It is best-effort and takes no lock, so two truly concurrent creates
// can still both land.
func (s *Store) Create(ctx context.Context, ops []model.Op) (model.Snapshot, error) {
	return s.create(ctx, ops, true)
}

// CreateExact roots an entity without content deduplication. The caller must
// provide its durable create nonce and enforce idempotency before calling.
func (s *Store) CreateExact(ctx context.Context, ops []model.Op) (model.Snapshot, error) {
	return s.create(ctx, ops, false)
}

func (s *Store) create(ctx context.Context, ops []model.Op, deduplicate bool) (model.Snapshot, error) {
	if err := s.CheckRecords(); err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	if len(ops) == 0 {
		return nil, errors.New("create: no ops")
	}
	create, ok := ops[0].(model.CreateOp)
	if !ok {
		return nil, fmt.Errorf("create: first op is %s, want create_note, create_task, create_sprint, create_project, create_doc, create_log, create_runbook, create_investigation, create_plan, or create_answer", ops[0].OpKind())
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
	snapshot, err := fold.Strict([]model.PackCommit{root})
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	ref := refs.For(kind, model.EntityID(sha))
	if err := s.track(ctx, ref, snapshot, nil); err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	if err := s.PublishRef(ctx, ref, sha, ""); err != nil {
		return nil, fmt.Errorf("create %s: %w", kind, err)
	}
	s.cache.putChain(sha, snapshot, []model.PackCommit{root})
	return snapshot, nil
}

// Append extends the chain at ref with one commit carrying ops under ref
// compare-and-swap with bounded retries, returning the new folded snapshot.
// Exhausting the retries fails wrapping ErrContended.
func (s *Store) Append(ctx context.Context, ref string, ops []model.Op) (model.Snapshot, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("append to %s: no ops", ref)
	}
	return s.appendBuilt(ctx, ref, func(context.Context, model.SHA) ([]model.Op, error) { return ops, nil })
}

// AppendFrom is Append with the ops rebuilt from the snapshot at the tip each
// attempt extends, so an edit derived from current state, such as text added
// to a body, keeps a concurrent writer's change instead of overwriting it.
func (s *Store) AppendFrom(ctx context.Context, ref string, build func(model.Snapshot) ([]model.Op, error)) (model.Snapshot, error) {
	return s.appendBuilt(ctx, ref, func(ctx context.Context, tip model.SHA) ([]model.Op, error) {
		snapshot, err := s.LoadAt(ctx, tip)
		if err != nil {
			return nil, err
		}
		return build(snapshot)
	})
}

func (s *Store) appendBuilt(ctx context.Context, ref string, build func(context.Context, model.SHA) ([]model.Op, error)) (model.Snapshot, error) {
	if err := s.CheckRecords(); err != nil {
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
		ops, err := build(ctx, tip)
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		if len(ops) == 0 {
			return nil, fmt.Errorf("append to %s: no ops", ref)
		}
		validated, err := roundTrip(model.Pack{Lamport: 1, Ops: ops})
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		kinds := make([]string, len(ops))
		for i, op := range ops {
			kinds[i] = op.OpKind()
		}
		top, extend, err := s.extender(ctx, tip, validated.Ops)
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		pack := model.Pack{Lamport: top + 1, Session: session(), Ops: validated.Ops}
		sig := gitobj.Signature{Name: name, Email: email, When: s.now()}
		sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{tip}, sig, "cc-notes: "+strings.Join(kinds, " "), pack)
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		commit := model.PackCommit{SHA: sha, Parents: []model.SHA{tip}, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
		snapshot, floor, err := extend(commit)
		if err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		if err := s.track(ctx, ref, snapshot, addedAttachments(ops)); err != nil {
			return nil, fmt.Errorf("append to %s: %w", ref, err)
		}
		switch err := s.PublishRef(ctx, ref, sha, tip); {
		case err == nil:
			s.cache.put(sha, snapshot, max(top, pack.Lamport), floor)
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
	if err := s.CheckRecords(); err != nil {
		return nil, fmt.Errorf("compact %s: %w", ref, err)
	}
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
		snap, err := fold.Strict(chain)
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
			CoversLamport: topLamport(chain),
			CoversShas:    coversShas,
		}
		pack, err := roundTrip(model.Pack{Lamport: topLamport(chain) + 1, Session: session(), Ops: []model.Op{checkpoint}})
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		sig := gitobj.Signature{Name: name, Email: email, When: s.now()}
		sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{tip}, sig, "cc-notes: compact", pack)
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		commit := model.PackCommit{SHA: sha, Parents: []model.SHA{tip}, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
		snapshot, err := fold.Strict(append(chain, commit))
		if err != nil {
			return nil, fmt.Errorf("compact %s: %w", ref, err)
		}
		switch err := s.PublishRef(ctx, ref, sha, tip); {
		case err == nil:
			s.cache.putChain(sha, snapshot, append(chain, commit))
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
// with gitcmd.ErrCASMismatch. The merge pack carries no ops of its own, so the
// fold stays tolerant: a chain a newer cc-notes wrote still merges here.
func (s *Store) Merge(ctx context.Context, ref string, ours, theirs model.SHA) (model.SHA, error) {
	if err := s.CheckRecords(); err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
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
	pack := model.Pack{Lamport: topLamport(combined) + 1, Session: session()}
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
	if err := s.PublishRef(ctx, ref, sha, ours); err != nil {
		return "", fmt.Errorf("merge %s: %w", ref, err)
	}
	s.cache.putChain(sha, merged, append(combined, merge))
	return sha, nil
}

func (s *Store) extender(ctx context.Context, tip model.SHA, ops []model.Op) (model.Lamport, func(model.PackCommit) (model.Snapshot, model.Lamport, error), error) {
	if !slices.ContainsFunc(ops, isCheckpoint) {
		if base, top, floor, ok := s.cache.getTop(tip); ok && top+1 > floor {
			return top, func(c model.PackCommit) (model.Snapshot, model.Lamport, error) {
				snapshot, err := fold.Extend(base, c)
				return snapshot, floor, err
			}, nil
		}
	}
	chain, err := s.Repo.ReadChain(ctx, tip)
	if err != nil {
		return 0, nil, err
	}
	return topLamport(chain), func(c model.PackCommit) (model.Snapshot, model.Lamport, error) {
		extended := append(chain, c)
		snapshot, err := fold.Strict(extended)
		return snapshot, fold.ExtendFloor(extended), err
	}, nil
}

func isCheckpoint(op model.Op) bool {
	_, ok := op.(model.Checkpoint)
	return ok
}

func addedAttachments(ops []model.Op) []model.Attachment {
	var out []model.Attachment
	for _, op := range ops {
		if add, ok := op.(model.AddAttachment); ok {
			out = append(out, model.Attachment(add))
		}
	}
	return out
}
