package cli

import (
	"fmt"
	"strings"

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

// leanAnswerLine renders the tab-separated answer line:
// <short7>\t<YYYY-MM-DD of updated_at UTC>\t<tags csv|->\t<question>\t<answer|->.
// The answer field is the body's first line, the chosen answer itself.
func leanAnswerLine(a model.Answer) string {
	answer, _, _ := strings.Cut(a.Body, "\n")
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", a.ID.Short(), dateUTC(a.UpdatedAt), csvOrDash(a.Tags), a.Title, orDash(answer))
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

// leanPlanLine renders the tab-separated plan line:
// <short7>\t<status>\t<title>.
func leanPlanLine(p model.Plan) string {
	return fmt.Sprintf("%s\t%s\t%s", p.ID.Short(), p.Status, p.Title)
}

// leanRunLine renders the tab-separated run line:
// <short7>\t<status>\t<runner>\t<YYYY-MM-DD started>\t<done+skipped>/<total steps>.
func leanRunLine(rb model.Runbook, run model.RunbookRun) string {
	done, _, _ := runStepCounts(rb, run)
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%d/%d", render.ShortWireID(run.ID), run.Status, run.Runner, dateUTC(run.StartedAt), done, len(rb.Steps))
}

// leanLedgerLine renders the tab-separated ledger line:
// <short7>\t<status>\t<rows>\t<title>.
func leanLedgerLine(l model.Ledger) string {
	return fmt.Sprintf("%s\t%s\t%d\t%s", l.ID.Short(), l.Status, len(l.Rows), l.Title)
}

// leanRowLine renders the tab-separated row line: the key, then one cell per
// effective column in order.
func leanRowLine(l model.Ledger, row model.LedgerRow) string {
	columns := model.LedgerColumns(l)
	cells := make([]string, 0, 1+len(columns))
	cells = append(cells, row.Key)
	for _, c := range columns {
		cells = append(cells, row.Fields[c])
	}
	return strings.Join(cells, "\t")
}
