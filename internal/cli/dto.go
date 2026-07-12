package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
)

// runbookStepPending is the display status of a run step with no recorded
// result: pending is absence, not a StepResultStatus value.
const runbookStepPending = "pending"

// anchorDTO is one note anchor with its content witness rendered as the git
// object id at verify time, or null when the anchor carries no witness.
type anchorDTO struct {
	Kind    string  `json:"kind"`
	Value   string  `json:"value"`
	Witness *string `json:"witness"`
}

// attachmentDTO is one attachment reference with its content's local
// presence: false means the bytes are not in this repository's LFS store yet
// and download on the next `cc-notes sync`.
type attachmentDTO struct {
	Name    string `json:"name"`
	OID     string `json:"oid"`
	Size    int64  `json:"size"`
	Present bool   `json:"present"`
}

// noteDTO fixes the JSON field order and formats for note output: full hex
// id, RFC3339 UTC timestamps, sorted set slices, per-anchor witnesses, the
// verify metadata (null when never verified), the single replacement id (null
// when not superseded), and the computed drift verdict (null when fresh).
type noteDTO struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	Tags         []string        `json:"tags"`
	Anchors      []anchorDTO     `json:"anchors"`
	Author       string          `json:"author"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	VerifiedAt   *string         `json:"verified_at"`
	VerifiedBy   *string         `json:"verified_by"`
	SupersededBy *string         `json:"superseded_by"`
	Drift        *string         `json:"drift"`
	Deleted      bool            `json:"deleted"`
	StaleAt      *string         `json:"stale_at"`
	StaleBy      *string         `json:"stale_by"`
	StaleReason  *string         `json:"stale_reason"`
	Attachments  []attachmentDTO `json:"attachments"`
}

// docDTO fixes the JSON field order and formats for doc output: the noteDTO
// shape plus the free-text When trigger (always present, surfaced verbatim)
// placed right after the body.
type docDTO struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	When         string          `json:"when"`
	Tags         []string        `json:"tags"`
	Anchors      []anchorDTO     `json:"anchors"`
	Author       string          `json:"author"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	VerifiedAt   *string         `json:"verified_at"`
	VerifiedBy   *string         `json:"verified_by"`
	SupersededBy *string         `json:"superseded_by"`
	Drift        *string         `json:"drift"`
	Deleted      bool            `json:"deleted"`
	StaleAt      *string         `json:"stale_at"`
	StaleBy      *string         `json:"stale_by"`
	StaleReason  *string         `json:"stale_reason"`
	Attachments  []attachmentDTO `json:"attachments"`
}

// logEntryDTO is one append-only log entry with its timestamp rendered RFC3339
// UTC and the optional model identity (null when unset).
type logEntryDTO struct {
	Author string  `json:"author"`
	TS     string  `json:"ts"`
	Text   string  `json:"text"`
	Model  *string `json:"model"`
}

