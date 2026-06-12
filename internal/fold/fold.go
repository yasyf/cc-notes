// Package fold turns an entity's commit DAG into deterministic state. It is
// the CRDT core: Linearize totally orders the commits, and Fold replays
// their operation packs into a snapshot. Scalars resolve last-write-wins
// under the linearization, sets resolve per-element last-op-wins, claim is
// conditional first-wins, and deletion is monotone. Fold never validates
// transitions — every replica must converge on whatever the history says;
// legality is an append-time concern. The package is pure: no git, no I/O.
package fold

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/internal/model"
)

var (
	// ErrNoCreate reports a chain whose first operation is not a create.
	ErrNoCreate = errors.New("first op is not a create")
	// ErrDuplicateCreate reports a chain with a second create operation.
	ErrDuplicateCreate = errors.New("duplicate create op")
	// ErrKindMismatch reports an op that does not apply to the chain's
	// entity kind, or a chain folded as the wrong kind.
	ErrKindMismatch = errors.New("entity kind mismatch")
)

// Fold linearizes the chain and replays its operation packs into a snapshot,
// dispatching on the create op: a create_note chain folds to model.Note, a
// create_task chain to model.Task. Set-valued snapshot fields come back as
// non-nil sorted slices; anchors sort by (kind, value).
func Fold(commits []model.PackCommit) (model.Snapshot, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return nil, err
	}
	switch first := firstOp(ordered).(type) {
	case model.CreateNote:
		return foldNote(ordered)
	case model.CreateTask:
		return foldTask(ordered)
	case nil:
		return nil, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	default:
		return nil, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
}

// Note linearizes the chain and folds it as a note. It fails with
// ErrKindMismatch when the chain was created as a task.
func Note(commits []model.PackCommit) (model.Note, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Note{}, err
	}
	return foldNote(ordered)
}

// Task linearizes the chain and folds it as a task. It fails with
// ErrKindMismatch when the chain was created as a note.
func Task(commits []model.PackCommit) (model.Task, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Task{}, err
	}
	return foldTask(ordered)
}

func foldNote(ordered []model.PackCommit) (model.Note, error) {
	note := model.Note{
		ID:        model.EntityID(ordered[0].SHA),
		CreatedAt: ordered[0].AuthorTime,
		Head:      ordered[len(ordered)-1].SHA,
	}
	tags := map[string]bool{}
	anchors := map[model.Anchor]bool{}
	created := false
	for _, c := range ordered {
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateNote:
					created = true
					note.Title, note.Body, note.Author = o.Title, o.Body, c.Author
					for _, t := range o.Tags {
						tags[t] = true
					}
					for _, a := range o.Anchors {
						anchors[a] = true
					}
				case model.CreateTask:
					return model.Note{}, fmt.Errorf("%w: create_task chain folded as a note", ErrKindMismatch)
				default:
					return model.Note{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.CreateNote, model.CreateTask:
				return model.Note{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				note.Title = o.Title
			case model.SetBody:
				note.Body = o.Body
			case model.AddTag:
				tags[o.Tag] = true
			case model.RemoveTag:
				delete(tags, o.Tag)
			case model.AddAnchor:
				anchors[o.Anchor] = true
			case model.RemoveAnchor:
				delete(anchors, o.Anchor)
			case model.DeleteNote:
				note.Deleted = true
			default:
				return model.Note{}, fmt.Errorf("%w: %s on a note", ErrKindMismatch, op.OpKind())
			}
		}
		if len(c.Pack.Ops) > 0 {
			note.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Note{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	note.Tags = sortedKeys(tags)
	note.Anchors = sortedAnchors(anchors)
	return note, nil
}

func foldTask(ordered []model.PackCommit) (model.Task, error) {
	task := model.Task{
		ID:        model.EntityID(ordered[0].SHA),
		CreatedAt: ordered[0].AuthorTime,
		Head:      ordered[len(ordered)-1].SHA,
		Comments:  []model.Comment{},
	}
	labels := map[string]bool{}
	deps := map[model.EntityID]bool{}
	created := false
	for _, c := range ordered {
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateTask:
					created = true
					task.Title, task.Description = o.Title, o.Description
					task.Type, task.Priority = o.Type, o.Priority
					task.Branch, task.Parent = o.Branch, o.Parent
					task.Status = model.StatusOpen
					for _, l := range o.Labels {
						labels[l] = true
					}
				case model.CreateNote:
					return model.Task{}, fmt.Errorf("%w: create_note chain folded as a task", ErrKindMismatch)
				default:
					return model.Task{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.CreateNote, model.CreateTask:
				return model.Task{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				task.Title = o.Title
			case model.SetDescription:
				task.Description = o.Description
			case model.SetType:
				task.Type = o.Type
			case model.SetPriority:
				task.Priority = o.Priority
			case model.SetStatus:
				applyStatus(&task, o.Status, c.AuthorTime)
			case model.SetAssignee:
				task.Assignee = o.Assignee
			case model.Claim:
				if task.Status == model.StatusOpen && task.Assignee == "" {
					task.Status = model.StatusInProgress
					task.Assignee = o.Assignee
					task.StartedAt = c.AuthorTime
				}
			case model.AddLabel:
				labels[o.Label] = true
			case model.RemoveLabel:
				delete(labels, o.Label)
			case model.AddDep:
				deps[o.ID] = true
			case model.RemoveDep:
				delete(deps, o.ID)
			case model.SetParent:
				task.Parent = o.Parent
			case model.AddComment:
				task.Comments = append(task.Comments, model.Comment{Author: c.Author, TS: c.AuthorTime, Body: o.Body})
			case model.Promote:
				task.Branch = o.To
			default:
				return model.Task{}, fmt.Errorf("%w: %s on a task", ErrKindMismatch, op.OpKind())
			}
		}
		if len(c.Pack.Ops) > 0 {
			task.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Task{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	task.Labels = sortedKeys(labels)
	task.BlockedBy = sortedKeys(deps)
	return task, nil
}

func applyStatus(task *model.Task, status model.Status, at int64) {
	if status == model.StatusInProgress && task.Status != model.StatusInProgress {
		task.StartedAt = at
	}
	task.Status = status
	switch status {
	case model.StatusDone, model.StatusCancelled:
		task.ClosedAt = at
	case model.StatusOpen, model.StatusInProgress:
		task.ClosedAt = 0
	}
}

func firstOp(ordered []model.PackCommit) model.Op {
	for _, c := range ordered {
		if len(c.Pack.Ops) > 0 {
			return c.Pack.Ops[0]
		}
	}
	return nil
}

func sortedKeys[K cmp.Ordered](set map[K]bool) []K {
	keys := make([]K, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func sortedAnchors(set map[model.Anchor]bool) []model.Anchor {
	anchors := make([]model.Anchor, 0, len(set))
	for a := range set {
		anchors = append(anchors, a)
	}
	slices.SortFunc(anchors, func(a, b model.Anchor) int {
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return cmp.Compare(a.Value, b.Value)
	})
	return anchors
}
