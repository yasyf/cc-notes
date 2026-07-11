package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/render"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

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
// UTC.
type logEntryDTO struct {
	Author string `json:"author"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
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

// statusDTO fixes the JSON field order for a status report: the current
// branch, the backlog and your-branch task slices, the in-progress tasks
// grouped by assignee, and the note, doc, and log summaries.
type statusDTO struct {
	Branch     string              `json:"branch"`
	Backlog    []taskDTO           `json:"backlog"`
	YourBranch []taskDTO           `json:"your_branch"`
	InProgress []statusAssigneeDTO `json:"in_progress"`
	Notes      statusNotesDTO      `json:"notes"`
	Docs       statusNotesDTO      `json:"docs"`
	Logs       statusLogsDTO       `json:"logs"`
}

// statusAssigneeDTO groups one assignee's in-progress tasks.
type statusAssigneeDTO struct {
	Assignee string           `json:"assignee"`
	Tasks    []statusStaleDTO `json:"tasks"`
}

// statusStaleDTO embeds a taskDTO, inlining its fields, plus the reader-side
// stale verdict.
type statusStaleDTO struct {
	taskDTO
	Stale bool `json:"stale"`
}

// statusNotesDTO is the note summary: total notes and the count needing review.
type statusNotesDTO struct {
	Total       int `json:"total"`
	NeedsReview int `json:"needs_review"`
}

// statusLogsDTO is the log summary: total logs. Logs have no freshness
// lifecycle, so there is no needs_review count.
type statusLogsDTO struct {
	Total int `json:"total"`
}

// staleTaskDTO embeds a taskDTO, inlining its fields, plus the idle duration in
// seconds for a stale task.
type staleTaskDTO struct {
	taskDTO
	IdleSeconds int64 `json:"idle_seconds"`
}

// syncDTO fixes the JSON field order for a sync report.
type syncDTO struct {
	Created       int `json:"created"`
	FastForwarded int `json:"fast_forwarded"`
	Merged        int `json:"merged"`
	Pushed        int `json:"pushed"`
	Uploaded      int `json:"uploaded"`
	Downloaded    int `json:"downloaded"`
	Rounds        int `json:"rounds"`
}

// gcDTO fixes the JSON field order for a gc report: local entries tidied, and
// the tombstoned refs pruned and failed under --prune-remote (both zero
// without it).
type gcDTO struct {
	Tidied int `json:"tidied"`
	Pruned int `json:"pruned"`
	Failed int `json:"failed"`
}

// reconcileDTO fixes the JSON field order for a reconcile report: the target
// branch, the scanned/merged/carried tallies, and one nested entry per
// scanned source branch.
type reconcileDTO struct {
	Into     string               `json:"into"`
	Scanned  int                  `json:"scanned"`
	Merged   int                  `json:"merged"`
	Carried  int                  `json:"carried"`
	Branches []reconcileBranchDTO `json:"branches"`
}

// reconcileBranchDTO is one source branch in a reconcile report: its merged
// verdict, the skip reason (empty when carried), and the full-hex ids of the
// open and in-progress tasks it carried.
type reconcileBranchDTO struct {
	Branch string   `json:"branch"`
	Merged bool     `json:"merged"`
	Reason string   `json:"reason"`
	Tasks  []string `json:"tasks"`
}

func newReconcileDTO(r ccsync.ReconcileReport) reconcileDTO {
	branches := make([]reconcileBranchDTO, len(r.Branches))
	for i, b := range r.Branches {
		ids := make([]string, len(b.Tasks))
		for j, t := range b.Tasks {
			ids[j] = string(t.ID)
		}
		branches[i] = reconcileBranchDTO{
			Branch: string(b.Branch),
			Merged: b.Merged,
			Reason: b.Reason,
			Tasks:  ids,
		}
	}
	return reconcileDTO{
		Into:     string(r.Into),
		Scanned:  r.Scanned(),
		Merged:   r.Merged(),
		Carried:  r.Carried(),
		Branches: branches,
	}
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
		out[i] = logEntryDTO{Author: string(e.Author), TS: render.RFC3339(e.TS), Text: e.Text}
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

// leanNoteLine renders the tab-separated note line:
// <short7>\t<YYYY-MM-DD of updated_at UTC>\t<tags csv|->\t<title>.
func leanNoteLine(n model.Note) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s", n.ID.Short(), dateUTC(n.UpdatedAt), csvOrDash(n.Tags), n.Title)
}

// leanDocLine renders the tab-separated doc line:
// <short7>\t<YYYY-MM-DD of updated_at UTC>\t<tags csv|->\t<title>\t<when|->.
// The trailing field carries the free-text When trigger verbatim.
func leanDocLine(d model.Doc) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", d.ID.Short(), dateUTC(d.UpdatedAt), csvOrDash(d.Tags), d.Title, orDash(d.When))
}

// leanLogLine renders the tab-separated log line:
// <short7>\t<YYYY-MM-DD of updated_at UTC>\t<tags csv|->\t<title>.
// It has the same shape as leanNoteLine — a log carries no when trigger.
func leanLogLine(l model.Log) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s", l.ID.Short(), dateUTC(l.UpdatedAt), csvOrDash(l.Tags), l.Title)
}

// leanTaskLine renders the tab-separated task line:
// <short7>\t<status>\t<P{n}>\t<assignee|->\t<title>.
func leanTaskLine(t model.Task) string {
	return fmt.Sprintf("%s\t%s\tP%d\t%s\t%s", t.ID.Short(), t.Status, t.Priority, orDash(string(t.Assignee)), t.Title)
}

// renderNoteShow renders the lean show view: the fixed-order header block,
// then the body separated by a blank line. The header carries the verify
// metadata, the supersede edges in both directions, the computed drift
// verdict, and one attachment line per attachment (with a missing-locally
// marker when the content has not been synced). The deleted header appears
// only on a tombstoned note.
func renderNoteShow(n model.Note, drift string, supersedes []model.EntityID, atts []attachmentDTO) string {
	var b strings.Builder
	header(&b, "id", string(n.ID))
	header(&b, "title", n.Title)
	header(&b, "tags", csvOrDash(n.Tags))
	header(&b, "commits", csvOrDash(render.AnchorValues(n.Anchors, model.AnchorCommit)))
	header(&b, "paths", csvOrDash(render.AnchorValues(n.Anchors, model.AnchorPath)))
	header(&b, "dirs", csvOrDash(render.AnchorValues(n.Anchors, model.AnchorDir)))
	header(&b, "branches", csvOrDash(render.AnchorValues(n.Anchors, model.AnchorBranch)))
	header(&b, "author", string(n.Author))
	header(&b, "created", render.RFC3339(n.CreatedAt))
	header(&b, "updated", render.RFC3339(n.UpdatedAt))
	header(&b, "verified_at", orDash(render.OptTimeString(n.VerifiedAt)))
	header(&b, "verified_by", orDash(string(n.VerifiedBy)))
	header(&b, "superseded_by", csvOrDash(shortIDs(n.SupersededBy)))
	header(&b, "supersedes", csvOrDash(shortIDs(supersedes)))
	header(&b, "drift", orDash(drift))
	if n.StaleAt != 0 {
		header(&b, "stale_at", orDash(render.OptTimeString(n.StaleAt)))
		header(&b, "stale_by", string(n.StaleBy))
		header(&b, "stale_reason", n.StaleReason)
	}
	attachmentHeaders(&b, atts)
	if n.Deleted {
		header(&b, "deleted", "true")
	}
	if n.Body != "" {
		b.WriteByte('\n')
		b.WriteString(n.Body)
		b.WriteByte('\n')
	}
	return b.String()
}

// attachmentHeaders writes one attachment header line per attachment:
// "<name> (<size> bytes, oid <oid7>)", with a missing-locally marker and the
// sync remediation when the content is not in the local LFS store.
func attachmentHeaders(b *strings.Builder, atts []attachmentDTO) {
	for _, a := range atts {
		line := fmt.Sprintf("%s (%d bytes, oid %s)", a.Name, a.Size, a.OID[:7])
		if !a.Present {
			line = fmt.Sprintf("%s (%d bytes, oid %s, missing locally — run `cc-notes sync`)", a.Name, a.Size, a.OID[:7])
		}
		header(b, "attachment", line)
	}
}

// renderDocShow renders the lean show view of a doc: the fixed-order header
// block, with the free-text When trigger on a "when" line right after the
// title, then the body separated by a blank line. The header carries the
// verify metadata, the supersede edges in both directions, the computed
// drift verdict, and one attachment line per attachment. The deleted header
// appears only on a tombstoned doc.
func renderDocShow(d model.Doc, drift string, supersedes []model.EntityID, atts []attachmentDTO) string {
	var b strings.Builder
	header(&b, "id", string(d.ID))
	header(&b, "title", d.Title)
	header(&b, "when", orDash(d.When))
	header(&b, "tags", csvOrDash(d.Tags))
	header(&b, "commits", csvOrDash(render.AnchorValues(d.Anchors, model.AnchorCommit)))
	header(&b, "paths", csvOrDash(render.AnchorValues(d.Anchors, model.AnchorPath)))
	header(&b, "dirs", csvOrDash(render.AnchorValues(d.Anchors, model.AnchorDir)))
	header(&b, "branches", csvOrDash(render.AnchorValues(d.Anchors, model.AnchorBranch)))
	header(&b, "author", string(d.Author))
	header(&b, "created", render.RFC3339(d.CreatedAt))
	header(&b, "updated", render.RFC3339(d.UpdatedAt))
	header(&b, "verified_at", orDash(render.OptTimeString(d.VerifiedAt)))
	header(&b, "verified_by", orDash(string(d.VerifiedBy)))
	header(&b, "superseded_by", csvOrDash(shortIDs(d.SupersededBy)))
	header(&b, "supersedes", csvOrDash(shortIDs(supersedes)))
	header(&b, "drift", orDash(drift))
	if d.StaleAt != 0 {
		header(&b, "stale_at", orDash(render.OptTimeString(d.StaleAt)))
		header(&b, "stale_by", string(d.StaleBy))
		header(&b, "stale_reason", d.StaleReason)
	}
	attachmentHeaders(&b, atts)
	if d.Deleted {
		header(&b, "deleted", "true")
	}
	if d.Body != "" {
		b.WriteByte('\n')
		b.WriteString(d.Body)
		b.WriteByte('\n')
	}
	return b.String()
}

// renderLogShow renders the lean show view of a log: the fixed-order header
// block — dropping all verify/stale/supersede/drift, which a log never carries
// — with one attachment line per attachment, then each entry as a
// "-- <author> <RFC3339>" block, the same block style task comments render in.
// The deleted header appears only on a tombstoned log.
func renderLogShow(l model.Log, atts []attachmentDTO) string {
	var b strings.Builder
	header(&b, "id", string(l.ID))
	header(&b, "title", l.Title)
	header(&b, "tags", csvOrDash(l.Tags))
	header(&b, "commits", csvOrDash(render.AnchorValues(l.Anchors, model.AnchorCommit)))
	header(&b, "paths", csvOrDash(render.AnchorValues(l.Anchors, model.AnchorPath)))
	header(&b, "dirs", csvOrDash(render.AnchorValues(l.Anchors, model.AnchorDir)))
	header(&b, "branches", csvOrDash(render.AnchorValues(l.Anchors, model.AnchorBranch)))
	header(&b, "author", string(l.Author))
	header(&b, "created", render.RFC3339(l.CreatedAt))
	header(&b, "updated", render.RFC3339(l.UpdatedAt))
	attachmentHeaders(&b, atts)
	if l.Deleted {
		header(&b, "deleted", "true")
	}
	for _, e := range l.Entries {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", e.Author, render.RFC3339(e.TS), e.Text)
	}
	return b.String()
}

// renderTaskShow renders the lean show view: the fixed-order header block
// (entity references as short ids), the description separated by a blank
// line, then each comment as a "-- <author> <RFC3339>" block.
func renderTaskShow(t model.Task, blocks []model.EntityID) string {
	var b strings.Builder
	header(&b, "id", string(t.ID))
	header(&b, "branch", string(t.Branch))
	header(&b, "title", t.Title)
	header(&b, "type", string(t.Type))
	header(&b, "status", string(t.Status))
	header(&b, "priority", fmt.Sprintf("P%d", t.Priority))
	header(&b, "assignee", orDash(string(t.Assignee)))
	header(&b, "labels", csvOrDash(t.Labels))
	header(&b, "blocked_by", csvOrDash(shortIDs(t.BlockedBy)))
	header(&b, "blocks", csvOrDash(shortIDs(blocks)))
	header(&b, "parent", orDash(shortID(t.Parent)))
	header(&b, "created", render.RFC3339(t.CreatedAt))
	header(&b, "updated", render.RFC3339(t.UpdatedAt))
	header(&b, "started", orDash(render.OptTimeString(t.StartedAt)))
	header(&b, "closed", orDash(render.OptTimeString(t.ClosedAt)))
	header(&b, "commits", csvOrDash(shortSHAs(t.Commits)))
	if t.Description != "" {
		b.WriteByte('\n')
		b.WriteString(t.Description)
		b.WriteByte('\n')
	}
	for _, c := range t.Comments {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", c.Author, render.RFC3339(c.TS), c.Body)
	}
	return b.String()
}

// renderSprintShow renders the lean show view of a sprint: the fixed-order
// header block (project as a short id), the description separated by a blank
// line, each comment as a "-- <author> <RFC3339>" block, then a tasks header
// listing the short ids of the sprint's tasks.
func renderSprintShow(s model.Sprint, tasks []model.EntityID) string {
	var b strings.Builder
	header(&b, "id", string(s.ID))
	header(&b, "project", orDash(shortID(s.Project)))
	header(&b, "title", s.Title)
	header(&b, "status", string(s.Status))
	header(&b, "start_date", orDash(render.OptTimeString(s.StartDate)))
	header(&b, "end_date", orDash(render.OptTimeString(s.EndDate)))
	header(&b, "labels", csvOrDash(s.Labels))
	header(&b, "created", render.RFC3339(s.CreatedAt))
	header(&b, "updated", render.RFC3339(s.UpdatedAt))
	header(&b, "started", orDash(render.OptTimeString(s.StartedAt)))
	header(&b, "closed", orDash(render.OptTimeString(s.ClosedAt)))
	header(&b, "commits", csvOrDash(shortSHAs(s.Commits)))
	if s.Description != "" {
		b.WriteByte('\n')
		b.WriteString(s.Description)
		b.WriteByte('\n')
	}
	for _, c := range s.Comments {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", c.Author, render.RFC3339(c.TS), c.Body)
	}
	header(&b, "tasks", csvOrDash(shortIDs(tasks)))
	return b.String()
}

// renderProjectShow renders the lean show view of a project: the fixed-order
// header block, the description separated by a blank line, each comment as a
// "-- <author> <RFC3339>" block, then sprints and tasks headers listing the
// short ids of the project's sprints and tasks.
func renderProjectShow(p model.Project, sprints, tasks []model.EntityID) string {
	var b strings.Builder
	header(&b, "id", string(p.ID))
	header(&b, "title", p.Title)
	header(&b, "status", string(p.Status))
	header(&b, "labels", csvOrDash(p.Labels))
	header(&b, "created", render.RFC3339(p.CreatedAt))
	header(&b, "updated", render.RFC3339(p.UpdatedAt))
	header(&b, "closed", orDash(render.OptTimeString(p.ClosedAt)))
	header(&b, "commits", csvOrDash(shortSHAs(p.Commits)))
	if p.Description != "" {
		b.WriteByte('\n')
		b.WriteString(p.Description)
		b.WriteByte('\n')
	}
	for _, c := range p.Comments {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", c.Author, render.RFC3339(c.TS), c.Body)
	}
	header(&b, "sprints", csvOrDash(shortIDs(sprints)))
	header(&b, "tasks", csvOrDash(shortIDs(tasks)))
	return b.String()
}

// leanSprintLine renders the tab-separated sprint line:
// <short7>\t<status>\t<title>.
func leanSprintLine(s model.Sprint) string {
	return fmt.Sprintf("%s\t%s\t%s", s.ID.Short(), s.Status, s.Title)
}

// leanProjectLine renders the tab-separated project line:
// <short7>\t<status>\t<title>.
func leanProjectLine(p model.Project) string {
	return fmt.Sprintf("%s\t%s\t%s", p.ID.Short(), p.Status, p.Title)
}

// runbookStepPending is the display status of a run step with no recorded
// result: pending is absence, not a StepResultStatus value.
const runbookStepPending = "pending"

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

// leanRunbookLine renders the tab-separated runbook line:
// <short7>\t<status>\t<title>.
func leanRunbookLine(rb model.Runbook) string {
	return fmt.Sprintf("%s\t%s\t%s", rb.ID.Short(), rb.Status, rb.Title)
}

// leanRunLine renders the tab-separated run line:
// <short7>\t<status>\t<runner>\t<YYYY-MM-DD started>\t<done+skipped>/<total steps>.
func leanRunLine(rb model.Runbook, run model.RunbookRun) string {
	done, _, _ := runStepCounts(rb, run)
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%d/%d", render.ShortWireID(run.ID), run.Status, run.Runner, dateUTC(run.StartedAt), done, len(rb.Steps))
}

// runStepCounts tallies a run's results over the runbook's current steps: the
// number done-or-skipped (progress), the number skipped, and the number
// failed. Results for removed steps are excluded so the tallies never exceed
// the current step count.
func runStepCounts(rb model.Runbook, run model.RunbookRun) (progress, skipped, failed int) {
	byStep := make(map[string]model.RunbookStepResult, len(run.Results))
	for _, r := range run.Results {
		byStep[r.StepID] = r
	}
	for _, st := range rb.Steps {
		switch byStep[st.ID].Status {
		case model.StepDone:
			progress++
		case model.StepSkipped:
			progress++
			skipped++
		case model.StepFailed:
			failed++
		}
	}
	return progress, skipped, failed
}

// renderRunbookShow renders the lean show view of a runbook: the fixed-order
// header block, the description separated by a blank line, the numbered steps
// (each with an indented "$ command" line when set), then the runs newest
// first, capped at five with a "(+N older)" trailer.
func renderRunbookShow(rb model.Runbook) string {
	var b strings.Builder
	header(&b, "id", string(rb.ID))
	header(&b, "title", rb.Title)
	header(&b, "status", string(rb.Status))
	header(&b, "labels", csvOrDash(rb.Labels))
	header(&b, "created", render.RFC3339(rb.CreatedAt))
	header(&b, "updated", render.RFC3339(rb.UpdatedAt))
	header(&b, "archived", orDash(render.OptTimeString(rb.ArchivedAt)))
	if rb.Description != "" {
		b.WriteByte('\n')
		b.WriteString(rb.Description)
		b.WriteByte('\n')
	}
	for _, c := range rb.Comments {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", c.Author, render.RFC3339(c.TS), c.Body)
	}
	b.WriteString("\nsteps:\n")
	for i, st := range rb.Steps {
		fmt.Fprintf(&b, "  %d. [%s] %s\n", i+1, render.ShortWireID(st.ID), st.Text)
		if st.Command != "" {
			fmt.Fprintf(&b, "     $ %s\n", st.Command)
		}
	}
	b.WriteString("\nruns:\n")
	const runCap = 5
	runs := make([]model.RunbookRun, len(rb.Runs))
	for i, r := range rb.Runs {
		runs[len(rb.Runs)-1-i] = r
	}
	older := 0
	if len(runs) > runCap {
		older = len(runs) - runCap
		runs = runs[:runCap]
	}
	for _, r := range runs {
		fmt.Fprintln(&b, runbookRunSummaryLine(rb, r))
	}
	if older > 0 {
		fmt.Fprintf(&b, "  (+%d older — use run list)\n", older)
	}
	return b.String()
}

// runbookRunSummaryLine renders one run's summary line under the runs header of
// renderRunbookShow.
func runbookRunSummaryLine(rb model.Runbook, run model.RunbookRun) string {
	progress, skipped, failed := runStepCounts(rb, run)
	done := progress - skipped
	finished := "running"
	if run.FinishedAt != 0 {
		finished = render.RFC3339(run.FinishedAt)
	}
	return fmt.Sprintf("-- %s %s by %s %s → %s (%d done, %d skipped, %d failed / %d) task %s",
		render.ShortWireID(run.ID), run.Status, run.Runner, render.RFC3339(run.StartedAt), finished, done, skipped, failed, len(rb.Steps), orDash(shortID(run.Task)))
}

// renderRunShow renders one run: the fixed-order run header, then one line per
// current step in runbook order carrying the step's status ("pending" when no
// result) and, indented, the recorded note when set.
func renderRunShow(rb model.Runbook, run model.RunbookRun) string {
	var b strings.Builder
	header(&b, "run", run.ID)
	header(&b, "runbook", string(rb.ID))
	header(&b, "status", string(run.Status))
	header(&b, "runner", string(run.Runner))
	header(&b, "started", render.RFC3339(run.StartedAt))
	header(&b, "finished", orDash(render.OptTimeString(run.FinishedAt)))
	header(&b, "task", orDash(shortID(run.Task)))
	byStep := make(map[string]model.RunbookStepResult, len(run.Results))
	for _, r := range run.Results {
		byStep[r.StepID] = r
	}
	b.WriteString("\nsteps:\n")
	for _, st := range rb.Steps {
		status, note := runbookStepPending, ""
		if res, ok := byStep[st.ID]; ok {
			status = string(res.Status)
			note = res.Note
		}
		fmt.Fprintf(&b, "  %s %s %s\n", render.ShortWireID(st.ID), status, st.Text)
		if note != "" {
			fmt.Fprintf(&b, "     note: %s\n", note)
		}
	}
	return b.String()
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

func header(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

func dateUTC(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func csvOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ",")
}

func shortID(id model.EntityID) string {
	if id == "" {
		return ""
	}
	return id.Short()
}

func shortIDs(ids []model.EntityID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.Short()
	}
	return out
}

func shortSHAs(shas []model.SHA) []string {
	out := make([]string, len(shas))
	for i, s := range shas {
		out[i] = string(s)[:7]
	}
	return out
}