// logDTO fixes the JSON field order and formats for log output: full hex id,
// RFC3339 UTC timestamps, sorted set slices, and the ordered append-only
// entries. A log carries no freshness lifecycle, so there is no
// witness/verify/stale/superseded/drift.
type logDTO struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Entries     []logEntryDTO   `json:"entries"`
	Tags        []string        `json:"tags"`
	Anchors     []anchorDTO     `json:"anchors"`
	Author      string          `json:"author"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	Deleted     bool            `json:"deleted"`
	Attachments []attachmentDTO `json:"attachments"`
}

// commentDTO is one task comment with its timestamp rendered RFC3339 UTC.
type commentDTO struct {
	Author string `json:"author"`
	TS     string `json:"ts"`
	Body   string `json:"body"`
}

// leaseDTO is the task lease: the current holder (the assignee, or null when
// unassigned) and the heartbeat timestamp (the AuthorTime of the assignee's
// latest op as RFC3339 UTC, or null before any claim).
type leaseDTO struct {
	Holder    *string `json:"holder"`
	Heartbeat *string `json:"heartbeat"`
}

// taskDTO fixes the JSON field order and formats for task output: full hex
// ids, RFC3339 UTC timestamps, null for unset optionals, sorted set slices,
// the derived blocks reverse index, the commits that implement the task, and
// the lease.
type taskDTO struct {
	ID           string         `json:"id"`
	Branch       string         `json:"branch"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Type         string         `json:"type"`
	Status       string         `json:"status"`
	Priority     int            `json:"priority"`
	Assignee     *string        `json:"assignee"`
	Labels       []string       `json:"labels"`
	BlockedBy    []string       `json:"blocked_by"`
	Blocks       []string       `json:"blocks"`
	Parent       *string        `json:"parent"`
	Comments     []commentDTO   `json:"comments"`
	Commits      []string       `json:"commits"`
	Lease        leaseDTO       `json:"lease"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	StartedAt    *string        `json:"started_at"`
	ClosedAt     *string        `json:"closed_at"`
	Sprint       *string        `json:"sprint"`
	Project      *string        `json:"project"`
	Criteria     []criterionDTO `json:"criteria"`
	ClosedForced bool           `json:"closed_forced"`
}

// criterionDTO is one structured acceptance criterion: the full nonce id, its
// text, the optional check script (empty when none), and the latest validation
// status.
type criterionDTO struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Script string `json:"script"`
	Status string `json:"status"`
}

// sprintDTO fixes the JSON field order and formats for sprint output: full hex
// ids, RFC3339 UTC timestamps, null for unset optionals, the user-set
// start/end dates (null when 0), sorted set slices, and the full-hex ids of the
// sprint's tasks (the reverse index, passed in).
type sprintDTO struct {
	ID          string       `json:"id"`
	Project     *string      `json:"project"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Status      string       `json:"status"`
	StartDate   *string      `json:"start_date"`
	EndDate     *string      `json:"end_date"`
	Labels      []string     `json:"labels"`
	Commits     []string     `json:"commits"`
	Comments    []commentDTO `json:"comments"`
	Author      string       `json:"author"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	StartedAt   *string      `json:"started_at"`
	ClosedAt    *string      `json:"closed_at"`
	Tasks       []string     `json:"tasks"`
}

// projectDTO fixes the JSON field order and formats for project output: full
// hex ids, RFC3339 UTC timestamps, null for unset optionals, sorted set slices,
// and the full-hex ids of the project's sprints and tasks (the reverse indexes,
// passed in).
type projectDTO struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Status      string       `json:"status"`
	Labels      []string     `json:"labels"`
	Commits     []string     `json:"commits"`
	Comments    []commentDTO `json:"comments"`
	Author      string       `json:"author"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	ClosedAt    *string      `json:"closed_at"`
	Sprints     []string     `json:"sprints"`
	Tasks       []string     `json:"tasks"`
}

// runbookStepDTO is one ordered runbook step: the full nonce id, its
// instruction text, the optional shell command (empty when none), and the
// fractional-index position string.
type runbookStepDTO struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Command  string `json:"command"`
	Position string `json:"position"`
}

// runbookRunStepDTO is one step's status within a run, in runbook step order:
// the full step id, its recorded status ("pending" when no result), and the
// recorded note.
type runbookRunStepDTO struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// runbookRunDTO fixes the JSON field order for one tracked run: the full run
// id, the optional cited task (null when none), the runner, the run status,
// RFC3339 UTC start/finish (finish null while running), and one entry per
// current step in order.
type runbookRunDTO struct {
	ID         string              `json:"id"`
	Task       *string             `json:"task"`
	Runner     string              `json:"runner"`
	Status     string              `json:"status"`
	StartedAt  string              `json:"started_at"`
	FinishedAt *string             `json:"finished_at"`
	Steps      []runbookRunStepDTO `json:"steps"`
}

