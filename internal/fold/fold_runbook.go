package fold

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type runbookFolder struct {
	rb      model.Runbook
	labels  map[string]bool
	anchors map[model.Anchor]bool
	steps   []model.RunbookStep
	runs    []model.RunbookRun
}

func newRunbookFolder() *runbookFolder {
	return &runbookFolder{labels: map[string]bool{}, anchors: map[model.Anchor]bool{}}
}

func foldRunbook(ordered []model.PackCommit) (model.Runbook, error) {
	return run[model.Runbook](ordered, newRunbookFolder())
}

func (f *runbookFolder) fresh(sha model.SHA, createdAt int64) {
	f.rb = model.Runbook{ID: model.EntityID(sha), CreatedAt: createdAt, Comments: []model.Comment{}}
	f.steps = []model.RunbookStep{}
	f.runs = []model.RunbookRun{}
}

func (f *runbookFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Runbook)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-runbook folded as a runbook", ErrKindMismatch)
	}
	f.rb = seed
	f.rb.Comments = slices.Clone(seed.Comments)
	f.steps = slices.Clone(seed.Steps)
	f.runs = cloneRuns(seed.Runs)
	for _, l := range seed.Labels {
		f.labels[l] = true
	}
	for _, a := range seed.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *runbookFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateRunbook)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a runbook", ErrKindMismatch, op.OpKind())
	}
	f.rb.Title, f.rb.Description = o.Title, o.Description
	f.rb.Author = author
	f.rb.Status = model.RunbookActive
	for _, l := range o.Labels {
		f.labels[l] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *runbookFolder) apply(op model.Op, c model.PackCommit) error {
	if applyLabel(f.labels, op) || applyAnchor(f.anchors, op) || applyComment(&f.rb.Comments, op, c) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.rb.Title = o.Title
	case model.SetDescription:
		f.rb.Description = o.Description
	case model.SetRunbookStatus:
		applyRunbookStatus(&f.rb, o.Status, c.AuthorTime)
	case model.AddStep:
		if stepIndex(f.steps, o.ID) < 0 {
			f.steps = append(f.steps, model.RunbookStep(o))
		}
	case model.RemoveStep:
		if i := stepIndex(f.steps, o.ID); i >= 0 {
			f.steps = slices.Delete(f.steps, i, i+1)
		}
	case model.SetStepText:
		if i := stepIndex(f.steps, o.ID); i >= 0 {
			f.steps[i].Text = o.Text
		}
	case model.SetStepCommand:
		if i := stepIndex(f.steps, o.ID); i >= 0 {
			f.steps[i].Command = o.Command
		}
	case model.SetStepPosition:
		if i := stepIndex(f.steps, o.ID); i >= 0 {
			f.steps[i].Position = o.Position
		}
	case model.StartRun:
		if runIndex(f.runs, o.ID) < 0 {
			f.runs = append(f.runs, model.RunbookRun{
				ID:        o.ID,
				Task:      o.Task,
				Status:    model.RunRunning,
				Runner:    c.Author,
				StartedAt: c.AuthorTime,
				Results:   []model.RunbookStepResult{},
			})
		}
	case model.SetRunStepStatus:
		if i := runIndex(f.runs, o.RunID); i >= 0 {
			result := model.RunbookStepResult{StepID: o.StepID, Status: o.Status, Note: o.Note, Actor: c.Author, TS: c.AuthorTime}
			if j := resultIndex(f.runs[i].Results, o.StepID); j >= 0 {
				f.runs[i].Results[j] = result
			} else {
				f.runs[i].Results = append(f.runs[i].Results, result)
			}
		}
	case model.FinishRun:
		if i := runIndex(f.runs, o.ID); i >= 0 {
			f.runs[i].Status = o.Status
			f.runs[i].FinishedAt = c.AuthorTime
		}
	case model.DeleteNote:
		f.rb.Deleted = true
	default:
		return fmt.Errorf("%w: %s on a runbook", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *runbookFolder) touch(c model.PackCommit) {
	f.rb.UpdatedAt = c.AuthorTime
}

func (f *runbookFolder) finalize(head model.SHA) model.Runbook {
	f.rb.Labels = sortedKeys(f.labels)
	f.rb.Anchors = sortedAnchorsNil(f.anchors)
	slices.SortFunc(f.steps, func(a, b model.RunbookStep) int {
		if c := cmp.Compare(a.Position, b.Position); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	f.rb.Steps = f.steps
	f.rb.Runs = f.runs
	f.rb.Head = head
	return f.rb
}

func applyRunbookStatus(r *model.Runbook, status model.RunbookStatus, at int64) {
	r.Status = status
	switch status {
	case model.RunbookArchived:
		r.ArchivedAt = at
	case model.RunbookActive:
		r.ArchivedAt = 0
	}
}

func stepIndex(steps []model.RunbookStep, id string) int {
	for i := range steps {
		if steps[i].ID == id {
			return i
		}
	}
	return -1
}

func runIndex(runs []model.RunbookRun, id string) int {
	for i := range runs {
		if runs[i].ID == id {
			return i
		}
	}
	return -1
}

func resultIndex(results []model.RunbookStepResult, stepID string) int {
	for i := range results {
		if results[i].StepID == stepID {
			return i
		}
	}
	return -1
}

// cloneRuns deep-copies a seeded checkpoint's runs. slices.Clone alone is not
// enough: each run's Results slice would share its backing array with the
// checkpoint State, and fold.History re-folds prefixes over the same decoded
// chain, so an in-place result upsert in one prefix fold would corrupt the seed
// for the next.
func cloneRuns(runs []model.RunbookRun) []model.RunbookRun {
	out := slices.Clone(runs)
	for i := range out {
		out[i].Results = slices.Clone(out[i].Results)
	}
	return out
}
