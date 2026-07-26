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
// object id at verify time, omitted when the anchor carries no witness.
type anchorDTO struct {
	Kind    string  `json:"kind"`
	Value   string  `json:"value"`
	Witness *string `json:"witness,omitempty"`
}

// attachmentDTO is one attachment reference with its content's local
// presence: false means the bytes are not in this repository's LFS store yet
// and download on the next `cc-notes sync`.
type attachmentDTO struct {
	Name    string `json:"name"`
	OID     string `json:"oid"`
	Size    int64  `json:"size,omitempty"`
	Present bool   `json:"present"`
}

// noteDTO fixes the JSON field order and formats for note output: full hex
// id, RFC3339 UTC timestamps, sorted set slices, per-anchor witnesses, the
// verify metadata, the single replacement id, and the computed drift verdict.
// Only the id, title, and timestamps survive their zero value.
type noteDTO struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Body         string          `json:"body,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Anchors      []anchorDTO     `json:"anchors,omitempty"`
	Author       string          `json:"author,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	VerifiedAt   *string         `json:"verified_at,omitempty"`
	VerifiedBy   *string         `json:"verified_by,omitempty"`
	SupersededBy *string         `json:"superseded_by,omitempty"`
	Drift        *string         `json:"drift,omitempty"`
	Deleted      bool            `json:"deleted,omitempty"`
	StaleAt      *string         `json:"stale_at,omitempty"`
	StaleBy      *string         `json:"stale_by,omitempty"`
	StaleReason  *string         `json:"stale_reason,omitempty"`
	Attachments  []attachmentDTO `json:"attachments,omitempty"`
}

// noteSummaryDTO is one note in a listing or write acknowledgement: the
// identity, the tags, and the drift verdict, without the body.
type noteSummaryDTO struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Tags      []string `json:"tags,omitempty"`
	Author    string   `json:"author,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	Drift     string   `json:"drift,omitempty"`
}

// docDTO fixes the JSON field order and formats for doc output: the noteDTO
// shape plus the free-text When trigger, surfaced verbatim right after the
// body.
type docDTO struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Body         string          `json:"body,omitempty"`
	When         string          `json:"when,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Anchors      []anchorDTO     `json:"anchors,omitempty"`
	Author       string          `json:"author,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	VerifiedAt   *string         `json:"verified_at,omitempty"`
	VerifiedBy   *string         `json:"verified_by,omitempty"`
	SupersededBy *string         `json:"superseded_by,omitempty"`
	Drift        *string         `json:"drift,omitempty"`
	Deleted      bool            `json:"deleted,omitempty"`
	StaleAt      *string         `json:"stale_at,omitempty"`
	StaleBy      *string         `json:"stale_by,omitempty"`
	StaleReason  *string         `json:"stale_reason,omitempty"`
	Attachments  []attachmentDTO `json:"attachments,omitempty"`
}

// docSummaryDTO is the noteSummaryDTO shape plus the free-text When trigger,
// the field a reader needs to decide whether the doc applies.
type docSummaryDTO struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	When      string   `json:"when,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Author    string   `json:"author,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	Drift     string   `json:"drift,omitempty"`
}

// logEntryDTO is one append-only log entry with its timestamp rendered RFC3339
// UTC and the optional model identity (omitted when unset).
type logEntryDTO struct {
	Author string  `json:"author,omitempty"`
	TS     string  `json:"ts"`
	Text   string  `json:"text"`
	Model  *string `json:"model,omitempty"`
}

// logDTO fixes the JSON field order and formats for log output: full hex id,
// RFC3339 UTC timestamps, sorted set slices, and the ordered append-only
// entries. A log carries no freshness lifecycle, so there is no
// witness/verify/stale/superseded/drift.
type logDTO struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Entries     []logEntryDTO   `json:"entries,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Anchors     []anchorDTO     `json:"anchors,omitempty"`
	Author      string          `json:"author,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	Deleted     bool            `json:"deleted,omitempty"`
	Attachments []attachmentDTO `json:"attachments,omitempty"`
}

