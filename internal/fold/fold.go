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

	"github.com/yasyf/cc-notes/model"
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
// create_doc chain to model.Doc, a create_log chain to model.Log, a create_task
// chain to model.Task, a create_sprint chain to model.Sprint, a create_project
// chain to model.Project, and a create_runbook chain to model.Runbook.
// Set-valued snapshot fields come back as
// non-nil sorted slices; anchors sort by (kind, value). The one exception is
// Attachments (LWW by name), which sorts by name and comes back nil when
// empty — the field marshals omitempty, so attachment-less snapshots keep
// their pre-attachment bytes.
//
// A chain may contain Checkpoint commits. Fold receives the full chain (the
// create commit is always the root); when the newest checkpoint is seed-safe it
// starts the snapshot from Checkpoint.State and replays only the uncovered
// suffix, otherwise it folds the full history with every checkpoint a no-op.
// Either way the result equals folding the uncompacted history, so a compacted
// fold and a full fold converge on every replica.
func Fold(commits []model.PackCommit) (model.Snapshot, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return nil, err
	}
	switch first := firstOp(ordered).(type) {
	case model.CreateNote:
		return foldNote(ordered)
	case model.CreateDoc:
		return foldDoc(ordered)
	case model.CreateLog:
		return foldLog(ordered)
	case model.CreateTask:
		return foldTask(ordered)
	case model.CreateSprint:
		return foldSprint(ordered)
	case model.CreateProject:
		return foldProject(ordered)
	case model.CreateRunbook:
		return foldRunbook(ordered)
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

// Doc linearizes the chain and folds it as a doc. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Doc(commits []model.PackCommit) (model.Doc, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Doc{}, err
	}
	return foldDoc(ordered)
}

// Log linearizes the chain and folds it as a log. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Log(commits []model.PackCommit) (model.Log, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Log{}, err
	}
	return foldLog(ordered)
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

// Sprint linearizes the chain and folds it as a sprint. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Sprint(commits []model.PackCommit) (model.Sprint, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Sprint{}, err
	}
	return foldSprint(ordered)
}

// Project linearizes the chain and folds it as a project. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Project(commits []model.PackCommit) (model.Project, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Project{}, err
	}
	return foldProject(ordered)
}

// Runbook linearizes the chain and folds it as a runbook. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Runbook(commits []model.PackCommit) (model.Runbook, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Runbook{}, err
	}
	return foldRunbook(ordered)
}

