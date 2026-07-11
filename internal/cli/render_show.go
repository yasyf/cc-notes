package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
)

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