// logSummaryDTO is one log in a listing or write acknowledgement: the identity
// and the entry tally, without the entries themselves.
type logSummaryDTO struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Tags       []string `json:"tags,omitempty"`
	Author     string   `json:"author,omitempty"`
	UpdatedAt  string   `json:"updated_at"`
	EntryCount int      `json:"entry_count,omitempty"`
}

// findingDTO is one investigation finding with its structural disposition and
// the evidence supporting the latest disposition.
type findingDTO struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// investigationDTO fixes the JSON field order and formats for investigation
// output, including the immutable premise, structural lifecycle, ordered
// findings and timeline entries, verdict fields, and outbound evidence links.
type investigationDTO struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Premise      string          `json:"premise,omitempty"`
	Body         string          `json:"body,omitempty"`
	Status       string          `json:"status"`
	RootCause    string          `json:"root_cause,omitempty"`
	Findings     []findingDTO    `json:"findings,omitempty"`
	Entries      []logEntryDTO   `json:"entries,omitempty"`
	FollowUps    []string        `json:"follow_ups,omitempty"`
	FixCommits   []string        `json:"fix_commits,omitempty"`
	Commits      []string        `json:"commits,omitempty"`
	Labels       []string        `json:"labels,omitempty"`
	Anchors      []anchorDTO     `json:"anchors,omitempty"`
	SupersededBy []string        `json:"superseded_by,omitempty"`
	Author       string          `json:"author,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	ClosedAt     *string         `json:"closed_at,omitempty"`
	ClosedBy     *string         `json:"closed_by,omitempty"`
	Deleted      bool            `json:"deleted,omitempty"`
	Attachments  []attachmentDTO `json:"attachments,omitempty"`
}

// investigationSummaryDTO is one investigation in a listing or write
// acknowledgement: the identity, the lifecycle status, and the finding and
// timeline tallies, without the premise, findings, or entries.
type investigationSummaryDTO struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	UpdatedAt    string `json:"updated_at"`
	FindingCount int    `json:"finding_count,omitempty"`
	EntryCount   int    `json:"entry_count,omitempty"`
}

// commentDTO is one task comment with its timestamp rendered RFC3339 UTC.
type commentDTO struct {
	Author string `json:"author,omitempty"`
	TS     string `json:"ts"`
	Body   string `json:"body"`
}

// leaseDTO is the task lease: the current holder (the assignee, omitted when
// unassigned) and the heartbeat timestamp (the AuthorTime of the assignee's
// latest op as RFC3339 UTC, omitted before any claim).
type leaseDTO struct {
	Holder    *string `json:"holder,omitempty"`
	Heartbeat *string `json:"heartbeat,omitempty"`
}

// taskDTO fixes the JSON field order and formats for task output: full hex
// ids, RFC3339 UTC timestamps, sorted set slices, the derived blocks reverse
// index, the commits that implement the task, and the lease (omitted while
// unheld).
type taskDTO struct {
	ID           string         `json:"id"`
	Branch       string         `json:"branch,omitempty"`
	Title        string         `json:"title"`
	Description  string         `json:"description,omitempty"`
	Type         string         `json:"type,omitempty"`
	Status       string         `json:"status"`
	Priority     int            `json:"priority"`
	Assignee     *string        `json:"assignee,omitempty"`
	Labels       []string       `json:"labels,omitempty"`
	BlockedBy    []string       `json:"blocked_by,omitempty"`
	Blocks       []string       `json:"blocks,omitempty"`
	Parent       *string        `json:"parent,omitempty"`
	Comments     []commentDTO   `json:"comments,omitempty"`
	Commits      []string       `json:"commits,omitempty"`
	Lease        *leaseDTO      `json:"lease,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	StartedAt    *string        `json:"started_at,omitempty"`
	ClosedAt     *string        `json:"closed_at,omitempty"`
	Sprint       *string        `json:"sprint,omitempty"`
	Project      *string        `json:"project,omitempty"`
	Criteria     []criterionDTO `json:"criteria,omitempty"`
	ClosedForced bool           `json:"closed_forced,omitempty"`
}