func foldNote(ordered []model.PackCommit) (model.Note, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var note model.Note
	tags := map[string]bool{}
	anchors := map[model.Anchor]bool{}
	superseded := map[model.EntityID]bool{}
	attachments := map[string]model.Attachment{}
	var witness []model.AnchorWitness
	created := false
	if seeded {
		seed, ok := base.State.(model.Note)
		if !ok {
			return model.Note{}, fmt.Errorf("%w: checkpoint over a task folded as a note", ErrKindMismatch)
		}
		note = seed
		for _, t := range seed.Tags {
			tags[t] = true
		}
		for _, a := range seed.Anchors {
			anchors[a] = true
		}
		for _, id := range seed.SupersededBy {
			superseded[id] = true
		}
		for _, a := range seed.Attachments {
			attachments[a.Name] = a
		}
		witness = seed.Witness
		created = true
	} else {
		note = model.Note{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
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
				case model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
					return model.Note{}, fmt.Errorf("%w: %s chain folded as a note", ErrKindMismatch, op.OpKind())
				default:
					return model.Note{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
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
			case model.VerifyNote:
				note.VerifiedAt = c.AuthorTime
				note.VerifiedBy = c.Author
				note.VerifiedCommit = o.VerifiedCommit
				witness = o.Witness
				note.StaleAt, note.StaleBy, note.StaleReason = 0, "", ""
			case model.MarkStale:
				note.StaleAt, note.StaleBy, note.StaleReason = c.AuthorTime, c.Author, o.Reason
			case model.ClearStale:
				note.StaleAt, note.StaleBy, note.StaleReason = 0, "", ""
			case model.AddSupersededBy:
				superseded[o.ID] = true
			case model.RemoveSupersededBy:
				delete(superseded, o.ID)
			case model.AddAttachment:
				attachments[o.Name] = model.Attachment(o)
			case model.RemoveAttachment:
				delete(attachments, o.Name)
			default:
				return model.Note{}, fmt.Errorf("%w: %s on a note", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			note.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Note{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	note.Tags = sortedKeys(tags)
	note.Anchors = sortedAnchors(anchors)
	note.SupersededBy = sortedKeys(superseded)
	note.Attachments = sortedAttachments(attachments)
	note.Witness = witness
	note.Head = ordered[len(ordered)-1].SHA
	return note, nil
}

func foldDoc(ordered []model.PackCommit) (model.Doc, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var doc model.Doc
	tags := map[string]bool{}
	anchors := map[model.Anchor]bool{}
	superseded := map[model.EntityID]bool{}
	attachments := map[string]model.Attachment{}
	var witness []model.AnchorWitness
	created := false
	if seeded {
		seed, ok := base.State.(model.Doc)
		if !ok {
			return model.Doc{}, fmt.Errorf("%w: checkpoint over a non-doc folded as a doc", ErrKindMismatch)
		}
		doc = seed
		for _, t := range seed.Tags {
			tags[t] = true
		}
		for _, a := range seed.Anchors {
			anchors[a] = true
		}
		for _, id := range seed.SupersededBy {
			superseded[id] = true
		}
		for _, a := range seed.Attachments {
			attachments[a.Name] = a
		}
		witness = seed.Witness
		created = true
	} else {
		doc = model.Doc{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateDoc:
					created = true
					doc.Title, doc.Body, doc.When, doc.Author = o.Title, o.Body, o.When, c.Author
					for _, t := range o.Tags {
						tags[t] = true
					}
					for _, a := range o.Anchors {
						anchors[a] = true
					}
				case model.CreateNote, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
					return model.Doc{}, fmt.Errorf("%w: %s chain folded as a doc", ErrKindMismatch, op.OpKind())
				default:
					return model.Doc{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
				return model.Doc{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				doc.Title = o.Title
			case model.SetBody:
				doc.Body = o.Body
			case model.SetWhen:
				doc.When = o.When
			case model.AddTag:
				tags[o.Tag] = true
			case model.RemoveTag:
				delete(tags, o.Tag)
			case model.AddAnchor:
				anchors[o.Anchor] = true
			case model.RemoveAnchor:
				delete(anchors, o.Anchor)
			case model.DeleteNote:
				doc.Deleted = true
			case model.VerifyNote:
				doc.VerifiedAt = c.AuthorTime
				doc.VerifiedBy = c.Author
				doc.VerifiedCommit = o.VerifiedCommit
				witness = o.Witness
				doc.StaleAt, doc.StaleBy, doc.StaleReason = 0, "", ""
			case model.MarkStale:
				doc.StaleAt, doc.StaleBy, doc.StaleReason = c.AuthorTime, c.Author, o.Reason
			case model.ClearStale:
				doc.StaleAt, doc.StaleBy, doc.StaleReason = 0, "", ""
			case model.AddSupersededBy:
				superseded[o.ID] = true
			case model.RemoveSupersededBy:
				delete(superseded, o.ID)
			case model.AddAttachment:
				attachments[o.Name] = model.Attachment(o)
			case model.RemoveAttachment:
				delete(attachments, o.Name)
			default:
				return model.Doc{}, fmt.Errorf("%w: %s on a doc", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			doc.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Doc{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	doc.Tags = sortedKeys(tags)
	doc.Anchors = sortedAnchors(anchors)
	doc.SupersededBy = sortedKeys(superseded)
	doc.Attachments = sortedAttachments(attachments)
	doc.Witness = witness
	doc.Head = ordered[len(ordered)-1].SHA
	return doc, nil
}

func foldLog(ordered []model.PackCommit) (model.Log, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var log model.Log
	tags := map[string]bool{}
	anchors := map[model.Anchor]bool{}
	attachments := map[string]model.Attachment{}
	var entries []model.LogEntry
	created := false
	if seeded {
		seed, ok := base.State.(model.Log)
		if !ok {
			return model.Log{}, fmt.Errorf("%w: checkpoint over a non-log folded as a log", ErrKindMismatch)
		}
		log = seed
		entries = slices.Clone(seed.Entries)
		for _, t := range seed.Tags {
			tags[t] = true
		}
		for _, a := range seed.Anchors {
			anchors[a] = true
		}
		for _, a := range seed.Attachments {
			attachments[a.Name] = a
		}
		created = true
	} else {
		log = model.Log{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime, Entries: []model.LogEntry{}}
		entries = []model.LogEntry{}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateLog:
					created = true
					log.Title, log.Author = o.Title, c.Author
					for _, t := range o.Tags {
						tags[t] = true
					}
					for _, a := range o.Anchors {
						anchors[a] = true
					}
				case model.CreateNote, model.CreateDoc, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
					return model.Log{}, fmt.Errorf("%w: %s chain folded as a log", ErrKindMismatch, op.OpKind())
				default:
					return model.Log{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
				return model.Log{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				log.Title = o.Title
			case model.AppendEntry:
				entries = append(entries, model.LogEntry{Author: c.Author, TS: c.AuthorTime, Text: o.Text})
			case model.AddTag:
				tags[o.Tag] = true
			case model.RemoveTag:
				delete(tags, o.Tag)
			case model.AddAnchor:
				anchors[o.Anchor] = true
			case model.RemoveAnchor:
				delete(anchors, o.Anchor)
			case model.DeleteNote:
				log.Deleted = true
			case model.AddAttachment:
				attachments[o.Name] = model.Attachment(o)
			case model.RemoveAttachment:
				delete(attachments, o.Name)
			default:
				return model.Log{}, fmt.Errorf("%w: %s on a log", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			log.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Log{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	log.Tags = sortedKeys(tags)
	log.Anchors = sortedAnchors(anchors)
	log.Attachments = sortedAttachments(attachments)
	if entries == nil {
		entries = []model.LogEntry{}
	}
	log.Entries = entries
	log.Head = ordered[len(ordered)-1].SHA
	return log, nil
}

func foldTask(ordered []model.PackCommit) (model.Task, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var task model.Task
	labels := map[string]bool{}
	deps := map[model.EntityID]bool{}
	commits := map[model.SHA]bool{}
	var criteria []model.Criterion
	created := false
	if seeded {
		seed, ok := base.State.(model.Task)
		if !ok {
			return model.Task{}, fmt.Errorf("%w: checkpoint over a note folded as a task", ErrKindMismatch)
		}
		task = seed
		task.Comments = slices.Clone(seed.Comments)
		criteria = slices.Clone(seed.Criteria)
		for _, l := range seed.Labels {
			labels[l] = true
		}
		for _, id := range seed.BlockedBy {
			deps[id] = true
		}
		for _, sha := range seed.Commits {
			commits[sha] = true
		}
		created = true
	} else {
		task = model.Task{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime, Comments: []model.Comment{}}
		criteria = []model.Criterion{}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
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
				case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateSprint, model.CreateProject, model.CreateRunbook:
					return model.Task{}, fmt.Errorf("%w: %s chain folded as a task", ErrKindMismatch, op.OpKind())
				default:
					return model.Task{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
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
			case model.SetBranch:
				task.Branch = o.Branch
			case model.Renew:
				// heartbeat refresh handled uniformly below
			case model.Reclaim:
				if task.Assignee == o.From && task.HeartbeatLamport <= o.AfterLamport {
					task.Assignee = o.Assignee
					task.Status = model.StatusInProgress
					task.ClosedAt = 0
					task.HeartbeatAt = c.AuthorTime
					task.HeartbeatLamport = c.Pack.Lamport
				}
			case model.LinkCommit:
				commits[o.SHA] = true
			case model.UnlinkCommit:
				delete(commits, o.SHA)
			case model.SetSprint:
				task.Sprint = o.Sprint
			case model.SetProject:
				task.Project = o.Project
			case model.AddCriterion:
				if criterionIndex(criteria, o.ID) < 0 {
					criteria = append(criteria, model.Criterion{ID: o.ID, Text: o.Text, Script: o.Script, Status: model.CriterionPending})
				}
			case model.RemoveCriterion:
				if i := criterionIndex(criteria, o.ID); i >= 0 {
					criteria = slices.Delete(criteria, i, i+1)
				}
			case model.SetCriterionText:
				if i := criterionIndex(criteria, o.ID); i >= 0 {
					criteria[i].Text = o.Text
				}
			case model.SetCriterionStatus:
				if i := criterionIndex(criteria, o.ID); i >= 0 {
					criteria[i].Status = o.Status
				}
			case model.SetCriterionScript:
				if i := criterionIndex(criteria, o.ID); i >= 0 {
					criteria[i].Script = o.Script
				}
			default:
				return model.Task{}, fmt.Errorf("%w: %s on a task", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			task.UpdatedAt = c.AuthorTime
			if task.Assignee != "" && c.Author == task.Assignee {
				task.HeartbeatAt = c.AuthorTime
				task.HeartbeatLamport = c.Pack.Lamport
			}
		}
	}
	if !created {
		return model.Task{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	task.Labels = sortedKeys(labels)
	task.BlockedBy = sortedKeys(deps)
	task.Commits = sortedKeys(commits)
	if criteria == nil {
		criteria = []model.Criterion{}
	}
	task.Criteria = criteria
	task.Head = ordered[len(ordered)-1].SHA
	return task, nil
}

// criterionIndex returns the index of the criterion with the given id in the
// slice, or -1 when absent. Criteria lists are tiny, so a linear scan is the
// right tool; there is no stored lamport.
func criterionIndex(criteria []model.Criterion, id string) int {
	for i := range criteria {
		if criteria[i].ID == id {
			return i
		}
	}
	return -1
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

func foldSprint(ordered []model.PackCommit) (model.Sprint, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var sprint model.Sprint
	labels := map[string]bool{}
	commits := map[model.SHA]bool{}
	created := false
	if seeded {
		seed, ok := base.State.(model.Sprint)
		if !ok {
			return model.Sprint{}, fmt.Errorf("%w: checkpoint over a non-sprint folded as a sprint", ErrKindMismatch)
		}
		sprint = seed
		sprint.Comments = slices.Clone(seed.Comments)
		for _, l := range seed.Labels {
			labels[l] = true
		}
		for _, sha := range seed.Commits {
			commits[sha] = true
		}
		created = true
	} else {
		sprint = model.Sprint{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime, Comments: []model.Comment{}}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateSprint:
					created = true
					sprint.Title, sprint.Description = o.Title, o.Description
					sprint.Project, sprint.Author = o.Project, c.Author
					sprint.Status = model.SprintPlanned
					for _, l := range o.Labels {
						labels[l] = true
					}
				case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateProject, model.CreateRunbook:
					return model.Sprint{}, fmt.Errorf("%w: %s chain folded as a sprint", ErrKindMismatch, op.OpKind())
				default:
					return model.Sprint{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
				return model.Sprint{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				sprint.Title = o.Title
			case model.SetDescription:
				sprint.Description = o.Description
			case model.SetProject:
				sprint.Project = o.Project
			case model.SetSprintStatus:
				applySprintStatus(&sprint, o.Status, c.AuthorTime)
			case model.SetStartDate:
				sprint.StartDate = o.Date
			case model.SetEndDate:
				sprint.EndDate = o.Date
			case model.AddLabel:
				labels[o.Label] = true
			case model.RemoveLabel:
				delete(labels, o.Label)
			case model.AddComment:
				sprint.Comments = append(sprint.Comments, model.Comment{Author: c.Author, TS: c.AuthorTime, Body: o.Body})
			case model.LinkCommit:
				commits[o.SHA] = true
			case model.UnlinkCommit:
				delete(commits, o.SHA)
			default:
				return model.Sprint{}, fmt.Errorf("%w: %s on a sprint", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			sprint.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Sprint{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	sprint.Labels = sortedKeys(labels)
	sprint.Commits = sortedKeys(commits)
	sprint.Head = ordered[len(ordered)-1].SHA
	return sprint, nil
}

func applySprintStatus(s *model.Sprint, status model.SprintStatus, at int64) {
	if status == model.SprintActive && s.Status != model.SprintActive {
		s.StartedAt = at
	}
	s.Status = status
	switch status {
	case model.SprintCompleted, model.SprintCancelled:
		s.ClosedAt = at
	case model.SprintPlanned, model.SprintActive:
		s.ClosedAt = 0
	}
}

func foldProject(ordered []model.PackCommit) (model.Project, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var project model.Project
	labels := map[string]bool{}
	commits := map[model.SHA]bool{}
	created := false
	if seeded {
		seed, ok := base.State.(model.Project)
		if !ok {
			return model.Project{}, fmt.Errorf("%w: checkpoint over a non-project folded as a project", ErrKindMismatch)
		}
		project = seed
		project.Comments = slices.Clone(seed.Comments)
		for _, l := range seed.Labels {
			labels[l] = true
		}
		for _, sha := range seed.Commits {
			commits[sha] = true
		}
		created = true
	} else {
		project = model.Project{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime, Comments: []model.Comment{}}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateProject:
					created = true
					project.Title, project.Description = o.Title, o.Description
					project.Author = c.Author
					project.Status = model.ProjectActive
					for _, l := range o.Labels {
						labels[l] = true
					}
				case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateRunbook:
					return model.Project{}, fmt.Errorf("%w: %s chain folded as a project", ErrKindMismatch, op.OpKind())
				default:
					return model.Project{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
				return model.Project{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				project.Title = o.Title
			case model.SetDescription:
				project.Description = o.Description
			case model.SetProjectStatus:
				applyProjectStatus(&project, o.Status, c.AuthorTime)
			case model.AddLabel:
				labels[o.Label] = true
			case model.RemoveLabel:
				delete(labels, o.Label)
			case model.AddComment:
				project.Comments = append(project.Comments, model.Comment{Author: c.Author, TS: c.AuthorTime, Body: o.Body})
			case model.LinkCommit:
				commits[o.SHA] = true
			case model.UnlinkCommit:
				delete(commits, o.SHA)
			default:
				return model.Project{}, fmt.Errorf("%w: %s on a project", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			project.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Project{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	project.Labels = sortedKeys(labels)
	project.Commits = sortedKeys(commits)
	project.Head = ordered[len(ordered)-1].SHA
	return project, nil
}

func applyProjectStatus(p *model.Project, status model.ProjectStatus, at int64) {
	p.Status = status
	switch status {
	case model.ProjectCompleted, model.ProjectArchived, model.ProjectCancelled:
		p.ClosedAt = at
	case model.ProjectActive:
		p.ClosedAt = 0
	}
}

func foldRunbook(ordered []model.PackCommit) (model.Runbook, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var rb model.Runbook
	labels := map[string]bool{}
	var steps []model.RunbookStep
	var runs []model.RunbookRun
	created := false
	if seeded {
		seed, ok := base.State.(model.Runbook)
		if !ok {
			return model.Runbook{}, fmt.Errorf("%w: checkpoint over a non-runbook folded as a runbook", ErrKindMismatch)
		}
		rb = seed
		rb.Comments = slices.Clone(seed.Comments)
		steps = slices.Clone(seed.Steps)
		runs = cloneRuns(seed.Runs)
		for _, l := range seed.Labels {
			labels[l] = true
		}
		created = true
	} else {
		rb = model.Runbook{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime, Comments: []model.Comment{}}
		steps = []model.RunbookStep{}
		runs = []model.RunbookRun{}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateRunbook:
					created = true
					rb.Title, rb.Description = o.Title, o.Description
					rb.Author = c.Author
					rb.Status = model.RunbookActive
					for _, l := range o.Labels {
						labels[l] = true
					}
				case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject:
					return model.Runbook{}, fmt.Errorf("%w: %s chain folded as a runbook", ErrKindMismatch, op.OpKind())
				default:
					return model.Runbook{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateDoc, model.CreateLog, model.CreateTask, model.CreateSprint, model.CreateProject, model.CreateRunbook:
				return model.Runbook{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				rb.Title = o.Title
			case model.SetDescription:
				rb.Description = o.Description
			case model.SetRunbookStatus:
				applyRunbookStatus(&rb, o.Status, c.AuthorTime)
			case model.AddLabel:
				labels[o.Label] = true
			case model.RemoveLabel:
				delete(labels, o.Label)
			case model.AddComment:
				rb.Comments = append(rb.Comments, model.Comment{Author: c.Author, TS: c.AuthorTime, Body: o.Body})
			case model.AddStep:
				if stepIndex(steps, o.ID) < 0 {
					steps = append(steps, model.RunbookStep(o))
				}
			case model.RemoveStep:
				if i := stepIndex(steps, o.ID); i >= 0 {
					steps = slices.Delete(steps, i, i+1)
				}
			case model.SetStepText:
				if i := stepIndex(steps, o.ID); i >= 0 {
					steps[i].Text = o.Text
				}
			case model.SetStepCommand:
				if i := stepIndex(steps, o.ID); i >= 0 {
					steps[i].Command = o.Command
				}
			case model.SetStepPosition:
				if i := stepIndex(steps, o.ID); i >= 0 {
					steps[i].Position = o.Position
				}
			case model.StartRun:
				if runIndex(runs, o.ID) < 0 {
					runs = append(runs, model.RunbookRun{
						ID:        o.ID,
						Task:      o.Task,
						Status:    model.RunRunning,
						Runner:    c.Author,
						StartedAt: c.AuthorTime,
						Results:   []model.RunbookStepResult{},
					})
				}
			case model.SetRunStepStatus:
				if i := runIndex(runs, o.RunID); i >= 0 {
					result := model.RunbookStepResult{StepID: o.StepID, Status: o.Status, Note: o.Note, Actor: c.Author, TS: c.AuthorTime}
					if j := resultIndex(runs[i].Results, o.StepID); j >= 0 {
						runs[i].Results[j] = result
					} else {
						runs[i].Results = append(runs[i].Results, result)
					}
				}
			case model.FinishRun:
				if i := runIndex(runs, o.ID); i >= 0 {
					runs[i].Status = o.Status
					runs[i].FinishedAt = c.AuthorTime
				}
			default:
				return model.Runbook{}, fmt.Errorf("%w: %s on a runbook", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			rb.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Runbook{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	rb.Labels = sortedKeys(labels)
	slices.SortFunc(steps, func(a, b model.RunbookStep) int {
		if c := cmp.Compare(a.Position, b.Position); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	rb.Steps = steps
	rb.Runs = runs
	rb.Head = ordered[len(ordered)-1].SHA
	return rb, nil
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

func firstOp(ordered []model.PackCommit) model.Op {
	for _, c := range ordered {
		if len(c.Pack.Ops) > 0 {
			return c.Pack.Ops[0]
		}
	}
	return nil
}

// selectSeed chooses the checkpoint a fold may start its snapshot from, plus
// the set of commits its State already covers. It returns the checkpoint with
// the greatest CoversLamport (sha tiebreak) only when that checkpoint is
// seed-safe: every commit in ordered that is neither the checkpoint commit nor
// in its CoversShas has Pack.Lamport > CoversLamport. That gate is the
// load-bearing convergence guarantee — because Linearize orders by lamport
// first and every covered op has lamport <= CoversLamport, a strictly-greater
// uncovered lamport means every uncovered op sorts after every covered op, so
// seeding from State and replaying only the uncovered suffix yields exactly the
// full-history fold. When the gate fails (concurrent checkpoints over different
// frontiers), it returns ok=false and the caller folds the full chain, where
// every checkpoint is a no-op and the covered ops replay through their original
// commits. The fallback is always available: compaction never deletes objects,
// so covered commits remain in the chain.
func selectSeed(ordered []model.PackCommit) (base model.Checkpoint, seedSHA model.SHA, covered map[model.SHA]bool, ok bool) {
	found := false
	for _, c := range ordered {
		cp, isCheckpoint := checkpointOf(c)
		if !isCheckpoint {
			continue
		}
		if !found || cp.CoversLamport > base.CoversLamport ||
			(cp.CoversLamport == base.CoversLamport && c.SHA > seedSHA) {
			base, seedSHA, found = cp, c.SHA, true
		}
	}
	if !found {
		return model.Checkpoint{}, "", nil, false
	}
	covered = make(map[model.SHA]bool, len(base.CoversShas))
	for _, s := range base.CoversShas {
		covered[s] = true
	}
	for _, c := range ordered {
		if c.SHA == seedSHA || covered[c.SHA] {
			continue
		}
		if c.Pack.Lamport <= base.CoversLamport {
			return model.Checkpoint{}, "", nil, false
		}
	}
	return base, seedSHA, covered, true
}

// checkpointOf returns the Checkpoint a checkpoint commit carries. A checkpoint
// commit holds exactly one op, the Checkpoint.
func checkpointOf(c model.PackCommit) (model.Checkpoint, bool) {
	if len(c.Pack.Ops) == 1 {
		if cp, ok := c.Pack.Ops[0].(model.Checkpoint); ok {
			return cp, true
		}
	}
	return model.Checkpoint{}, false
}

// hasNonCheckpointOp reports whether c carries any op that is not a Checkpoint.
// A checkpoint commit's compaction time and author are not the entity's, so it
// must not stamp UpdatedAt or the lease heartbeat — only a commit with a real
// op does.
func hasNonCheckpointOp(c model.PackCommit) bool {
	for _, op := range c.Pack.Ops {
		if _, ok := op.(model.Checkpoint); !ok {
			return true
		}
	}
	return false
}

func sortedKeys[K cmp.Ordered](set map[K]bool) []K {
	keys := make([]K, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// sortedAttachments returns the folded attachment set ordered by Name, or nil
// when empty: Attachments marshals omitempty, so a nil result keeps
// attachment-less snapshot bytes identical to their pre-attachment form and
// keeps a fresh fold DeepEqual to its own cache and checkpoint round-trips.
func sortedAttachments(set map[string]model.Attachment) []model.Attachment {
	if len(set) == 0 {
		return nil
	}
	attachments := make([]model.Attachment, 0, len(set))
	for _, a := range set {
		attachments = append(attachments, a)
	}
	slices.SortFunc(attachments, func(a, b model.Attachment) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return attachments
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
