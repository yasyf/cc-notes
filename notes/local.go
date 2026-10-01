package notes

import (
	"cmp"
	"context"
	"slices"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// LocalEntity is one entity the local policy keeps on this clone. Secluded
// reports whether the negative push refspec already guards it; an entity a
// default covers is secluded on its next write or sync.
type LocalEntity struct {
	Kind     model.Kind
	ID       model.EntityID
	Title    string
	Reason   string
	Secluded bool
}

// LocalEntities lists every entity the local policy keeps on this clone,
// sorted by kind then id. A default never covers an entity a remote already
// tracks.
func (c *Client) LocalEntities(ctx context.Context) ([]LocalEntity, error) {
	tips, err := c.s.Git.Refs(ctx, refs.Namespace)
	if err != nil {
		return nil, err
	}
	policy, err := c.s.LocalPolicy(ctx)
	if err != nil {
		return nil, err
	}
	secluded, err := c.s.Secluded()
	if err != nil {
		return nil, err
	}
	tracked, err := c.s.Git.Refs(ctx, "refs/cc-notes-sync/")
	if err != nil {
		return nil, err
	}
	published := map[string]bool{}
	for name := range tracked {
		if _, canonical, err := refs.ParseTracking(name); err == nil {
			published[canonical] = true
		}
	}
	var out []LocalEntity
	for ref := range tips {
		parsed, err := refs.Parse(ref)
		if err != nil {
			return nil, err
		}
		snap, err := c.s.Load(ctx, ref)
		if err != nil {
			return nil, err
		}
		meta := snap.Meta()
		reason := policy.Reason(meta)
		if published[ref] && store.Defaulted(reason) {
			continue
		}
		if reason != "" {
			out = append(out, LocalEntity{Kind: parsed.Kind, ID: parsed.ID, Title: meta.Title, Reason: reason, Secluded: secluded[ref]})
		}
	}
	slices.SortFunc(out, func(a, b LocalEntity) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// SetLocal labels the entity local, keeping it on this clone, or removes that
// label and labels it synced, publishing it past every default. It returns
// why the entity stays local afterwards, "" once it syncs.
func (c *Client) SetLocal(ctx context.Context, kind model.Kind, id model.EntityID, local bool) (string, error) {
	ops := []model.Op{labelOp(kind, store.LocalLabel, true)}
	if !local {
		ops = []model.Op{labelOp(kind, store.LocalLabel, false), labelOp(kind, store.SyncedLabel, true)}
	}
	snap, err := c.s.Append(ctx, refs.For(kind, id), ops)
	if err != nil {
		return "", err
	}
	policy, err := c.s.LocalPolicy(ctx)
	if err != nil {
		return "", err
	}
	return policy.Reason(snap.Meta()), nil
}

func labelOp(kind model.Kind, label string, add bool) model.Op {
	switch kind {
	case model.KindNote, model.KindDoc, model.KindLog, model.KindInvestigation, model.KindAnswer:
		if add {
			return model.AddTag{Tag: label}
		}
		return model.RemoveTag{Tag: label}
	default:
		if add {
			return model.AddLabel{Label: label}
		}
		return model.RemoveLabel{Label: label}
	}
}