// taskSummaryDTO is one task in a listing or write acknowledgement: the fields
// a reader needs to triage and claim, without the description, comments, or
// criteria.
type taskSummaryDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority,omitempty"`
	Assignee  string `json:"assignee,omitempty"`
	Branch    string `json:"branch,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// criterionDTO is one structured acceptance criterion: the full nonce id, its
// text, the optional check script (omitted when none), the latest validation
// status, and the optional evidence note recorded with that verdict.
type criterionDTO struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Script string `json:"script,omitempty"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// criterionSummaryDTO is one acceptance criterion in a listing: the identity,
// the latest validation status, and the evidence note, reporting whether a
// check script is attached rather than carrying its body.
type criterionSummaryDTO struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	Note      string `json:"note,omitempty"`
	HasScript bool   `json:"has_script,omitempty"`
}

// sprintDTO fixes the JSON field order and formats for sprint output: full hex
// ids, RFC3339 UTC timestamps, the user-set start/end dates, sorted set
// slices, and the full-hex ids of the sprint's tasks (the reverse index,
// passed in).
type sprintDTO struct {
	ID          string       `json:"id"`
	Project     *string      `json:"project,omitempty"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Status      string       `json:"status"`
	StartDate   *string      `json:"start_date,omitempty"`
	EndDate     *string      `json:"end_date,omitempty"`
	Labels      []string     `json:"labels,omitempty"`
	Commits     []string     `json:"commits,omitempty"`
	Comments    []commentDTO `json:"comments,omitempty"`
	Author      string       `json:"author,omitempty"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	StartedAt   *string      `json:"started_at,omitempty"`
	ClosedAt    *string      `json:"closed_at,omitempty"`
	Tasks       []string     `json:"tasks,omitempty"`
}

// sprintSummaryDTO is one sprint in a listing or write acknowledgement: the
// identity and lifecycle status, without the description, comments, or task
// roll-up.
type sprintSummaryDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

