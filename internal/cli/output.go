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

// taskDTO fixes the JSON field order and formats for task output: full hex
// ids, RFC3339 UTC timestamps, null for unset optionals, sorted set slices,
// and the derived blocks reverse index.
//
// TODO(P2/P3): the committed task JSON shape also lists "lease" and "commits";
// they land with note hygiene and coordination, not in P1.
type taskDTO struct {
	ID          string       `json:"id"`
	Branch      string       `json:"branch"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Type        string       `json:"type"`
	Status      string       `json:"status"`
	Priority    int          `json:"priority"`
	Assignee    *string      `json:"assignee"`
	Labels      []string     `json:"labels"`
	BlockedBy   []string     `json:"blocked_by"`
	Blocks      []string     `json:"blocks"`
	Parent      *string      `json:"parent"`
	Comments    []commentDTO `json:"comments"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	StartedAt   *string      `json:"started_at"`
	ClosedAt    *string      `json:"closed_at"`
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
	comments := make([]commentDTO, len(t.Comments))
	for i, c := range t.Comments {
		comments[i] = commentDTO{Author: string(c.Author), TS: rfc3339(c.TS), Body: c.Body}
	}
	return taskDTO{
		ID:          string(t.ID),
		Branch:      string(t.Branch),
		Title:       t.Title,
		Description: t.Description,
		Type:        string(t.Type),
		Status:      string(t.Status),
		Priority:    int(t.Priority),
		Assignee:    optString(string(t.Assignee)),
		Labels:      emptyNotNil(t.Labels),
		BlockedBy:   idStrings(t.BlockedBy),
		Blocks:      idStrings(blocks),
		Parent:      optString(string(t.Parent)),
		Comments:    comments,
		CreatedAt:   rfc3339(t.CreatedAt),
		UpdatedAt:   rfc3339(t.UpdatedAt),
		StartedAt:   optTime(t.StartedAt),
		ClosedAt:    optTime(t.ClosedAt),
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
