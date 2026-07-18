package viz

import (
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

// Entity kind tags, matching the lowercase names internal/trail.EntityKind
// emits; they are the EntityRef.Kind and EntitySummary.Kind wire strings.
const (
	entityNote          = "note"
	entityDoc           = "doc"
	entityLog           = "log"
	entityTask          = "task"
	entitySprint        = "sprint"
	entityProject       = "project"
	entityRunbook       = "runbook"
	entityInvestigation = "investigation"
)

// trailCreate is the internal/trail Entry.Kind for a chain's root commit.
const trailCreate = "create"

// Event types, the wire strings the timeline keys on. "created", "status", and
// "edited" are shared across entity kinds; the rest are kind-specific.
const (
	evCreated          = "created"
	evClaimed          = "claimed"
	evReclaimed        = "reclaimed"
	evClosed           = "closed"
	evStatus           = "status"
	evBranchMoved      = "branch_moved"
	evCommitLinked     = "commit_linked"
	evEdited           = "edited"
	evVerified         = "verified"
	evSuperseded       = "superseded"
	evStale            = "stale"
	evEntry            = "entry"
	evRunStarted       = "run_started"
	evRunFinished      = "run_finished"
	evFindingCleared   = "finding_cleared"
	evFindingConfirmed = "finding_confirmed"
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
	case model.Runbook:
		return runbookEvents(entry)
	case model.Investigation:
		return investigationEvents(entry)
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

// runbookEvents classifies a runbook entry: create, then — accumulated like
// taskEvents across orthogonal axes — a status change and the run starts and
// finishes read off the runs set-delta, or a plain edit when neither axis fired.
func runbookEvents(entry trail.Entry) []eventSpec {
	if entry.Kind == trailCreate {
		return []eventSpec{{typ: evCreated}}
	}
	var specs []eventSpec
	if _, ok := changeFor(entry.Changes, "status"); ok {
		specs = append(specs, eventSpec{typ: evStatus})
	}
	if ch, ok := changeFor(entry.Changes, "runs"); ok {
		specs = append(specs, runEvents(ch)...)
	}
	if len(specs) == 0 {
		specs = append(specs, eventSpec{typ: evEdited})
	}
	return specs
}

// investigationEvents classifies an investigation entry: create, appended
// evidence, lifecycle status changes, and the cleared or confirmed finding
// dispositions that define the investigation arc. Orthogonal changes in one
// pack accumulate as separate events.
func investigationEvents(entry trail.Entry) []eventSpec {
	if entry.Kind == trailCreate {
		return []eventSpec{{typ: evCreated}}
	}
	var specs []eventSpec
	if ch, ok := changeFor(entry.Changes, "entries"); ok && len(ch.Added) > 0 {
		inv := entry.Snapshot.(model.Investigation)
		added := inv.Entries[len(inv.Entries)-len(ch.Added):]
		for _, e := range added {
			specs = append(specs, eventSpec{typ: evEntry, detail: map[string]string{"text": e.Text}})
		}
	}
	if ch, ok := changeFor(entry.Changes, "status"); ok {
		status := changeStr(ch.To)
		if status == string(model.InvestigationOpen) {
			status = "reopened"
		}
		specs = append(specs, eventSpec{typ: evStatus, detail: map[string]string{"status": status}})
	}
	if ch, ok := changeFor(entry.Changes, "findings"); ok {
		specs = append(specs, findingEvents(ch)...)
	}
	if len(specs) == 0 {
		specs = append(specs, eventSpec{typ: evEdited})
	}
	return specs
}

// findingEvents reads a findings set-delta into first-class disposition
// events. A same-status replacement is a text or note edit, not a repeated
// verdict.
func findingEvents(ch trail.Change) []eventSpec {
	prev := make(map[string]string, len(ch.Removed))
	for _, f := range ch.Removed {
		prev[findingElemID(f)] = findingElemStatus(f)
	}
	var specs []eventSpec
	for _, f := range ch.Added {
		id, status := findingElemID(f), findingElemStatus(f)
		was, paired := prev[id]
		if paired && was == status {
			continue
		}
		detail := map[string]string{"finding": shortID(id)}
		switch status {
		case string(model.FindingCleared):
			specs = append(specs, eventSpec{typ: evFindingCleared, detail: detail})
		case string(model.FindingConfirmed):
			specs = append(specs, eventSpec{typ: evFindingConfirmed, detail: detail})
		}
	}
	return specs
}

// findingElemID reads the id of a findings set element, canonical-JSON decoded
// to a map.
func findingElemID(elem any) string {
	m, ok := elem.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["id"].(string)
	return id
}

// findingElemStatus reads the status of a findings set element.
func findingElemStatus(elem any) string {
	m, ok := elem.(map[string]any)
	if !ok {
		return ""
	}
	status, _ := m["status"].(string)
	return status
}

// runEvents reads a runs set-delta into lifecycle events. Runs diff by
// whole-object identity, so each changed run adds and, unless new, removes a
// same-id twin. An added run with no twin started (run_started); an added run
// that transitioned into a terminal status — no twin, or a twin still running —
// finished (run_finished). Every other pairing (a twin already terminal, or
// both still running) is a step-result correction carrying no event, folded to
// the caller's edit fallback rather than a spurious second run_finished.
func runEvents(ch trail.Change) []eventSpec {
	prev := make(map[string]string, len(ch.Removed))
	for _, r := range ch.Removed {
		prev[runElemID(r)] = runElemStatus(r)
	}
	var specs []eventSpec
	for _, a := range ch.Added {
		id, status := runElemID(a), runElemStatus(a)
		was, paired := prev[id]
		switch {
		case status == string(model.RunRunning) && !paired:
			specs = append(specs, eventSpec{typ: evRunStarted, detail: runStartedDetail(a)})
		case status != string(model.RunRunning) && (!paired || was == string(model.RunRunning)):
			specs = append(specs, eventSpec{typ: evRunFinished, detail: map[string]string{"run": shortID(id), "status": status}})
		}
	}
	return specs
}

// runStartedDetail carries the started run's short id and, when the run cites a
// task, that task's short id.
func runStartedDetail(elem any) map[string]string {
	d := map[string]string{"run": shortID(runElemID(elem))}
	if m, ok := elem.(map[string]any); ok {
		if task, _ := m["task"].(string); task != "" {
			d["task"] = shortID(task)
		}
	}
	return d
}

// runElemID reads the id of a runs set element, canonical-JSON decoded to a map.
func runElemID(elem any) string {
	m, ok := elem.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["id"].(string)
	return id
}

// runElemStatus reads the status of a runs set element.
func runElemStatus(elem any) string {
	m, ok := elem.(map[string]any)
	if !ok {
		return ""
	}
	status, _ := m["status"].(string)
	return status
}

// shortID is the 7-character short form of an entity or nonce id, or the whole
// string when it is already that short.
func shortID(id string) string {
	if len(id) <= 7 {
		return id
	}
	return id[:7]
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
	return snap.Meta().Title
}

// branchOf attributes a snapshot to a branch at its step: a task's branch
// scalar, or the first branch anchor of an anchored entity. Sprints, projects,
// and runbooks carry no branch. An empty result is kept as-is.
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
	case model.Investigation:
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