// runbookDTO fixes the JSON field order and formats for runbook output: full
// hex ids, RFC3339 UTC timestamps, null for the unset archived stamp, sorted
// set slices, the ordered steps, and the append-only runs.
type runbookDTO struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Status      string           `json:"status"`
	Steps       []runbookStepDTO `json:"steps"`
	Runs        []runbookRunDTO  `json:"runs"`
	Labels      []string         `json:"labels"`
	Comments    []commentDTO     `json:"comments"`
	Author      string           `json:"author"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
	ArchivedAt  *string          `json:"archived_at"`
}

func newNoteDTO(n model.Note, drift string, atts []attachmentDTO) noteDTO {
	byAnchor := witnessIndex(n.Witness)
	anchors := make([]anchorDTO, len(n.Anchors))
	for i, a := range n.Anchors {
		var witness *string
		if w, ok := byAnchor[a]; ok {
			oid := string(w.OID)
			witness = &oid
		}
		anchors[i] = anchorDTO{Kind: string(a.Kind), Value: a.Value, Witness: witness}
	}
	var superseded *string
	if len(n.SupersededBy) > 0 {
		id := string(n.SupersededBy[0])
		superseded = &id
	}
	return noteDTO{
		ID:           string(n.ID),
		Title:        n.Title,
		Body:         n.Body,
		Tags:         render.EmptyNotNil(n.Tags),
		Anchors:      anchors,
		Author:       string(n.Author),
		CreatedAt:    render.RFC3339(n.CreatedAt),
		UpdatedAt:    render.RFC3339(n.UpdatedAt),
		VerifiedAt:   render.OptTime(n.VerifiedAt),
		VerifiedBy:   render.OptString(string(n.VerifiedBy)),
		SupersededBy: superseded,
		Drift:        render.OptString(drift),
		Deleted:      n.Deleted,
		StaleAt:      render.OptTime(n.StaleAt),
		StaleBy:      render.OptString(string(n.StaleBy)),
		StaleReason:  render.OptString(n.StaleReason),
		Attachments:  atts,
	}
}

func newDocDTO(d model.Doc, drift string, atts []attachmentDTO) docDTO {
	byAnchor := witnessIndex(d.Witness)
	anchors := make([]anchorDTO, len(d.Anchors))
	for i, a := range d.Anchors {
		var witness *string
		if w, ok := byAnchor[a]; ok {
			oid := string(w.OID)
			witness = &oid
		}
		anchors[i] = anchorDTO{Kind: string(a.Kind), Value: a.Value, Witness: witness}
	}
	var superseded *string
	if len(d.SupersededBy) > 0 {
		id := string(d.SupersededBy[0])
		superseded = &id
	}
	return docDTO{
		ID:           string(d.ID),
		Title:        d.Title,
		Body:         d.Body,
		When:         d.When,
		Tags:         render.EmptyNotNil(d.Tags),
		Anchors:      anchors,
		Author:       string(d.Author),
		CreatedAt:    render.RFC3339(d.CreatedAt),
		UpdatedAt:    render.RFC3339(d.UpdatedAt),
		VerifiedAt:   render.OptTime(d.VerifiedAt),
		VerifiedBy:   render.OptString(string(d.VerifiedBy)),
		SupersededBy: superseded,
		Drift:        render.OptString(drift),
		Deleted:      d.Deleted,
		StaleAt:      render.OptTime(d.StaleAt),
		StaleBy:      render.OptString(string(d.StaleBy)),
		StaleReason:  render.OptString(d.StaleReason),
		Attachments:  atts,
	}
}

// newLogDTO renders a log snapshot into its fixed-order DTO. A log carries no
// per-anchor witness, so every anchor's witness is null.
func newLogDTO(l model.Log, atts []attachmentDTO) logDTO {
	anchors := make([]anchorDTO, len(l.Anchors))
	for i, a := range l.Anchors {
		anchors[i] = anchorDTO{Kind: string(a.Kind), Value: a.Value, Witness: nil}
	}
	return logDTO{
		ID:          string(l.ID),
		Title:       l.Title,
		Entries:     logEntryDTOs(l.Entries),
		Tags:        render.EmptyNotNil(l.Tags),
		Anchors:     anchors,
		Author:      string(l.Author),
		CreatedAt:   render.RFC3339(l.CreatedAt),
		UpdatedAt:   render.RFC3339(l.UpdatedAt),
		Deleted:     l.Deleted,
		Attachments: atts,
	}
}

// logEntryDTOs renders a folded entry slice into its DTO form with RFC3339 UTC
// timestamps, always non-nil so JSON serializes an empty list rather than null.
func logEntryDTOs(entries []model.LogEntry) []logEntryDTO {
	out := make([]logEntryDTO, len(entries))
	for i, e := range entries {
		out[i] = logEntryDTO{Author: string(e.Author), TS: render.RFC3339(e.TS), Text: e.Text, Model: render.OptString(e.Model)}
	}
	return out
}

func newTaskDTO(t model.Task, blocks []model.EntityID) taskDTO {
	return taskDTO{
		ID:           string(t.ID),
		Branch:       string(t.Branch),
		Title:        t.Title,
		Description:  t.Description,
		Type:         string(t.Type),
		Status:       string(t.Status),
		Priority:     int(t.Priority),
		Assignee:     render.OptString(string(t.Assignee)),
		Labels:       render.EmptyNotNil(t.Labels),
		BlockedBy:    render.IDStrings(t.BlockedBy),
		Blocks:       render.IDStrings(blocks),
		Parent:       render.OptString(string(t.Parent)),
		Comments:     commentDTOs(t.Comments),
		Commits:      render.SHAStrings(t.Commits),
		Lease:        leaseDTO{Holder: render.OptString(string(t.Assignee)), Heartbeat: render.OptTime(t.HeartbeatAt)},
		CreatedAt:    render.RFC3339(t.CreatedAt),
		UpdatedAt:    render.RFC3339(t.UpdatedAt),
		StartedAt:    render.OptTime(t.StartedAt),
		ClosedAt:     render.OptTime(t.ClosedAt),
		Sprint:       render.OptString(string(t.Sprint)),
		Project:      render.OptString(string(t.Project)),
		Criteria:     criterionDTOs(t.Criteria),
		ClosedForced: closedForced(t),
	}
}

// criterionDTOs renders a task's criteria as DTOs, always non-nil so JSON
// serializes an empty list rather than null.
func criterionDTOs(criteria []model.Criterion) []criterionDTO {
	out := make([]criterionDTO, len(criteria))
	for i, c := range criteria {
		out[i] = criterionDTO{ID: c.ID, Text: c.Text, Script: c.Script, Status: string(c.Status)}
	}
	return out
}

// closedForced reports whether a done task was closed with at least one
// criterion still unmet — the force-close escape hatch leaves a visible mark.
func closedForced(t model.Task) bool {
	if t.Status != model.StatusDone {
		return false
	}
	for _, c := range t.Criteria {
		if c.Status != model.CriterionMet {
			return true
		}
	}
	return false
}

// commentDTOs renders a folded comment slice into its DTO form with RFC3339 UTC
// timestamps.
func commentDTOs(comments []model.Comment) []commentDTO {
	out := make([]commentDTO, len(comments))
	for i, c := range comments {
		out[i] = commentDTO{Author: string(c.Author), TS: render.RFC3339(c.TS), Body: c.Body}
	}
	return out
}

// newSprintDTO renders a sprint snapshot plus its reverse-index task ids into
// its fixed-order DTO.
func newSprintDTO(s model.Sprint, tasks []model.EntityID) sprintDTO {
	return sprintDTO{
		ID:          string(s.ID),
		Project:     render.OptString(string(s.Project)),
		Title:       s.Title,
		Description: s.Description,
		Status:      string(s.Status),
		StartDate:   render.OptTime(s.StartDate),
		EndDate:     render.OptTime(s.EndDate),
		Labels:      render.EmptyNotNil(s.Labels),
		Commits:     render.SHAStrings(s.Commits),
		Comments:    commentDTOs(s.Comments),
		Author:      string(s.Author),
		CreatedAt:   render.RFC3339(s.CreatedAt),
		UpdatedAt:   render.RFC3339(s.UpdatedAt),
		StartedAt:   render.OptTime(s.StartedAt),
		ClosedAt:    render.OptTime(s.ClosedAt),
		Tasks:       render.IDStrings(tasks),
	}
}

// newProjectDTO renders a project snapshot plus its reverse-index sprint and
// task ids into its fixed-order DTO.
func newProjectDTO(p model.Project, sprints, tasks []model.EntityID) projectDTO {
	return projectDTO{
		ID:          string(p.ID),
		Title:       p.Title,
		Description: p.Description,
		Status:      string(p.Status),
		Labels:      render.EmptyNotNil(p.Labels),
		Commits:     render.SHAStrings(p.Commits),
		Comments:    commentDTOs(p.Comments),
		Author:      string(p.Author),
		CreatedAt:   render.RFC3339(p.CreatedAt),
		UpdatedAt:   render.RFC3339(p.UpdatedAt),
		ClosedAt:    render.OptTime(p.ClosedAt),
		Sprints:     render.IDStrings(sprints),
		Tasks:       render.IDStrings(tasks),
	}
}

// runbookStepDTOs renders a folded step slice into its fixed-order DTO form,
// always non-nil so an empty runbook marshals steps as [].
func runbookStepDTOs(steps []model.RunbookStep) []runbookStepDTO {
	out := make([]runbookStepDTO, len(steps))
	for i, st := range steps {
		out[i] = runbookStepDTO{ID: st.ID, Text: st.Text, Command: st.Command, Position: st.Position}
	}
	return out
}

// newRunbookRunDTO renders one run into its DTO, projecting the run's recorded
// results onto the runbook's current steps in order: a step with no result is
// "pending". Results for removed steps are historical and not shown.
func newRunbookRunDTO(rb model.Runbook, run model.RunbookRun) runbookRunDTO {
	byStep := make(map[string]model.RunbookStepResult, len(run.Results))
	for _, r := range run.Results {
		byStep[r.StepID] = r
	}
	steps := make([]runbookRunStepDTO, len(rb.Steps))
	for i, st := range rb.Steps {
		entry := runbookRunStepDTO{Step: st.ID, Status: runbookStepPending}
		if res, ok := byStep[st.ID]; ok {
			entry.Status = string(res.Status)
			entry.Note = res.Note
		}
		steps[i] = entry
	}
	return runbookRunDTO{
		ID:         run.ID,
		Task:       render.OptString(string(run.Task)),
		Runner:     string(run.Runner),
		Status:     string(run.Status),
		StartedAt:  render.RFC3339(run.StartedAt),
		FinishedAt: render.OptTime(run.FinishedAt),
		Steps:      steps,
	}
}

// newRunbookDTO renders a runbook snapshot into its fixed-order DTO.
func newRunbookDTO(rb model.Runbook) runbookDTO {
	runs := make([]runbookRunDTO, len(rb.Runs))
	for i, r := range rb.Runs {
		runs[i] = newRunbookRunDTO(rb, r)
	}
	return runbookDTO{
		ID:          string(rb.ID),
		Title:       rb.Title,
		Description: rb.Description,
		Status:      string(rb.Status),
		Steps:       runbookStepDTOs(rb.Steps),
		Runs:        runs,
		Labels:      render.EmptyNotNil(rb.Labels),
		Comments:    commentDTOs(rb.Comments),
		Author:      string(rb.Author),
		CreatedAt:   render.RFC3339(rb.CreatedAt),
		UpdatedAt:   render.RFC3339(rb.UpdatedAt),
		ArchivedAt:  render.OptTime(rb.ArchivedAt),
	}
}

// printJSON writes v as one compact JSON document with a trailing newline.
func printJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
