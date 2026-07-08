package store

import (
	"context"
	"slices"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// findDuplicate scans the live entities of kind for one whose folded content
// equals the candidate pack's, returning it (the oldest on a tie, since the
// List functions order by CreatedAt then id) or nil when none matches. It backs
// Create's best-effort duplicate guard: the comparison is over folded state —
// so flag-order permutations of tags and anchors still match — and ignores every
// identity, provenance, and lifecycle field (id, nonce, author, timestamps,
// verify/stale/supersede/tombstone state, task status/assignee/heartbeat/
// comments/commits, and the chain head). Tombstoned or superseded twins and
// closed tasks, sprints, and projects are filtered out by the live-set queries,
// so re-adding identical content after a delete or close roots a fresh entity.
// Stale (expired) notes and docs stay in the live set but the scan skips them,
// so re-asserting an expired fact roots a fresh entity rather than reviving the
// stale twin.
//
// The candidate is folded from a synthetic single-commit chain; its placeholder
// sha only feeds ignored fields, so it reuses fold's set-sorting and LWW
// normalization exactly as a persisted chain would.
//
// The scan runs only when dedupeCovered holds for the pack: a create pack that
// bundles any op folding into a field the comparator ignores skips dedupe and
// creates normally, so reusing an existing entity can never silently drop one
// of the pack's ops.
func (s *Store) findDuplicate(ctx context.Context, kind string, pack model.Pack) (model.Snapshot, error) {
	if !dedupeCovered(pack.Ops) {
		return nil, nil
	}
	candidate := []model.PackCommit{{SHA: "candidate", Pack: pack}}
	switch kind {
	case "note":
		cand, err := fold.Note(candidate)
		if err != nil {
			return nil, err
		}
		notes, err := s.ListNotes(ctx, false, false)
		if err != nil {
			return nil, err
		}
		for _, n := range notes {
			// An expired note never blocks re-asserting its fact — skip it so a
			// repeat add roots a fresh note instead of reviving the stale twin.
			if n.StaleAt == 0 && sameNoteContent(cand, n) {
				return n, nil
			}
		}
	case "doc":
		cand, err := fold.Doc(candidate)
		if err != nil {
			return nil, err
		}
		docs, err := s.ListDocs(ctx, false, false)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			// An expired doc never blocks re-asserting its fact — skip it so a
			// repeat add roots a fresh doc instead of reviving the stale twin.
			if d.StaleAt == 0 && sameDocContent(cand, d) {
				return d, nil
			}
		}
	case "log":
		cand, err := fold.Log(candidate)
		if err != nil {
			return nil, err
		}
		logs, err := s.ListLogs(ctx, false)
		if err != nil {
			return nil, err
		}
		for _, l := range logs {
			if sameLogContent(cand, l) {
				return l, nil
			}
		}
	case "task":
		cand, err := fold.Task(candidate)
		if err != nil {
			return nil, err
		}
		tasks, err := s.ListTasks(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			if t.ClosedAt == 0 && sameTaskContent(cand, t) {
				return t, nil
			}
		}
	case "sprint":
		cand, err := fold.Sprint(candidate)
		if err != nil {
			return nil, err
		}
		sprints, err := s.ListSprints(ctx)
		if err != nil {
			return nil, err
		}
		for _, sp := range sprints {
			if sp.ClosedAt == 0 && sameSprintContent(cand, sp) {
				return sp, nil
			}
		}
	case "project":
		cand, err := fold.Project(candidate)
		if err != nil {
			return nil, err
		}
		projects, err := s.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			if p.ClosedAt == 0 && sameProjectContent(cand, p) {
				return p, nil
			}
		}
	}
	return nil, nil
}

// dedupeCovered reports whether every op in a create pack folds only into a
// field the kind's content comparator checks — the invariant that lets Create
// return an existing exact-duplicate entity in place of writing a twin without
// dropping anything. The allow-list is the closed set of ops the CLI, the notes
// client, and the FUSE mount bundle into a create pack: the six Create* roots,
// AddAttachment (note/doc/log), the task's SetSprint/SetProject/AddCriterion/
// AddDep, and the sprint's SetStartDate/SetEndDate — each of which lands in a
// compared field. Any op outside the list skips dedupe, most importantly
// AppendEntry: the FUSE NewLog path bundles it, and the log comparator ignores
// Entries, so a log created with initial entries must root fresh rather than
// collapse into an entry-less twin whose entries would be lost.
func dedupeCovered(ops []model.Op) bool {
	for _, op := range ops {
		switch op.(type) {
		case model.CreateNote, model.CreateDoc, model.CreateLog,
			model.CreateTask, model.CreateSprint, model.CreateProject,
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

// sameLogContent compares two logs by their create-pack content — Title, Tags,
// Anchors, and Attachments — and deliberately ignores Entries. The CLI never
// bundles entries into a log's create pack (AppendEntry is a separate append),
// so a candidate folded from the create pack always carries empty Entries;
// comparing them would only match an entry-less log and would mint a twin on a
// repeat `log add T --entry Y`. With Entries excluded, a repeat add converges on
// the existing log and the caller appends the new entry to it. A store-level
// pack that does bundle AppendEntry (the FUSE NewLog path) skips dedupe via
// dedupeCovered, so nothing is dropped there either.
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

// sameCriteria compares criteria by their content — text and check script —
// ignoring the per-criterion nonce id and the validation status, so two tasks
// carrying the same acceptance criteria dedupe regardless of criterion ids.
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
