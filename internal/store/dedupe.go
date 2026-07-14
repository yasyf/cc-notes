package store

import (
	"context"
	"slices"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// dupChecker scans one kind's live entities for an exact-content duplicate of a
// create candidate. Each row binds scanDup to that kind's folder, list method,
// liveness predicate, and content comparator — the genuinely per-kind identity
// semantics. dupCheckers must cover exactly model.Kinds() (TestDupCheckersCoverKinds).
type dupChecker func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error)

var dupCheckers = map[model.Kind]dupChecker{
	model.KindNote: func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error) {
		return scanDup(candidate, fold.Note,
			func() ([]model.Note, error) { return s.ListNotes(ctx, false, false) },
			func(n model.Note) bool { return n.StaleAt == 0 }, sameNoteContent)
	},
	model.KindDoc: func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error) {
		return scanDup(candidate, fold.Doc,
			func() ([]model.Doc, error) { return s.ListDocs(ctx, false, false) },
			func(d model.Doc) bool { return d.StaleAt == 0 }, sameDocContent)
	},
	model.KindLog: func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error) {
		return scanDup(candidate, fold.Log,
			func() ([]model.Log, error) { return s.ListLogs(ctx, false) },
			func(model.Log) bool { return true }, sameLogContent)
	},
	model.KindTask: func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error) {
		return scanDup(candidate, fold.Task,
			func() ([]model.Task, error) { return s.ListTasks(ctx) },
			func(t model.Task) bool { return t.ClosedAt == 0 }, sameTaskContent)
	},
	model.KindSprint: func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error) {
		return scanDup(candidate, fold.Sprint,
			func() ([]model.Sprint, error) { return s.ListSprints(ctx) },
			func(sp model.Sprint) bool { return sp.ClosedAt == 0 }, sameSprintContent)
	},
	model.KindProject: func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error) {
		return scanDup(candidate, fold.Project,
			func() ([]model.Project, error) { return s.ListProjects(ctx) },
			func(p model.Project) bool { return p.ClosedAt == 0 }, sameProjectContent)
	},
	model.KindRunbook: func(s *Store, ctx context.Context, candidate []model.PackCommit) (model.Snapshot, error) {
		return scanDup(candidate, fold.Runbook,
			func() ([]model.Runbook, error) { return s.ListRunbooks(ctx) },
			liveRunbook, sameRunbookContent)
	},
}

// liveRunbook reports whether rb is a valid dedupe target: active and not
// tombstoned. ListRunbooks already hides tombstones, so the !Deleted guard only
// bites when a deleted runbook reaches the scan directly.
func liveRunbook(rb model.Runbook) bool {
	return rb.ArchivedAt == 0 && !rb.Deleted
}

func (s *Store) findDuplicate(ctx context.Context, kind model.Kind, pack model.Pack) (model.Snapshot, error) {
	if !dedupeCovered(pack.Ops) {
		return nil, nil
	}
	check, ok := dupCheckers[kind]
	if !ok {
		return nil, nil
	}
	candidate := []model.PackCommit{{SHA: "candidate", Pack: pack}}
	return check(s, ctx, candidate)
}

func scanDup[S model.Snapshot](
	candidate []model.PackCommit,
	foldCand func([]model.PackCommit) (S, error),
	list func() ([]S, error),
	live func(S) bool,
	same func(a, b S) bool,
) (model.Snapshot, error) {
	cand, err := foldCand(candidate)
	if err != nil {
		return nil, err
	}
	items, err := list()
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if live(it) && same(cand, it) {
			return it, nil
		}
	}
	return nil, nil
}

func dedupeCovered(ops []model.Op) bool {
	for _, op := range ops {
		switch op.(type) {
		case model.CreateNote, model.CreateDoc, model.CreateLog,
			model.CreateTask, model.CreateSprint, model.CreateProject,
			model.CreateRunbook, model.AddStep,
			model.AddAttachment,
			model.SetSprint, model.SetProject,
			model.AddCriterion, model.AddDep,
			model.SetStartDate, model.SetEndDate:
		default:
			return false
		}
	}
	return true
}

func sameNoteContent(a, b model.Note) bool {
	return a.Title == b.Title &&
		a.Body == b.Body &&
		slices.Equal(a.Tags, b.Tags) &&
		slices.Equal(a.Anchors, b.Anchors) &&
		slices.Equal(a.Attachments, b.Attachments)
}

func sameDocContent(a, b model.Doc) bool {
	return a.Title == b.Title &&
		a.Body == b.Body &&
		a.When == b.When &&
		slices.Equal(a.Tags, b.Tags) &&
		slices.Equal(a.Anchors, b.Anchors) &&
		slices.Equal(a.Attachments, b.Attachments)
}

func sameLogContent(a, b model.Log) bool {
	return a.Title == b.Title &&
		slices.Equal(a.Tags, b.Tags) &&
		slices.Equal(a.Anchors, b.Anchors) &&
		slices.Equal(a.Attachments, b.Attachments)
}

func sameTaskContent(a, b model.Task) bool {
	return a.Branch == b.Branch &&
		a.Title == b.Title &&
		a.Description == b.Description &&
		a.Type == b.Type &&
		a.Priority == b.Priority &&
		a.Parent == b.Parent &&
		a.Sprint == b.Sprint &&
		a.Project == b.Project &&
		slices.Equal(a.Labels, b.Labels) &&
		slices.Equal(a.BlockedBy, b.BlockedBy) &&
		sameCriteria(a.Criteria, b.Criteria)
}

func sameCriteria(a, b []model.Criterion) bool {
	return slices.EqualFunc(a, b, func(x, y model.Criterion) bool {
		return x.Text == y.Text && x.Script == y.Script
	})
}

func sameSprintContent(a, b model.Sprint) bool {
	return a.Project == b.Project &&
		a.Title == b.Title &&
		a.Description == b.Description &&
		a.StartDate == b.StartDate &&
		a.EndDate == b.EndDate &&
		slices.Equal(a.Labels, b.Labels)
}

func sameProjectContent(a, b model.Project) bool {
	return a.Title == b.Title &&
		a.Description == b.Description &&
		slices.Equal(a.Labels, b.Labels)
}

func sameRunbookContent(a, b model.Runbook) bool {
	return a.Title == b.Title &&
		a.Description == b.Description &&
		slices.Equal(a.Labels, b.Labels) &&
		slices.Equal(a.Anchors, b.Anchors) &&
		sameSteps(a.Steps, b.Steps)
}

// sameSteps compares steps by content — text and command, in folded (position)
// order — ignoring the per-step nonce id and the position encoding itself.
func sameSteps(a, b []model.RunbookStep) bool {
	return slices.EqualFunc(a, b, func(x, y model.RunbookStep) bool {
		return x.Text == y.Text && x.Command == y.Command
	})
}
