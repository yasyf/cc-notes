package viz

import (
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

// Entity kind tags, matching the lowercase names internal/trail.EntityKind
// emits; they are the EntityRef.Kind and EntitySummary.Kind wire strings.
const (
	entityNote    = "note"
	entityDoc     = "doc"
	entityLog     = "log"
	entityTask    = "task"
	entitySprint  = "sprint"
	entityProject = "project"
)

// trailCreate is the internal/trail Entry.Kind for a chain's root commit.
const trailCreate = "create"

// Event types, the wire strings the timeline keys on. "created", "status", and
// "edited" are shared across entity kinds; the rest are kind-specific.
const (
	evCreated      = "created"
	evClaimed      = "claimed"
	evReclaimed    = "reclaimed"
	evClosed       = "closed"
	evStatus       = "status"
	evBranchMoved  = "branch_moved"
	evCommitLinked = "commit_linked"
	evEdited       = "edited"
	evVerified     = "verified"
	evSuperseded   = "superseded"
	evStale        = "stale"
	evEntry        = "entry"
)

// statusDeleted is the Lane.Status of a synthesized deleted-branch lane.
const statusDeleted = "deleted"

// eventSpec is one classified event before it is stamped with the entity, time,
// branch, and commit sha of the trail entry that produced it.
type eventSpec struct {
	typ    string
	detail map[string]string
}

// classify maps one non-checkpoint trail entry to its lifecycle events,
// dispatching on the entity kind. An entry usually yields one event; a log
// entry-append and a task commit-link fan out one event per added element.
func classify(entry trail.Entry) []eventSpec {
	switch entry.Snapshot.(type) {
	case model.Task:
		return taskEvents(entry)
	case model.Note, model.Doc:
		return noteEvents(entry)
	case model.Log:
		return logEvents(entry)
	case model.Sprint, model.Project:
		return groupEvents(entry)
	default:
		return nil
	}
}

// taskEvents classifies a task entry: create, the claim/reclaim/close/status
// lifecycle read off the status and assignee deltas, a branch move, one
// commit_linked per added sha, or a plain edit.
func taskEvents(entry trail.Entry) []eventSpec {
	if entry.Kind == trailCreate {
		return []eventSpec{{typ: evCreated}}
	}
	task := entry.Snapshot.(model.Task)
	var specs []eventSpec

	statusCh, hasStatus := changeFor(entry.Changes, "status")
	assigneeCh, hasAssignee := changeFor(entry.Changes, "assignee")
	assigneeSet := hasAssignee && changeSet(assigneeCh.To)
	switch {
	case hasStatus && changeStr(statusCh.To) == string(model.StatusInProgress) && assigneeSet:
		specs = append(specs, eventSpec{typ: evClaimed})
	case !hasStatus && assigneeSet && task.Status == model.StatusInProgress:
		specs = append(specs, eventSpec{typ: evReclaimed})
	case hasStatus && (changeStr(statusCh.To) == string(model.StatusDone) || changeStr(statusCh.To) == string(model.StatusCancelled)):
		specs = append(specs, eventSpec{typ: evClosed})
	case hasStatus:
		specs = append(specs, eventSpec{typ: evStatus})
	}

	if branchCh, ok := changeFor(entry.Changes, "branch"); ok {
		specs = append(specs, eventSpec{typ: evBranchMoved, detail: map[string]string{"from": changeStr(branchCh.From), "to": changeStr(branchCh.To)}})
	}
	if commitCh, ok := changeFor(entry.Changes, "commits"); ok {
		for _, sha := range commitCh.Added {
			specs = append(specs, eventSpec{typ: evCommitLinked, detail: map[string]string{"sha": sha.(string)}})
		}
	}

	if len(specs) == 0 {
		specs = append(specs, eventSpec{typ: evEdited})
	}
	return specs
}

// noteEvents classifies a note or doc entry: create, a verify (verified_at set),
// a supersede edge, a stale flag (stale_at set), or a plain edit. Verify wins
// over the stale-clear it carries.
func noteEvents(entry trail.Entry) []eventSpec {
	if entry.Kind == trailCreate {
		return []eventSpec{{typ: evCreated}}
	}
	if ch, ok := changeFor(entry.Changes, "verified_at"); ok && changeSet(ch.To) {
		return []eventSpec{{typ: evVerified}}
	}
	if ch, ok := changeFor(entry.Changes, "superseded_by"); ok && len(ch.Added) > 0 {
		return []eventSpec{{typ: evSuperseded}}
	}
	if ch, ok := changeFor(entry.Changes, "stale_at"); ok && changeSet(ch.To) {
		return []eventSpec{{typ: evStale}}
	}
	return []eventSpec{{typ: evEdited}}
}

// logEvents classifies a log entry: create, one "entry" event per appended log
// entry carrying its text, or a plain edit.
func logEvents(entry trail.Entry) []eventSpec {
	if entry.Kind == trailCreate {
		return []eventSpec{{typ: evCreated}}
	}
	if ch, ok := changeFor(entry.Changes, "entries"); ok && len(ch.Added) > 0 {
		log := entry.Snapshot.(model.Log)
		added := log.Entries[len(log.Entries)-len(ch.Added):]
		specs := make([]eventSpec, 0, len(added))
		for _, e := range added {
			specs = append(specs, eventSpec{typ: evEntry, detail: map[string]string{"text": e.Text}})
		}
		return specs
	}
	return []eventSpec{{typ: evEdited}}
}

// groupEvents classifies a sprint or project entry: create, a status change, or
// a plain edit.
func groupEvents(entry trail.Entry) []eventSpec {
	if entry.Kind == trailCreate {
		return []eventSpec{{typ: evCreated}}
	}
	if _, ok := changeFor(entry.Changes, "status"); ok {
		return []eventSpec{{typ: evStatus}}
	}
	return []eventSpec{{typ: evEdited}}
}

// changeFor returns the change to the named snapshot field in changes, if any.
func changeFor(changes []trail.Change, field string) (trail.Change, bool) {
	for _, c := range changes {
		if c.Field == field {
			return c, true
		}
	}
	return trail.Change{}, false
}

// changeStr reads a scalar trail value as a string: a string field's value, or
// "" for a nil (unset) field.
func changeStr(v any) string {
	s, _ := v.(string)
	return s
}

// changeSet reports whether a scalar trail value is set: a non-empty string or a
// non-zero number. A nil, empty string, or zero is unset.
func changeSet(v any) bool {
	switch x := v.(type) {
	case string:
		return x != ""
	case float64:
		return x != 0
	default:
		return false
	}
}

// entityRefOf builds the stable EntityRef for an entity from its tip snapshot.
func entityRefOf(snap model.Snapshot) EntityRef {
	id := snap.EntityID()
	return EntityRef{
		Kind:  trail.EntityKind(snap),
		ID:    id,
		Short: id.Short(),
		Title: entityTitle(snap),
	}
}

// entityTitle returns the title of any entity snapshot.
func entityTitle(snap model.Snapshot) string {
	switch s := snap.(type) {
	case model.Note:
		return s.Title
	case model.Doc:
		return s.Title
	case model.Log:
		return s.Title
	case model.Task:
		return s.Title
	case model.Sprint:
		return s.Title
	case model.Project:
		return s.Title
	default:
		return ""
	}
}

// branchOf attributes a snapshot to a branch at its step: a task's branch
// scalar, or the first branch anchor of a note, doc, or log. Sprints and
// projects carry no branch. An empty result is kept as-is.
func branchOf(snap model.Snapshot) string {
	switch s := snap.(type) {
	case model.Task:
		return string(s.Branch)
	case model.Note:
		return firstBranchAnchor(s.Anchors)
	case model.Doc:
		return firstBranchAnchor(s.Anchors)
	case model.Log:
		return firstBranchAnchor(s.Anchors)
	default:
		return ""
	}
}

// firstBranchAnchor returns the value of the first branch anchor, or "".
func firstBranchAnchor(anchors []model.Anchor) string {
	for _, a := range anchors {
		if a.Kind == model.AnchorBranch {
			return a.Value
		}
	}
	return ""
}
