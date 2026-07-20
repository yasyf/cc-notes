package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// PreparedRef is an immutable entity commit plus the exact ref comparison
// needed to publish it. Preparing writes Git objects but never moves refs.
type PreparedRef struct {
	Ref      string
	Old      model.SHA
	New      model.SHA
	Snapshot model.Snapshot
}

// RefUpdate returns the exact compare-and-swap for an aggregate transaction.
func (p PreparedRef) RefUpdate() gitcmd.RefUpdate {
	return gitcmd.RefUpdate{Ref: p.Ref, New: p.New, Old: p.Old}
}

// PrepareCreateExact writes an exact new entity chain without publishing its
// ref. The operation's durable nonce must already be present in the create op.
func (s *Store) PrepareCreateExact(ctx context.Context, ops []model.Op) (PreparedRef, error) {
	if len(ops) == 0 {
		return PreparedRef{}, errors.New("prepare create: no ops")
	}
	create, ok := ops[0].(model.CreateOp)
	if !ok {
		return PreparedRef{}, fmt.Errorf("prepare create: first op %s is not a create", ops[0].OpKind())
	}
	kind := create.CreateKind()
	pack, err := roundTrip(model.Pack{Lamport: 1, Session: session(), Ops: ops})
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare create %s: %w", kind, err)
	}
	sig, actor, err := s.signature(ctx)
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare create %s: %w", kind, err)
	}
	sha, err := s.Repo.WriteOpsCommit(ctx, nil, sig, "cc-notes: "+string(kind)+" create", pack)
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare create %s: %w", kind, err)
	}
	root := model.PackCommit{SHA: sha, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack}
	snapshot, err := fold.Fold([]model.PackCommit{root})
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare create %s: %w", kind, err)
	}
	return PreparedRef{
		Ref: refs.For(kind, model.EntityID(sha)), New: sha, Snapshot: snapshot,
	}, nil
}

// PrepareAppendAt writes an entity commit against one immutable expected tip
// without resolving or moving its ref.
func (s *Store) PrepareAppendAt(ctx context.Context, ref string, expected model.SHA, ops []model.Op) (PreparedRef, error) {
	if len(ops) == 0 {
		return PreparedRef{}, fmt.Errorf("prepare append to %s: no ops", ref)
	}
	if _, err := refs.Parse(ref); err != nil {
		return PreparedRef{}, fmt.Errorf("prepare append: %w", err)
	}
	if expected == "" {
		return PreparedRef{}, fmt.Errorf("prepare append to %s: expected tip is required", ref)
	}
	kinds := make([]string, len(ops))
	for index, operation := range ops {
		kinds[index] = operation.OpKind()
	}
	validated, err := roundTrip(model.Pack{Lamport: 1, Ops: ops})
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare append to %s: %w", ref, err)
	}
	chain, err := s.Repo.ReadChain(ctx, expected)
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare append to %s at %s: %w", ref, expected, err)
	}
	sig, actor, err := s.signature(ctx)
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare append to %s: %w", ref, err)
	}
	pack := model.Pack{Lamport: nextLamport(chain), Session: session(), Ops: validated.Ops}
	sha, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{expected}, sig, "cc-notes: "+strings.Join(kinds, " "), pack)
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare append to %s: %w", ref, err)
	}
	commit := model.PackCommit{
		SHA: sha, Parents: []model.SHA{expected}, Author: actor, AuthorTime: sig.When.Unix(), Pack: pack,
	}
	snapshot, err := fold.Fold(append(chain, commit))
	if err != nil {
		return PreparedRef{}, fmt.Errorf("prepare append to %s: %w", ref, err)
	}
	return PreparedRef{Ref: ref, Old: expected, New: sha, Snapshot: snapshot}, nil
}

// RememberPrepared admits an already-published prepared snapshot into the
// local fold cache. It does not inspect or move refs.
func (s *Store) RememberPrepared(prepared ...PreparedRef) {
	for _, value := range prepared {
		if value.New != "" && value.Snapshot != nil {
			s.cache.put(value.New, value.Snapshot)
		}
	}
}
