package cli

import (
	"context"
	"errors"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// Note review verdicts the review filters select. notes.Client computes every
// verdict and owns the vocabulary; these alias its notes.Verdict constants.
const (
	verdictExpired    = string(notes.VerdictExpired)
	verdictUnverified = string(notes.VerdictUnverified)
	verdictDrifted    = string(notes.VerdictDrifted)
)

// resolveHead returns the commit the context checkout's HEAD points at, or ""
// when HEAD is unborn (a repository with no commits yet). An unborn HEAD means
// there is no live content to witness or drift-check against.
func resolveHead(ctx context.Context, s *store.Store) (model.SHA, error) {
	head, err := s.ContextRepo.Tip(ctx, "HEAD")
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
			oid, err := s.ContextRepo.PathOID(ctx, head, a.Value)
			if errors.Is(err, model.ErrPathNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			witness = append(witness, model.AnchorWitness{Anchor: a, OID: oid})
		case model.AnchorCommit:
			witness = append(witness, model.AnchorWitness{Anchor: a, OID: model.SHA(a.Value)})
		case model.AnchorBranch:
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
