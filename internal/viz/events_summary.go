package viz

import (
	"context"
	"sort"

	"github.com/yasyf/cc-notes/model"
)

// summaryRow pairs an entity's legend summary with the creation time it sorts
// by, so the six kinds interleave into one created_at-then-id order.
type summaryRow struct {
	createdAt int64
	summary   EntitySummary
}

// entities builds the legend: one EntitySummary per entity across all six
// kinds, sorted by creation time then id. Superseded notes and docs stay in —
// the summary flags them — but tombstoned entities drop out.
func (b *Builder) entities(ctx context.Context) ([]EntitySummary, error) {
	var rows []summaryRow

	notes, err := b.store.ListNotes(ctx, false, true)
	if err != nil {
		return nil, err
	}
	for _, n := range notes {
		rows = append(rows, summaryRow{n.CreatedAt, noteSummary(n)})
	}
	docs, err := b.store.ListDocs(ctx, false, true)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		rows = append(rows, summaryRow{d.CreatedAt, docSummary(d)})
	}
	logs, err := b.store.ListLogs(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, l := range logs {
		rows = append(rows, summaryRow{l.CreatedAt, logSummary(l)})
	}
	tasks, err := b.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		rows = append(rows, summaryRow{t.CreatedAt, taskSummary(t)})
	}
	sprints, err := b.store.ListSprints(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range sprints {
		rows = append(rows, summaryRow{s.CreatedAt, sprintSummary(s)})
	}
	projects, err := b.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		rows = append(rows, summaryRow{p.CreatedAt, projectSummary(p)})
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

func noteSummary(n model.Note) EntitySummary {
	return EntitySummary{
		Kind:       entityNote,
		ID:         n.ID,
		Short:      n.ID.Short(),
		Title:      n.Title,
		VerifiedAt: n.VerifiedAt,
		Stale:      n.StaleAt != 0,
		Superseded: len(n.SupersededBy) > 0,
	}
}

func docSummary(d model.Doc) EntitySummary {
	return EntitySummary{
		Kind:       entityDoc,
		ID:         d.ID,
		Short:      d.ID.Short(),
		Title:      d.Title,
		VerifiedAt: d.VerifiedAt,
		Stale:      d.StaleAt != 0,
		Superseded: len(d.SupersededBy) > 0,
	}
}

func logSummary(l model.Log) EntitySummary {
	return EntitySummary{
		Kind:  entityLog,
		ID:    l.ID,
		Short: l.ID.Short(),
		Title: l.Title,
	}
}

func taskSummary(t model.Task) EntitySummary {
	return EntitySummary{
		Kind:      entityTask,
		ID:        t.ID,
		Short:     t.ID.Short(),
		Title:     t.Title,
		Status:    string(t.Status),
		Branch:    string(t.Branch),
		Assignee:  string(t.Assignee),
		StartedAt: t.StartedAt,
		ClosedAt:  t.ClosedAt,
		Sprint:    string(t.Sprint),
		Project:   string(t.Project),
	}
}

func sprintSummary(s model.Sprint) EntitySummary {
	return EntitySummary{
		Kind:      entitySprint,
		ID:        s.ID,
		Short:     s.ID.Short(),
		Title:     s.Title,
		Status:    string(s.Status),
		StartDate: s.StartDate,
		EndDate:   s.EndDate,
		Project:   string(s.Project),
	}
}

func projectSummary(p model.Project) EntitySummary {
	return EntitySummary{
		Kind:   entityProject,
		ID:     p.ID,
		Short:  p.ID.Short(),
		Title:  p.Title,
		Status: string(p.Status),
	}
}
