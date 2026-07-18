package cli

import (
	"fmt"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
)

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

// leanRunbookLine renders the tab-separated runbook line:
// <short7>\t<status>\t<title>.
func leanRunbookLine(rb model.Runbook) string {
	return fmt.Sprintf("%s\t%s\t%s", rb.ID.Short(), rb.Status, rb.Title)
}

// leanInvestigationLine renders the tab-separated investigation line:
// <short7>\t<status>\t<title>.
func leanInvestigationLine(inv model.Investigation) string {
	return fmt.Sprintf("%s\t%s\t%s", inv.ID.Short(), inv.Status, inv.Title)
}

// leanRunLine renders the tab-separated run line:
// <short7>\t<status>\t<runner>\t<YYYY-MM-DD started>\t<done+skipped>/<total steps>.
func leanRunLine(rb model.Runbook, run model.RunbookRun) string {
	done, _, _ := runStepCounts(rb, run)
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%d/%d", render.ShortWireID(run.ID), run.Status, run.Runner, dateUTC(run.StartedAt), done, len(rb.Steps))
}
