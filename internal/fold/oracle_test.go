package fold

// This file is the differential oracle gating the fold-engine refactor: it
// carries the seven pre-refactor per-kind interpreters verbatim (oracleFold*)
// and asserts, over hand-built vectors and seeded random chains, that the
// generic engine (Fold/History) folds every chain to a byte-identical snapshot
// and byte-identical error text. It is self-contained and slated for deletion
// after a soak period. The oracle reuses the package's pure, unchanged helpers
// (selectSeed, sortedKeys, criterionIndex, applyStatus, cloneRuns, ...) so the
// only variable under test is the interpreter structure.

import (
	"cmp"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// --- verbatim pre-refactor interpreters ---

func oracleFold(commits []model.PackCommit) (model.Snapshot, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return nil, err
	}
	switch first := firstOp(ordered).(type) {
	case model.CreateNote:
		return oracleFoldNote(ordered)
	case model.CreateDoc:
		return oracleFoldDoc(ordered)
	case model.CreateLog:
		return oracleFoldLog(ordered)
	case model.CreateTask:
		return oracleFoldTask(ordered)
	case model.CreateSprint:
		return oracleFoldSprint(ordered)
	case model.CreateProject:
		return oracleFoldProject(ordered)
	case model.CreateRunbook:
		return oracleFoldRunbook(ordered)
	case nil:
		return nil, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	default:
		return nil, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
}

func oracleDispatch(first model.Op) (func([]model.PackCommit) (model.Snapshot, error), error) {
	switch first.(type) {
	case model.CreateNote:
		return func(o []model.PackCommit) (model.Snapshot, error) { return oracleFoldNote(o) }, nil
	case model.CreateDoc:
		return func(o []model.PackCommit) (model.Snapshot, error) { return oracleFoldDoc(o) }, nil
	case model.CreateLog:
		return func(o []model.PackCommit) (model.Snapshot, error) { return oracleFoldLog(o) }, nil
	case model.CreateTask:
		return func(o []model.PackCommit) (model.Snapshot, error) { return oracleFoldTask(o) }, nil
	case model.CreateSprint:
		return func(o []model.PackCommit) (model.Snapshot, error) { return oracleFoldSprint(o) }, nil
	case model.CreateProject:
		return func(o []model.PackCommit) (model.Snapshot, error) { return oracleFoldProject(o) }, nil
	case model.CreateRunbook:
		return func(o []model.PackCommit) (model.Snapshot, error) { return oracleFoldRunbook(o) }, nil
	case nil:
		return nil, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	default:
		return nil, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
}

func oracleHistory(commits []model.PackCommit) ([]Step, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return nil, err
	}
	foldPrefix, err := oracleDispatch(firstOp(ordered))
	if err != nil {
		return nil, err
	}
	steps := make([]Step, len(ordered))
	for k := range ordered {
		snapshot, err := foldPrefix(ordered[:k+1])
		if err != nil {
			return nil, err
		}
		steps[k] = Step{Commit: ordered[k], Snapshot: snapshot}
	}
	return steps, nil
}

func oracleFoldNote(ordered []model.PackCommit) (model.Note, error) {
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

func oracleFoldDoc(ordered []model.PackCommit) (model.Doc, error) {
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

func oracleFoldLog(ordered []model.PackCommit) (model.Log, error) {
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

func oracleFoldTask(ordered []model.PackCommit) (model.Task, error) {
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

func oracleFoldSprint(ordered []model.PackCommit) (model.Sprint, error) {
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

func oracleFoldProject(ordered []model.PackCommit) (model.Project, error) {
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

func oracleFoldRunbook(ordered []model.PackCommit) (model.Runbook, error) {
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

// --- differential harness ---

func errText(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

var oracleSentinels = []error{
	ErrNoCreate, ErrDuplicateCreate, ErrKindMismatch,
	ErrEmptyChain, ErrMissingParent, ErrMultipleRoots, ErrMultipleHeads, ErrCorruptChain,
}

// diffChain asserts the engine (Fold/History) and the verbatim oracle agree on
// c: identical error text, identical wrapped sentinels, and DeepEqual snapshots
// and history steps.
func diffChain(t *testing.T, name string, c []model.PackCommit) {
	t.Helper()
	oldSnap, oldErr := oracleFold(c)
	newSnap, newErr := Fold(c)
	if errText(oldErr) != errText(newErr) {
		t.Fatalf("%s: Fold error text\n old=%q\n new=%q", name, errText(oldErr), errText(newErr))
	}
	for _, s := range oracleSentinels {
		if errors.Is(oldErr, s) != errors.Is(newErr, s) {
			t.Fatalf("%s: Fold errors.Is(%v) disagree: old=%v new=%v", name, s, oldErr, newErr)
		}
	}
	if oldErr == nil && !reflect.DeepEqual(oldSnap, newSnap) {
		t.Fatalf("%s: Fold snapshot mismatch\n old=%+v\n new=%+v", name, oldSnap, newSnap)
	}

	oldSteps, oldHErr := oracleHistory(c)
	newSteps, newHErr := History(c)
	if errText(oldHErr) != errText(newHErr) {
		t.Fatalf("%s: History error text\n old=%q\n new=%q", name, errText(oldHErr), errText(newHErr))
	}
	for _, s := range oracleSentinels {
		if errors.Is(oldHErr, s) != errors.Is(newHErr, s) {
			t.Fatalf("%s: History errors.Is(%v) disagree: old=%v new=%v", name, s, oldHErr, newHErr)
		}
	}
	if oldHErr == nil && !reflect.DeepEqual(oldSteps, newSteps) {
		t.Fatalf("%s: History steps mismatch\n old=%+v\n new=%+v", name, oldSteps, newSteps)
	}
}

// ocommit builds a decoded commit for the oracle vectors; it mirrors the
// external test helper mk but lives in package fold.
func ocommit(sha string, parents []string, author string, at int64, lamport uint64, ops ...model.Op) model.PackCommit {
	ps := make([]model.SHA, len(parents))
	for i, p := range parents {
		ps[i] = model.SHA(p)
	}
	return model.PackCommit{
		SHA:        model.SHA(sha),
		Parents:    ps,
		Author:     model.Actor(author),
		AuthorTime: at,
		Pack:       model.Pack{Lamport: model.Lamport(lamport), Ops: ops},
	}
}

// opFactory produces one op with pseudo-random fields drawn from small,
// colliding pools so add/remove and id-keyed ops (criteria, steps, runs)
// actually interact across a chain.
type opFactory func(r *rand.Rand) model.Op

// kindGen is one entity kind's create op plus the full vocabulary of ops its
// fold accepts. The factory slice covers every apply-switch case exactly once,
// which is what the coverage assertion pins.
type kindGen struct {
	kind   model.Kind
	create func(nonce string) model.CreateOp
	ops    []opFactory
}

var (
	pool3    = []string{"a", "b", "c"}
	poolIDs  = []model.EntityID{"e1", "e2"}
	poolSHAs = []model.SHA{"s1", "s2"}
	poolAnch = []model.Anchor{
		{Kind: model.AnchorPath, Value: "x.go"},
		{Kind: model.AnchorCommit, Value: "beef"},
		{Kind: model.AnchorDir, Value: "pkg"},
	}
	poolKeyIDs   = []string{"i1", "i2"}
	poolActors   = []model.Actor{"alice", "bob"}
	poolStatus   = []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled}
	poolCrit     = []model.CriterionStatus{model.CriterionPending, model.CriterionMet, model.CriterionFailed}
	poolSprintSt = []model.SprintStatus{model.SprintPlanned, model.SprintActive, model.SprintCompleted, model.SprintCancelled}
	poolProjSt   = []model.ProjectStatus{model.ProjectActive, model.ProjectCompleted, model.ProjectArchived, model.ProjectCancelled}
	poolRunbSt   = []model.RunbookStatus{model.RunbookActive, model.RunbookArchived}
	poolRunSt    = []model.RunStatus{model.RunSucceeded, model.RunFailed, model.RunAbandoned}
	poolStepSt   = []model.StepResultStatus{model.StepDone, model.StepFailed, model.StepSkipped}
	poolType     = []model.TaskType{model.TypeTask, model.TypeBug, model.TypeEpic, model.TypeQuestion}
	poolPrio     = []model.Priority{0, 1, 2, 3}
)

func pick[T any](r *rand.Rand, s []T) T { return s[r.IntN(len(s))] }

func noteOps() []opFactory {
	return []opFactory{
		func(r *rand.Rand) model.Op { return model.SetTitle{Title: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetBody{Body: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddTag{Tag: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.RemoveTag{Tag: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddAnchor{Anchor: pick(r, poolAnch)} },
		func(r *rand.Rand) model.Op { return model.RemoveAnchor{Anchor: pick(r, poolAnch)} },
		func(_ *rand.Rand) model.Op { return model.DeleteNote{} },
		func(r *rand.Rand) model.Op {
			return model.VerifyNote{
				Witness:        []model.AnchorWitness{{Anchor: pick(r, poolAnch), OID: pick(r, poolSHAs)}},
				VerifiedCommit: pick(r, poolSHAs),
			}
		},
		func(r *rand.Rand) model.Op { return model.MarkStale{Reason: pick(r, pool3)} },
		func(_ *rand.Rand) model.Op { return model.ClearStale{} },
		func(r *rand.Rand) model.Op { return model.AddSupersededBy{ID: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op { return model.RemoveSupersededBy{ID: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op {
			return model.AddAttachment{Name: pick(r, pool3), OID: pick(r, pool3), Size: int64(r.IntN(9))}
		},
		func(r *rand.Rand) model.Op { return model.RemoveAttachment{Name: pick(r, pool3)} },
	}
}

func docOps() []opFactory {
	ops := noteOps()
	ops = append(ops, func(r *rand.Rand) model.Op { return model.SetWhen{When: pick(r, pool3)} })
	return ops
}

func logOps() []opFactory {
	return []opFactory{
		func(r *rand.Rand) model.Op { return model.SetTitle{Title: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AppendEntry{Text: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddTag{Tag: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.RemoveTag{Tag: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddAnchor{Anchor: pick(r, poolAnch)} },
		func(r *rand.Rand) model.Op { return model.RemoveAnchor{Anchor: pick(r, poolAnch)} },
		func(_ *rand.Rand) model.Op { return model.DeleteNote{} },
		func(r *rand.Rand) model.Op {
			return model.AddAttachment{Name: pick(r, pool3), OID: pick(r, pool3), Size: int64(r.IntN(9))}
		},
		func(r *rand.Rand) model.Op { return model.RemoveAttachment{Name: pick(r, pool3)} },
	}
}

func taskOps() []opFactory {
	return []opFactory{
		func(r *rand.Rand) model.Op { return model.SetTitle{Title: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetDescription{Description: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetType{Type: pick(r, poolType)} },
		func(r *rand.Rand) model.Op { return model.SetPriority{Priority: pick(r, poolPrio)} },
		func(r *rand.Rand) model.Op { return model.SetStatus{Status: pick(r, poolStatus)} },
		func(r *rand.Rand) model.Op { return model.SetAssignee{Assignee: pick(r, poolActors)} },
		func(r *rand.Rand) model.Op { return model.Claim{Assignee: pick(r, poolActors)} },
		func(r *rand.Rand) model.Op { return model.AddLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.RemoveLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddDep{ID: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op { return model.RemoveDep{ID: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op { return model.SetParent{Parent: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op { return model.AddComment{Body: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetBranch{Branch: model.Branch(pick(r, pool3))} },
		func(_ *rand.Rand) model.Op { return model.Renew{} },
		func(r *rand.Rand) model.Op {
			return model.Reclaim{Assignee: pick(r, poolActors), From: pick(r, poolActors), AfterLamport: model.Lamport(r.IntN(6))}
		},
		func(r *rand.Rand) model.Op { return model.LinkCommit{SHA: pick(r, poolSHAs)} },
		func(r *rand.Rand) model.Op { return model.UnlinkCommit{SHA: pick(r, poolSHAs)} },
		func(r *rand.Rand) model.Op { return model.SetSprint{Sprint: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op { return model.SetProject{Project: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op {
			return model.AddCriterion{ID: pick(r, poolKeyIDs), Text: pick(r, pool3), Script: pick(r, pool3)}
		},
		func(r *rand.Rand) model.Op { return model.RemoveCriterion{ID: pick(r, poolKeyIDs)} },
		func(r *rand.Rand) model.Op {
			return model.SetCriterionText{ID: pick(r, poolKeyIDs), Text: pick(r, pool3)}
		},
		func(r *rand.Rand) model.Op {
			return model.SetCriterionStatus{ID: pick(r, poolKeyIDs), Status: pick(r, poolCrit)}
		},
		func(r *rand.Rand) model.Op {
			return model.SetCriterionScript{ID: pick(r, poolKeyIDs), Script: pick(r, pool3)}
		},
	}
}

func sprintOps() []opFactory {
	return []opFactory{
		func(r *rand.Rand) model.Op { return model.SetTitle{Title: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetDescription{Description: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetProject{Project: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op { return model.SetSprintStatus{Status: pick(r, poolSprintSt)} },
		func(r *rand.Rand) model.Op { return model.SetStartDate{Date: int64(r.IntN(1000))} },
		func(r *rand.Rand) model.Op { return model.SetEndDate{Date: int64(r.IntN(1000))} },
		func(r *rand.Rand) model.Op { return model.AddLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.RemoveLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddComment{Body: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.LinkCommit{SHA: pick(r, poolSHAs)} },
		func(r *rand.Rand) model.Op { return model.UnlinkCommit{SHA: pick(r, poolSHAs)} },
	}
}

func projectOps() []opFactory {
	return []opFactory{
		func(r *rand.Rand) model.Op { return model.SetTitle{Title: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetDescription{Description: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetProjectStatus{Status: pick(r, poolProjSt)} },
		func(r *rand.Rand) model.Op { return model.AddLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.RemoveLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddComment{Body: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.LinkCommit{SHA: pick(r, poolSHAs)} },
		func(r *rand.Rand) model.Op { return model.UnlinkCommit{SHA: pick(r, poolSHAs)} },
	}
}

func runbookOps() []opFactory {
	return []opFactory{
		func(r *rand.Rand) model.Op { return model.SetTitle{Title: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetDescription{Description: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.SetRunbookStatus{Status: pick(r, poolRunbSt)} },
		func(r *rand.Rand) model.Op { return model.AddLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.RemoveLabel{Label: pick(r, pool3)} },
		func(r *rand.Rand) model.Op { return model.AddComment{Body: pick(r, pool3)} },
		func(r *rand.Rand) model.Op {
			return model.AddStep{ID: pick(r, poolKeyIDs), Text: pick(r, pool3), Command: pick(r, pool3), Position: pick(r, pool3)}
		},
		func(r *rand.Rand) model.Op { return model.RemoveStep{ID: pick(r, poolKeyIDs)} },
		func(r *rand.Rand) model.Op { return model.SetStepText{ID: pick(r, poolKeyIDs), Text: pick(r, pool3)} },
		func(r *rand.Rand) model.Op {
			return model.SetStepCommand{ID: pick(r, poolKeyIDs), Command: pick(r, pool3)}
		},
		func(r *rand.Rand) model.Op {
			return model.SetStepPosition{ID: pick(r, poolKeyIDs), Position: pick(r, pool3)}
		},
		func(r *rand.Rand) model.Op { return model.StartRun{ID: pick(r, poolKeyIDs), Task: pick(r, poolIDs)} },
		func(r *rand.Rand) model.Op {
			return model.SetRunStepStatus{RunID: pick(r, poolKeyIDs), StepID: pick(r, poolKeyIDs), Status: pick(r, poolStepSt), Note: pick(r, pool3)}
		},
		func(r *rand.Rand) model.Op {
			return model.FinishRun{ID: pick(r, poolKeyIDs), Status: pick(r, poolRunSt)}
		},
	}
}

func kindGens() []kindGen {
	return []kindGen{
		{model.KindNote, func(n string) model.CreateOp {
			return model.CreateNote{Nonce: n, Title: "t", Body: "b", Tags: []string{"a"}, Anchors: []model.Anchor{poolAnch[0]}}
		}, noteOps()},
		{model.KindDoc, func(n string) model.CreateOp {
			return model.CreateDoc{Nonce: n, Title: "t", Body: "b", When: "w", Tags: []string{"a"}, Anchors: []model.Anchor{poolAnch[0]}}
		}, docOps()},
		{model.KindLog, func(n string) model.CreateOp {
			return model.CreateLog{Nonce: n, Title: "t", Tags: []string{"a"}, Anchors: []model.Anchor{poolAnch[0]}}
		}, logOps()},
		{model.KindTask, func(n string) model.CreateOp {
			return model.CreateTask{Nonce: n, Title: "t", Type: model.TypeTask, Branch: "main", Labels: []string{"a"}}
		}, taskOps()},
		{model.KindSprint, func(n string) model.CreateOp {
			return model.CreateSprint{Nonce: n, Title: "t", Labels: []string{"a"}}
		}, sprintOps()},
		{model.KindProject, func(n string) model.CreateOp {
			return model.CreateProject{Nonce: n, Title: "t", Labels: []string{"a"}}
		}, projectOps()},
		{model.KindRunbook, func(n string) model.CreateOp {
			return model.CreateRunbook{Nonce: n, Title: "t", Labels: []string{"a"}}
		}, runbookOps()},
	}
}

// randOps returns a commit's op pack: usually 1-4 random ops from g's
// vocabulary, occasionally empty. Multi-op packs exercise the per-commit (not
// per-op) touch and heartbeat gating the engine restructured; empty packs
// exercise the no-touch path. seen records every op kind emitted.
func randOps(g kindGen, r *rand.Rand, seen map[string]bool) []model.Op {
	if r.IntN(6) == 0 {
		return nil
	}
	ops := make([]model.Op, 1+r.IntN(4))
	for k := range ops {
		op := g.ops[r.IntN(len(g.ops))](r)
		seen[op.OpKind()] = true
		ops[k] = op
	}
	return ops
}

// randomChain builds a linear chain: a create commit (whose pack sometimes
// carries extra ops, as a real create pack can), then nCommits commits each
// carrying a 0-4 op pack.
func randomChain(g kindGen, r *rand.Rand, nCommits int, seen map[string]bool) []model.PackCommit {
	createOps := make([]model.Op, 0, 3)
	createOps = append(createOps, g.create("n0"))
	for range r.IntN(3) {
		op := g.ops[r.IntN(len(g.ops))](r)
		seen[op.OpKind()] = true
		createOps = append(createOps, op)
	}
	chain := []model.PackCommit{
		ocommit("c0", nil, string(pick(r, poolActors)), 100, 1, createOps...),
	}
	for i := 1; i <= nCommits; i++ {
		chain = append(chain, ocommit(
			fmt.Sprintf("c%d", i),
			[]string{string(chain[len(chain)-1].SHA)},
			string(pick(r, poolActors)),
			int64(100+100*i), uint64(i+1), randOps(g, r, seen)...,
		))
	}
	return chain
}

// exhaustiveChain applies every op in g's vocabulary once, in order, so a single
// chain covers the whole apply switch.
func exhaustiveChain(g kindGen, seen map[string]bool) []model.PackCommit {
	chain := make([]model.PackCommit, 0, 1+len(g.ops))
	chain = append(chain, ocommit("c0", nil, "alice", 100, 1, g.create("n0")))
	r := rand.New(rand.NewPCG(7, 7))
	for i, f := range g.ops {
		op := f(r)
		seen[op.OpKind()] = true
		chain = append(chain, ocommit(
			fmt.Sprintf("c%d", i+1),
			[]string{string(chain[len(chain)-1].SHA)},
			string(pick(r, poolActors)),
			int64(100+100*(i+1)), uint64(i+2), op,
		))
	}
	return chain
}

// withCheckpoint splices a seed-safe checkpoint into chain after position cut,
// covering commits 0..cut and reparenting the suffix onto the checkpoint. When
// cut is the last index the checkpoint becomes the new tip with no suffix. The
// checkpoint State is folded by the oracle, so it is an independent reference.
func withCheckpoint(chain []model.PackCommit, cut int) ([]model.PackCommit, error) {
	if cut < 0 || cut > len(chain)-1 {
		return chain, nil
	}
	prefix := chain[:cut+1]
	state, err := oracleFold(prefix)
	if err != nil {
		return nil, err
	}
	covers := make([]model.SHA, len(prefix))
	for i, c := range prefix {
		covers[i] = c.SHA
	}
	coversLamport := prefix[len(prefix)-1].Pack.Lamport
	cp := model.PackCommit{
		SHA:        "cK",
		Parents:    []model.SHA{chain[cut].SHA},
		Author:     "compactor",
		AuthorTime: chain[cut].AuthorTime + 1,
		Pack: model.Pack{
			Lamport: coversLamport + 1,
			Ops:     []model.Op{model.Checkpoint{EntityID: state.EntityID(), State: state, CoversLamport: coversLamport, CoversShas: covers}},
		},
	}
	out := make([]model.PackCommit, 0, len(chain)+1)
	out = append(out, prefix...)
	out = append(out, cp)
	for i := cut + 1; i < len(chain); i++ {
		c := chain[i]
		if i == cut+1 {
			c.Parents = []model.SHA{"cK"}
		}
		c.Pack.Lamport = coversLamport + 1 + model.Lamport(i-cut)
		out = append(out, c)
	}
	return out, nil
}

// twoCheckpointChain builds a chain carrying two checkpoints. When nested, the
// newer checkpoint (cp2) covers the older (cp1), so cp2 is seed-safe and
// selectSeed seeds from it. When not nested, cp1 is left uncovered with a lower
// lamport than cp2's coverage, so cp2 fails the seed-safety gate and the whole
// chain folds with every checkpoint a no-op. Both variants exercise the engine's
// multi-checkpoint handling — the branch single-checkpoint chains never reach.
func twoCheckpointChain(g kindGen, r *rand.Rand, seen map[string]bool, nested bool) ([]model.PackCommit, error) {
	c0 := ocommit("c0", nil, string(pick(r, poolActors)), 100, 1, g.create("n0"))
	c1 := ocommit("c1", []string{"c0"}, string(pick(r, poolActors)), 200, 2, randOps(g, r, seen)...)
	c2 := ocommit("c2", []string{"c1"}, string(pick(r, poolActors)), 300, 3, randOps(g, r, seen)...)
	state1, err := oracleFold([]model.PackCommit{c0, c1, c2})
	if err != nil {
		return nil, err
	}
	cp1 := ocommit("cp1", []string{"c2"}, "compactor", 350, 4,
		model.Checkpoint{EntityID: state1.EntityID(), State: state1, CoversLamport: 3, CoversShas: []model.SHA{"c0", "c1", "c2"}})
	c3 := ocommit("c3", []string{"cp1"}, string(pick(r, poolActors)), 400, 5, randOps(g, r, seen)...)
	c4 := ocommit("c4", []string{"c3"}, string(pick(r, poolActors)), 500, 6, randOps(g, r, seen)...)
	state2, err := oracleFold([]model.PackCommit{c0, c1, c2, cp1, c3, c4})
	if err != nil {
		return nil, err
	}
	covers2 := []model.SHA{"c0", "c1", "c2", "c3", "c4"}
	if nested {
		covers2 = append(covers2, "cp1")
	}
	cp2 := ocommit("cp2", []string{"c4"}, "compactor", 550, 7,
		model.Checkpoint{EntityID: state2.EntityID(), State: state2, CoversLamport: 6, CoversShas: covers2})
	c5 := ocommit("c5", []string{"cp2"}, string(pick(r, poolActors)), 600, 8, randOps(g, r, seen)...)
	return []model.PackCommit{c0, c1, c2, cp1, c3, c4, cp2, c5}, nil
}

func shuffledCommits(chain []model.PackCommit, r *rand.Rand) []model.PackCommit {
	out := slices.Clone(chain)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// assertSeed pins which selectSeed branch a checkpoint chain exercises, so a
// construction that silently stops seeding (and thus no longer tests the seeded
// replay path) fails loudly instead of passing a trivially-equal differential.
func assertSeed(t *testing.T, name string, chain []model.PackCommit, wantOK bool, wantSeed model.SHA) {
	t.Helper()
	ordered, err := Linearize(chain)
	if err != nil {
		t.Fatalf("%s: linearize: %v", name, err)
	}
	_, seedSHA, _, ok := selectSeed(ordered)
	if ok != wantOK {
		t.Fatalf("%s: selectSeed ok=%v, want %v", name, ok, wantOK)
	}
	if ok && seedSHA != wantSeed {
		t.Fatalf("%s: selectSeed seed=%q, want %q", name, seedSHA, wantSeed)
	}
}

// TestOracleDifferential drives the engine and the verbatim pre-refactor
// interpreters over the same chains and asserts byte-identical snapshots,
// history trails, and error text. It covers, per kind: an exhaustive chain
// hitting every op; many random chains whose commits carry multi-op and empty
// packs (exercising per-commit touch/heartbeat gating); a mid-chain checkpoint,
// a checkpoint as the final commit, and two-checkpoint chains (newest seeds vs
// newest fails the seed-safety gate); shuffled (re-linearized) inputs; and the
// error paths (duplicate create, foreign op, seed-kind mismatch). assertSeed
// pins that each checkpoint variant exercises the selectSeed branch it targets.
func TestOracleDifferential(t *testing.T) {
	//nolint:gosec // G404: deterministic PRNG for a reproducible differential fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(0x5eed, 0x1dea))
	const chainsPerKind = 80
	seen := map[model.Kind]map[string]bool{}
	for _, g := range kindGens() {
		seen[g.kind] = map[string]bool{}
		s := seen[g.kind]

		diffChain(t, string(g.kind)+"/exhaustive", exhaustiveChain(g, s))

		for i := range chainsPerKind {
			chain := randomChain(g, r, 6+r.IntN(10), s)
			diffChain(t, fmt.Sprintf("%s/rand%d", g.kind, i), chain)

			if len(chain) > 2 {
				cut := 1 + r.IntN(len(chain)-2)
				cpChain, err := withCheckpoint(chain, cut)
				if err != nil {
					t.Fatalf("%s/rand%d checkpoint build: %v", g.kind, i, err)
				}
				assertSeed(t, fmt.Sprintf("%s/rand%d/cp", g.kind, i), cpChain, true, "cK")
				diffChain(t, fmt.Sprintf("%s/rand%d/cp", g.kind, i), cpChain)

				finalCP, err := withCheckpoint(chain, len(chain)-1)
				if err != nil {
					t.Fatalf("%s/rand%d final checkpoint build: %v", g.kind, i, err)
				}
				assertSeed(t, fmt.Sprintf("%s/rand%d/cpfinal", g.kind, i), finalCP, true, "cK")
				diffChain(t, fmt.Sprintf("%s/rand%d/cpfinal", g.kind, i), finalCP)
			}

			for sh := range 2 {
				diffChain(t, fmt.Sprintf("%s/rand%d/shuf%d", g.kind, i, sh), shuffledCommits(chain, r))
			}
		}

		// Multi-checkpoint chains: nested (newest seeds) and non-nested (newest
		// fails the seed-safety gate, so the whole chain folds with every
		// checkpoint a no-op).
		for _, nested := range []bool{true, false} {
			dbl, err := twoCheckpointChain(g, r, s, nested)
			if err != nil {
				t.Fatalf("%s two-checkpoint build (nested=%v): %v", g.kind, nested, err)
			}
			wantSeed := model.SHA("")
			if nested {
				wantSeed = "cp2"
			}
			assertSeed(t, fmt.Sprintf("%s/2cp/nested=%v", g.kind, nested), dbl, nested, wantSeed)
			diffChain(t, fmt.Sprintf("%s/2cp/nested=%v", g.kind, nested), dbl)
			diffChain(t, fmt.Sprintf("%s/2cp/nested=%v/shuf", g.kind, nested), shuffledCommits(dbl, r))
		}
	}

	// Coverage: every op kind in each generator's vocabulary must have been
	// emitted, so the differential can never silently narrow.
	for _, g := range kindGens() {
		for _, f := range g.ops {
			kind := f(rand.New(rand.NewPCG(1, 1))).OpKind()
			if !seen[g.kind][kind] {
				t.Errorf("coverage: %s op %s never generated", g.kind, kind)
			}
		}
	}

	diffOracleErrorChains(t)
}

// diffOracleErrorChains checks the error paths the random chains do not reliably
// hit: a duplicate create, a foreign op, a non-create first op, and a checkpoint
// whose State is a foreign kind (exercising each folder's seed-mismatch string).
func diffOracleErrorChains(t *testing.T) {
	root := ocommit("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "t"})

	diffChain(t, "err/duplicate_create", []model.PackCommit{
		root,
		ocommit("c1", []string{"c0"}, "bob", 200, 2, model.CreateNote{Nonce: "m"}),
	})
	diffChain(t, "err/foreign_op", []model.PackCommit{
		root,
		ocommit("c1", []string{"c0"}, "bob", 200, 2, model.SetSprintStatus{Status: model.SprintActive}),
	})
	diffChain(t, "err/first_op_not_create", []model.PackCommit{
		ocommit("c0", nil, "alice", 100, 1, model.SetTitle{Title: "x"}),
	})

	// Seed-mismatch: a create_<kind> chain whose sole checkpoint carries a
	// foreign-kind State. selectSeed picks it (seed-safe), and the fold trips
	// its per-kind mismatch message.
	wrong := map[model.Kind]model.Snapshot{
		model.KindNote:    model.Task{ID: "c0"},
		model.KindDoc:     model.Note{ID: "c0"},
		model.KindLog:     model.Note{ID: "c0"},
		model.KindTask:    model.Note{ID: "c0"},
		model.KindSprint:  model.Note{ID: "c0"},
		model.KindProject: model.Note{ID: "c0"},
		model.KindRunbook: model.Note{ID: "c0"},
	}
	for _, g := range kindGens() {
		state := wrong[g.kind]
		chain := []model.PackCommit{
			ocommit("c0", nil, "alice", 100, 1, g.create("n")),
			ocommit("cK", []string{"c0"}, "compactor", 150, 3,
				model.Checkpoint{EntityID: state.EntityID(), State: state, CoversLamport: 1, CoversShas: []model.SHA{"c0"}}),
		}
		diffChain(t, "err/seed_mismatch/"+string(g.kind), chain)
	}
}

// TestFoldersExhaustive pins the dispatch table to exactly model.Kinds().
func TestFoldersExhaustive(t *testing.T) {
	if len(folders) != len(model.Kinds()) {
		t.Fatalf("folders has %d kinds, model.Kinds() has %d", len(folders), len(model.Kinds()))
	}
	for _, k := range model.Kinds() {
		if _, ok := folders[k]; !ok {
			t.Errorf("folders missing kind %s", k)
		}
	}
}
