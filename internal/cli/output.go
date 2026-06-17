package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/model"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
)

// anchorDTO is one note anchor with its content witness rendered as the git
// object id at verify time, or null when the anchor carries no witness.
type anchorDTO struct {
	Kind    string  `json:"kind"`
	Value   string  `json:"value"`
	Witness *string `json:"witness"`
}

// noteDTO fixes the JSON field order and formats for note output: full hex
// id, RFC3339 UTC timestamps, sorted set slices, per-anchor witnesses, the
// verify metadata (null when never verified), the single replacement id (null
// when not superseded), and the computed drift verdict (null when fresh).
type noteDTO struct {
	ID           string      `json:"id"`
	Title        string      `json:"title"`
	Body         string      `json:"body"`
	Tags         []string    `json:"tags"`
	Anchors      []anchorDTO `json:"anchors"`
	Author       string      `json:"author"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
	VerifiedAt   *string     `json:"verified_at"`
	VerifiedBy   *string     `json:"verified_by"`
	SupersededBy *string     `json:"superseded_by"`
	Drift        *string     `json:"drift"`
	Deleted      bool        `json:"deleted"`
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

// statusDTO fixes the JSON field order for a status report: the current
// branch, the backlog and your-branch task slices, the in-progress tasks
// grouped by assignee, and the note summary.
type statusDTO struct {
	Branch     string              `json:"branch"`
	Backlog    []taskDTO           `json:"backlog"`
	YourBranch []taskDTO           `json:"your_branch"`
	InProgress []statusAssigneeDTO `json:"in_progress"`
	Notes      statusNotesDTO      `json:"notes"`
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

func newNoteDTO(n model.Note, drift string) noteDTO {
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
		Tags:         emptyNotNil(n.Tags),
		Anchors:      anchors,
		Author:       string(n.Author),
		CreatedAt:    rfc3339(n.CreatedAt),
		UpdatedAt:    rfc3339(n.UpdatedAt),
		VerifiedAt:   optTime(n.VerifiedAt),
		VerifiedBy:   optString(string(n.VerifiedBy)),
		SupersededBy: superseded,
		Drift:        optString(drift),
		Deleted:      n.Deleted,
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
		Assignee:     optString(string(t.Assignee)),
		Labels:       emptyNotNil(t.Labels),
		BlockedBy:    idStrings(t.BlockedBy),
		Blocks:       idStrings(blocks),
		Parent:       optString(string(t.Parent)),
		Comments:     commentDTOs(t.Comments),
		Commits:      shaStrings(t.Commits),
		Lease:        leaseDTO{Holder: optString(string(t.Assignee)), Heartbeat: optTime(t.HeartbeatAt)},
		CreatedAt:    rfc3339(t.CreatedAt),
		UpdatedAt:    rfc3339(t.UpdatedAt),
		StartedAt:    optTime(t.StartedAt),
		ClosedAt:     optTime(t.ClosedAt),
		Sprint:       optString(string(t.Sprint)),
		Project:      optString(string(t.Project)),
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
		out[i] = commentDTO{Author: string(c.Author), TS: rfc3339(c.TS), Body: c.Body}
	}
	return out
}

// newSprintDTO renders a sprint snapshot plus its reverse-index task ids into
// its fixed-order DTO.
func newSprintDTO(s model.Sprint, tasks []model.EntityID) sprintDTO {
	return sprintDTO{
		ID:          string(s.ID),
		Project:     optString(string(s.Project)),
		Title:       s.Title,
		Description: s.Description,
		Status:      string(s.Status),
		StartDate:   optTime(s.StartDate),
		EndDate:     optTime(s.EndDate),
		Labels:      emptyNotNil(s.Labels),
		Commits:     shaStrings(s.Commits),
		Comments:    commentDTOs(s.Comments),
		Author:      string(s.Author),
		CreatedAt:   rfc3339(s.CreatedAt),
		UpdatedAt:   rfc3339(s.UpdatedAt),
		StartedAt:   optTime(s.StartedAt),
		ClosedAt:    optTime(s.ClosedAt),
		Tasks:       idStrings(tasks),
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
		Labels:      emptyNotNil(p.Labels),
		Commits:     shaStrings(p.Commits),
		Comments:    commentDTOs(p.Comments),
		Author:      string(p.Author),
		CreatedAt:   rfc3339(p.CreatedAt),
		UpdatedAt:   rfc3339(p.UpdatedAt),
		ClosedAt:    optTime(p.ClosedAt),
		Sprints:     idStrings(sprints),
		Tasks:       idStrings(tasks),
	}
}

// leanNoteLine renders the tab-separated note line:
// <short7>\t<YYYY-MM-DD of updated_at UTC>\t<tags csv|->\t<title>.
func leanNoteLine(n model.Note) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s", n.ID.Short(), dateUTC(n.UpdatedAt), csvOrDash(n.Tags), n.Title)
}

// leanTaskLine renders the tab-separated task line:
// <short7>\t<status>\t<P{n}>\t<assignee|->\t<title>.
func leanTaskLine(t model.Task) string {
	return fmt.Sprintf("%s\t%s\tP%d\t%s\t%s", t.ID.Short(), t.Status, t.Priority, orDash(string(t.Assignee)), t.Title)
}

// renderNoteShow renders the lean show view: the fixed-order header block,
// then the body separated by a blank line. The header carries the verify
// metadata, the supersede edges in both directions, and the computed drift
// verdict. The deleted header appears only on a tombstoned note.
func renderNoteShow(n model.Note, drift string, supersedes []model.EntityID) string {
	var b strings.Builder
	header(&b, "id", string(n.ID))
	header(&b, "title", n.Title)
	header(&b, "tags", csvOrDash(n.Tags))
	header(&b, "commits", csvOrDash(anchorValues(n.Anchors, model.AnchorCommit)))
	header(&b, "paths", csvOrDash(anchorValues(n.Anchors, model.AnchorPath)))
	header(&b, "branches", csvOrDash(anchorValues(n.Anchors, model.AnchorBranch)))
	header(&b, "author", string(n.Author))
	header(&b, "created", rfc3339(n.CreatedAt))
	header(&b, "updated", rfc3339(n.UpdatedAt))
	header(&b, "verified_at", orDash(optTimeString(n.VerifiedAt)))
	header(&b, "verified_by", orDash(string(n.VerifiedBy)))
	header(&b, "superseded_by", csvOrDash(shortIDs(n.SupersededBy)))
	header(&b, "supersedes", csvOrDash(shortIDs(supersedes)))
	header(&b, "drift", orDash(drift))
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
	header(&b, "created", rfc3339(t.CreatedAt))
	header(&b, "updated", rfc3339(t.UpdatedAt))
	header(&b, "started", orDash(optTimeString(t.StartedAt)))
	header(&b, "closed", orDash(optTimeString(t.ClosedAt)))
	header(&b, "commits", csvOrDash(shortSHAs(t.Commits)))
	if t.Description != "" {
		b.WriteByte('\n')
		b.WriteString(t.Description)
		b.WriteByte('\n')
	}
	for _, c := range t.Comments {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", c.Author, rfc3339(c.TS), c.Body)
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
	header(&b, "start_date", orDash(optTimeString(s.StartDate)))
	header(&b, "end_date", orDash(optTimeString(s.EndDate)))
	header(&b, "labels", csvOrDash(s.Labels))
	header(&b, "created", rfc3339(s.CreatedAt))
	header(&b, "updated", rfc3339(s.UpdatedAt))
	header(&b, "started", orDash(optTimeString(s.StartedAt)))
	header(&b, "closed", orDash(optTimeString(s.ClosedAt)))
	header(&b, "commits", csvOrDash(shortSHAs(s.Commits)))
	if s.Description != "" {
		b.WriteByte('\n')
		b.WriteString(s.Description)
		b.WriteByte('\n')
	}
	for _, c := range s.Comments {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", c.Author, rfc3339(c.TS), c.Body)
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
	header(&b, "created", rfc3339(p.CreatedAt))
	header(&b, "updated", rfc3339(p.UpdatedAt))
	header(&b, "closed", orDash(optTimeString(p.ClosedAt)))
	header(&b, "commits", csvOrDash(shortSHAs(p.Commits)))
	if p.Description != "" {
		b.WriteByte('\n')
		b.WriteString(p.Description)
		b.WriteByte('\n')
	}
	for _, c := range p.Comments {
		fmt.Fprintf(&b, "\n-- %s %s\n%s\n", c.Author, rfc3339(c.TS), c.Body)
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

func rfc3339(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) }

func dateUTC(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") }

func optTime(ts int64) *string {
	if ts == 0 {
		return nil
	}
	s := rfc3339(ts)
	return &s
}

func optTimeString(ts int64) string {
	if ts == 0 {
		return ""
	}
	return rfc3339(ts)
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

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

func idStrings(ids []model.EntityID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func shaStrings(shas []model.SHA) []string {
	out := make([]string, 0, len(shas))
	for _, s := range shas {
		out = append(out, string(s))
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

func emptyNotNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

func anchorValues(anchors []model.Anchor, kind model.AnchorKind) []string {
	var values []string
	for _, a := range anchors {
		if a.Kind == kind {
			values = append(values, a.Value)
		}
	}
	return values
}
