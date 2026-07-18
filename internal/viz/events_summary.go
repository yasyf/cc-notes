package viz

import (
	"context"
	"sort"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// summaryRow pairs an entity's legend summary with the creation time it sorts
// by, so all kinds interleave into one created_at-then-id order.
type summaryRow struct {
	createdAt int64
	summary   EntitySummary
}

// entities builds the legend: one EntitySummary per entity across all kinds,
// sorted by creation time then id. Superseded notes and docs stay in —
// the summary flags them — but tombstoned entities drop out.
func (b *Builder) entities(ctx context.Context) ([]EntitySummary, error) {
	var rows []summaryRow
	for _, kind := range model.Kinds() {
		snaps, err := b.store.ListSnapshots(ctx, kind, summaryListOpts(kind))
		if err != nil {
			return nil, err
		}
		for _, snap := range snaps {
			rows = append(rows, summaryRow{snap.Meta().CreatedAt.Unix(), summaryOf(snap)})
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].createdAt != rows[j].createdAt {
			return rows[i].createdAt < rows[j].createdAt
		}
		return rows[i].summary.ID < rows[j].summary.ID
	})
	out := make([]EntitySummary, len(rows))
	for i, r := range rows {
		out[i] = r.summary
	}
	return out, nil
}

// summaryListOpts is the inclusion filter each kind's legend row uses: notes
// and docs keep superseded entities (the summary flags them); every kind drops
// tombstoned entities.
func summaryListOpts(kind model.Kind) store.ListOpts {
	switch kind {
	case model.KindNote, model.KindDoc:
		return store.ListOpts{IncludeSuperseded: true}
	default:
		return store.ListOpts{}
	}
}