// projectDTO fixes the JSON field order and formats for project output: full
// hex ids, RFC3339 UTC timestamps, sorted set slices, and the full-hex ids of
// the project's sprints and tasks (the reverse indexes, passed in).
type projectDTO struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Status      string       `json:"status"`
	Labels      []string     `json:"labels,omitempty"`
	Commits     []string     `json:"commits,omitempty"`
	Comments    []commentDTO `json:"comments,omitempty"`
	Author      string       `json:"author,omitempty"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	ClosedAt    *string      `json:"closed_at,omitempty"`
	Sprints     []string     `json:"sprints,omitempty"`
	Tasks       []string     `json:"tasks,omitempty"`
}

// projectSummaryDTO is one project in a listing or write acknowledgement: the
// identity and lifecycle status, without the description, comments, or sprint
// and task roll-ups.
type projectSummaryDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

// runbookStepDTO is one ordered runbook step: the full nonce id, its
// instruction text, the optional shell command (omitted when none), and the
// fractional-index position string.
type runbookStepDTO struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Command  string `json:"command,omitempty"`
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
// id, the optional cited task, the runner, the run status, RFC3339 UTC
// start/finish (finish omitted while running), and one entry per current step
// in order.
type runbookRunDTO struct {
	ID         string              `json:"id"`
	Task       *string             `json:"task,omitempty"`
	Runner     string              `json:"runner,omitempty"`
	Status     string              `json:"status"`
	StartedAt  string              `json:"started_at"`
	FinishedAt *string             `json:"finished_at,omitempty"`
	Steps      []runbookRunStepDTO `json:"steps,omitempty"`
}

// runbookRunSummaryDTO is one tracked run in a listing or write
// acknowledgement: the identity, the runner, and the step progress, without
// the per-step entries.
type runbookRunSummaryDTO struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Runner     string `json:"runner,omitempty"`
	StartedAt  string `json:"started_at"`
	StepsDone  int    `json:"steps_done,omitempty"`
	StepsTotal int    `json:"steps_total,omitempty"`
}

// runbookDTO fixes the JSON field order and formats for runbook output: full
// hex ids, RFC3339 UTC timestamps, sorted set slices, the ordered steps, and
// the append-only runs.
type runbookDTO struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description,omitempty"`
	Status      string           `json:"status"`
	Steps       []runbookStepDTO `json:"steps,omitempty"`
	Runs        []runbookRunDTO  `json:"runs,omitempty"`
	Labels      []string         `json:"labels,omitempty"`
	Anchors     []anchorDTO      `json:"anchors,omitempty"`
	Comments    []commentDTO     `json:"comments,omitempty"`
	Author      string           `json:"author,omitempty"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
	ArchivedAt  *string          `json:"archived_at,omitempty"`
	Deleted     bool             `json:"deleted,omitempty"`
}

// runbookSummaryDTO is one runbook in a listing or write acknowledgement: the
// identity, the lifecycle status, and the step and run tallies, without the
// steps or runs.
type runbookSummaryDTO struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
	StepCount int    `json:"step_count,omitempty"`
	RunCount  int    `json:"run_count,omitempty"`
}

func newNoteDTO(n model.Note, drift string, atts []attachmentDTO) noteDTO {
	byAnchor := witnessIndex(n.Witness)
	var anchors []anchorDTO
	for _, a := range n.Anchors {
		var witness *string
		if w, ok := byAnchor[a]; ok {
			oid := string(w.OID)
			witness = &oid
		}
		anchors = append(anchors, anchorDTO{Kind: string(a.Kind), Value: a.Value, Witness: witness})
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
		Tags:         nilIfEmpty(n.Tags),
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
		Attachments:  nilIfEmpty(atts),
	}
}

// newNoteSummaryDTO renders a note snapshot and its drift verdict into the
// listing projection, carrying the full id so it stays a usable handle.
func newNoteSummaryDTO(n model.Note, drift string) noteSummaryDTO {
	return noteSummaryDTO{
		ID:        string(n.ID),
		Title:     n.Title,
		Tags:      nilIfEmpty(n.Tags),
		Author:    string(n.Author),
		UpdatedAt: render.RFC3339(n.UpdatedAt),
		Drift:     drift,
	}
}

func newDocDTO(d model.Doc, drift string, atts []attachmentDTO) docDTO {
	byAnchor := witnessIndex(d.Witness)
	var anchors []anchorDTO
	for _, a := range d.Anchors {
		var witness *string
		if w, ok := byAnchor[a]; ok {
			oid := string(w.OID)
			witness = &oid
		}
		anchors = append(anchors, anchorDTO{Kind: string(a.Kind), Value: a.Value, Witness: witness})
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
		Tags:         nilIfEmpty(d.Tags),
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
		Attachments:  nilIfEmpty(atts),
	}
}

// newDocSummaryDTO renders a doc snapshot and its drift verdict into the
// listing projection, keeping the When trigger the reader selects on.
func newDocSummaryDTO(d model.Doc, drift string) docSummaryDTO {
	return docSummaryDTO{
		ID:        string(d.ID),
		Title:     d.Title,
		When:      d.When,
		Tags:      nilIfEmpty(d.Tags),
		Author:    string(d.Author),
		UpdatedAt: render.RFC3339(d.UpdatedAt),
		Drift:     drift,
	}
}

// newLogDTO renders a log snapshot into its DTO. A log carries no per-anchor
// witness, so every anchor's witness is omitted.
func newLogDTO(l model.Log, atts []attachmentDTO) logDTO {
	var anchors []anchorDTO
	for _, a := range l.Anchors {
		anchors = append(anchors, anchorDTO{Kind: string(a.Kind), Value: a.Value, Witness: nil})
	}
	return logDTO{
		ID:          string(l.ID),
		Title:       l.Title,
		Entries:     logEntryDTOs(l.Entries),
		Tags:        nilIfEmpty(l.Tags),
		Anchors:     anchors,
		Author:      string(l.Author),
		CreatedAt:   render.RFC3339(l.CreatedAt),
		UpdatedAt:   render.RFC3339(l.UpdatedAt),
		Deleted:     l.Deleted,
		Attachments: nilIfEmpty(atts),
	}
}

// newLogSummaryDTO renders a log snapshot into the listing projection, trading
// the entries for their count.
func newLogSummaryDTO(l model.Log) logSummaryDTO {
	return logSummaryDTO{
		ID:         string(l.ID),
		Title:      l.Title,
		Tags:       nilIfEmpty(l.Tags),
		Author:     string(l.Author),
		UpdatedAt:  render.RFC3339(l.UpdatedAt),
		EntryCount: len(l.Entries),
	}
}

// logEntryDTOs renders a folded entry slice into its DTO form with RFC3339 UTC
// timestamps, nil when there are no entries.
func logEntryDTOs(entries []model.LogEntry) []logEntryDTO {
	var out []logEntryDTO
	for _, e := range entries {
		out = append(out, logEntryDTO{Author: string(e.Author), TS: render.RFC3339(e.TS), Text: e.Text, Model: render.OptString(e.Model)})
	}
	return out
}

// findingDTOs renders findings into their DTO form, nil when there are none.
func findingDTOs(findings []model.Finding) []findingDTO {
	var out []findingDTO
	for _, finding := range findings {
		out = append(out, findingDTO{ID: finding.ID, Text: finding.Text, Status: string(finding.Status), Note: finding.Note})
	}
	return out
}

// newInvestigationDTO renders an investigation snapshot into its DTO.
func newInvestigationDTO(inv model.Investigation, atts []attachmentDTO) investigationDTO {
	var anchors []anchorDTO
	for _, anchor := range inv.Anchors {
		anchors = append(anchors, anchorDTO{Kind: string(anchor.Kind), Value: anchor.Value, Witness: nil})
	}
	return investigationDTO{
		ID:           string(inv.ID),
		Title:        inv.Title,
		Premise:      inv.Premise,
		Body:         inv.Body,
		Status:       string(inv.Status),
		RootCause:    inv.RootCause,
		Findings:     findingDTOs(inv.Findings),
		Entries:      logEntryDTOs(inv.Entries),
		FollowUps:    nilIfEmpty(render.IDStrings(inv.FollowUps)),
		FixCommits:   nilIfEmpty(render.SHAStrings(inv.FixCommits)),
		Commits:      nilIfEmpty(render.SHAStrings(inv.Commits)),
		Labels:       nilIfEmpty(inv.Tags),
		Anchors:      anchors,
		SupersededBy: nilIfEmpty(render.IDStrings(inv.SupersededBy)),
		Author:       string(inv.Author),
		CreatedAt:    render.RFC3339(inv.CreatedAt),
		UpdatedAt:    render.RFC3339(inv.UpdatedAt),
		ClosedAt:     render.OptTime(inv.ClosedAt),
		ClosedBy:     render.OptString(string(inv.ClosedBy)),
		Deleted:      inv.Deleted,
		Attachments:  nilIfEmpty(atts),
	}
}

// newInvestigationSummaryDTO renders an investigation snapshot into the
// listing projection, trading the findings and entries for their counts.
func newInvestigationSummaryDTO(inv model.Investigation) investigationSummaryDTO {
	return investigationSummaryDTO{
		ID:           string(inv.ID),
		Title:        inv.Title,
		Status:       string(inv.Status),
		UpdatedAt:    render.RFC3339(inv.UpdatedAt),
		FindingCount: len(inv.Findings),
		EntryCount:   len(inv.Entries),
	}
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
		Labels:       nilIfEmpty(t.Labels),
		BlockedBy:    nilIfEmpty(render.IDStrings(t.BlockedBy)),
		Blocks:       nilIfEmpty(render.IDStrings(blocks)),
		Parent:       render.OptString(string(t.Parent)),
		Comments:     commentDTOs(t.Comments),
		Commits:      nilIfEmpty(render.SHAStrings(t.Commits)),
		Lease:        newLeaseDTO(t),
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

// newTaskSummaryDTO renders a task snapshot into the listing projection: the
// triage and claim fields, without the description, comments, or criteria.
func newTaskSummaryDTO(t model.Task) taskSummaryDTO {
	return taskSummaryDTO{
		ID:        string(t.ID),
		Title:     t.Title,
		Status:    string(t.Status),
		Priority:  int(t.Priority),
		Assignee:  string(t.Assignee),
		Branch:    string(t.Branch),
		UpdatedAt: render.RFC3339(t.UpdatedAt),
	}
}

// newLeaseDTO renders a task's lease, nil when the task was never claimed.
func newLeaseDTO(t model.Task) *leaseDTO {
	holder := render.OptString(string(t.Assignee))
	heartbeat := render.OptTime(t.HeartbeatAt)
	if holder == nil && heartbeat == nil {
		return nil
	}
	return &leaseDTO{Holder: holder, Heartbeat: heartbeat}
}

// criterionDTOs renders a task's criteria as DTOs, nil when there are none.
func criterionDTOs(criteria []model.Criterion) []criterionDTO {
	var out []criterionDTO
	for _, c := range criteria {
		out = append(out, criterionDTO{ID: c.ID, Text: c.Text, Script: c.Script, Status: string(c.Status), Note: c.Note})
	}
	return out
}

// criterionSummaryDTOs renders a task's criteria as summaries, nil when there
// are none.
func criterionSummaryDTOs(criteria []model.Criterion) []criterionSummaryDTO {
	var out []criterionSummaryDTO
	for _, c := range criteria {
		out = append(out, criterionSummaryDTO{ID: c.ID, Text: c.Text, Status: string(c.Status), Note: c.Note, HasScript: c.Script != ""})
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
// timestamps, nil when there are no comments.
func commentDTOs(comments []model.Comment) []commentDTO {
	var out []commentDTO
	for _, c := range comments {
		out = append(out, commentDTO{Author: string(c.Author), TS: render.RFC3339(c.TS), Body: c.Body})
	}
	return out
}

// newSprintDTO renders a sprint snapshot plus its reverse-index task ids into
// its DTO.
func newSprintDTO(s model.Sprint, tasks []model.EntityID) sprintDTO {
	return sprintDTO{
		ID:          string(s.ID),
		Project:     render.OptString(string(s.Project)),
		Title:       s.Title,
		Description: s.Description,
		Status:      string(s.Status),
		StartDate:   render.OptTime(s.StartDate),
		EndDate:     render.OptTime(s.EndDate),
		Labels:      nilIfEmpty(s.Labels),
		Commits:     nilIfEmpty(render.SHAStrings(s.Commits)),
		Comments:    commentDTOs(s.Comments),
		Author:      string(s.Author),
		CreatedAt:   render.RFC3339(s.CreatedAt),
		UpdatedAt:   render.RFC3339(s.UpdatedAt),
		StartedAt:   render.OptTime(s.StartedAt),
		ClosedAt:    render.OptTime(s.ClosedAt),
		Tasks:       nilIfEmpty(render.IDStrings(tasks)),
	}
}

// newSprintSummaryDTO renders a sprint snapshot into the listing projection.
func newSprintSummaryDTO(s model.Sprint) sprintSummaryDTO {
	return sprintSummaryDTO{
		ID:        string(s.ID),
		Title:     s.Title,
		Status:    string(s.Status),
		UpdatedAt: render.RFC3339(s.UpdatedAt),
	}
}

// newProjectDTO renders a project snapshot plus its reverse-index sprint and
// task ids into its DTO.
func newProjectDTO(p model.Project, sprints, tasks []model.EntityID) projectDTO {
	return projectDTO{
		ID:          string(p.ID),
		Title:       p.Title,
		Description: p.Description,
		Status:      string(p.Status),
		Labels:      nilIfEmpty(p.Labels),
		Commits:     nilIfEmpty(render.SHAStrings(p.Commits)),
		Comments:    commentDTOs(p.Comments),
		Author:      string(p.Author),
		CreatedAt:   render.RFC3339(p.CreatedAt),
		UpdatedAt:   render.RFC3339(p.UpdatedAt),
		ClosedAt:    render.OptTime(p.ClosedAt),
		Sprints:     nilIfEmpty(render.IDStrings(sprints)),
		Tasks:       nilIfEmpty(render.IDStrings(tasks)),
	}
}

// newProjectSummaryDTO renders a project snapshot into the listing projection.
func newProjectSummaryDTO(p model.Project) projectSummaryDTO {
	return projectSummaryDTO{
		ID:        string(p.ID),
		Title:     p.Title,
		Status:    string(p.Status),
		UpdatedAt: render.RFC3339(p.UpdatedAt),
	}
}

// runbookStepDTOs renders a folded step slice into its DTO form, nil when the
// runbook has no steps.
func runbookStepDTOs(steps []model.RunbookStep) []runbookStepDTO {
	var out []runbookStepDTO
	for _, st := range steps {
		out = append(out, runbookStepDTO{ID: st.ID, Text: st.Text, Command: st.Command, Position: st.Position})
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
	var steps []runbookRunStepDTO
	for _, st := range rb.Steps {
		entry := runbookRunStepDTO{Step: st.ID, Status: runbookStepPending}
		if res, ok := byStep[st.ID]; ok {
			entry.Status = string(res.Status)
			entry.Note = res.Note
		}
		steps = append(steps, entry)
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

// newRunbookRunSummaryDTO renders one run into the listing projection, trading
// the per-step entries for the done-over-total tally.
func newRunbookRunSummaryDTO(rb model.Runbook, run model.RunbookRun) runbookRunSummaryDTO {
	done, _, _ := runStepCounts(rb, run)
	return runbookRunSummaryDTO{
		ID:         run.ID,
		Status:     string(run.Status),
		Runner:     string(run.Runner),
		StartedAt:  render.RFC3339(run.StartedAt),
		StepsDone:  done,
		StepsTotal: len(rb.Steps),
	}
}

// newRunbookDTO renders a runbook snapshot into its DTO. Like a log, a runbook
// anchor carries no witness, so every anchor's witness is omitted.
func newRunbookDTO(rb model.Runbook) runbookDTO {
	var runs []runbookRunDTO
	for _, r := range rb.Runs {
		runs = append(runs, newRunbookRunDTO(rb, r))
	}
	var anchors []anchorDTO
	for _, a := range rb.Anchors {
		anchors = append(anchors, anchorDTO{Kind: string(a.Kind), Value: a.Value, Witness: nil})
	}
	return runbookDTO{
		ID:          string(rb.ID),
		Title:       rb.Title,
		Description: rb.Description,
		Status:      string(rb.Status),
		Steps:       runbookStepDTOs(rb.Steps),
		Runs:        runs,
		Labels:      nilIfEmpty(rb.Labels),
		Anchors:     anchors,
		Comments:    commentDTOs(rb.Comments),
		Author:      string(rb.Author),
		CreatedAt:   render.RFC3339(rb.CreatedAt),
		UpdatedAt:   render.RFC3339(rb.UpdatedAt),
		ArchivedAt:  render.OptTime(rb.ArchivedAt),
		Deleted:     rb.Deleted,
	}
}

// newRunbookSummaryDTO renders a runbook snapshot into the listing projection,
// trading the steps and runs for their counts.
func newRunbookSummaryDTO(rb model.Runbook) runbookSummaryDTO {
	return runbookSummaryDTO{
		ID:        string(rb.ID),
		Title:     rb.Title,
		Status:    string(rb.Status),
		UpdatedAt: render.RFC3339(rb.UpdatedAt),
		StepCount: len(rb.Steps),
		RunCount:  len(rb.Runs),
	}
}

// nilIfEmpty returns nil for an empty slice so an omitempty field vanishes
// instead of marshaling as [].
func nilIfEmpty[T any](items []T) []T {
	if len(items) == 0 {
		return nil
	}
	return items
}

// tailWithCount returns the last n items of items and the number elided.
func tailWithCount[T any](items []T, n int) ([]T, int) {
	if len(items) <= n {
		return items, 0
	}
	return items[len(items)-n:], len(items) - n
}

// printJSONList writes items as a whole JSON response, rendering an empty
// collection as [] rather than null. The DTO constructors return a nil slice for
// "no members" so an embedded field omits itself, but a nil slice marshals to
// null, which a client iterating a whole response cannot consume.
func printJSONList[T any](w io.Writer, items []T) error {
	if items == nil {
		items = []T{}
	}
	return printJSON(w, items)
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
