package cli

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// relevantDTO is one ranked entity in the JSON output of relevant: a kind
// discriminator ("note"|"doc"|"log"|"runbook"|"investigation"|"plan"|"answer"),
// the matching summary DTO — the doc summary carrying the free-text trigger, the
// answer summary carrying its answer body, notes, docs, and answers carrying the
// drift verdict, logs, runbooks and plans never drifting — the summed relevance
// score, and the matched reasons in fixed priority order. The entity fields are
// mutually exclusive; the unused ones are omitted so the float hook can index
// entry["note"]/entry["doc"]/entry["log"]/entry["runbook"]/
// entry["investigation"]/entry["plan"]/entry["answer"] by kind. Other bodies
// stay with the per-kind show verbs.
type relevantDTO struct {
	Kind          string                   `json:"kind"`
	Note          *noteSummaryDTO          `json:"note,omitempty"`
	Doc           *docSummaryDTO           `json:"doc,omitempty"`
	Log           *logSummaryDTO           `json:"log,omitempty"`
	Runbook       *runbookSummaryDTO       `json:"runbook,omitempty"`
	Ledger        *ledgerSummaryDTO        `json:"ledger,omitempty"`
	Investigation *investigationSummaryDTO `json:"investigation,omitempty"`
	Plan          *planSummaryDTO          `json:"plan,omitempty"`
	Answer        *answerSummaryDTO        `json:"answer,omitempty"`
	Score         int                      `json:"score"`
	Reasons       []string                 `json:"reasons"`
}

func newRelevantCmd() *cobra.Command {
	var branchFlag, baseFlag string
	var limit int
	var jsonOut, attached, worktree bool
	cmd := &cobra.Command{
		Use:   "relevant PATH",
		Short: "Surface the notes, docs, logs, runbooks, investigations, plans, and answers most relevant to a path, ranked with reasons",
		Long: "Surface the notes, docs, logs, runbooks, investigations, plans, and answers most relevant\n" +
			"to PATH, ranked by accumulated signal with the matched reasons shown. Notes, docs, and\n" +
			"answers carry a drift verdict against HEAD; logs, runbooks, investigations, and plans\n" +
			"never drift.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if branchFlag != "" {
				if err := s.Git.CheckRefFormat(ctx, branchFlag); err != nil {
					return &UsageError{Err: err}
				}
			}
			filter := notes.RelevantFilter{Branch: branchFlag, Base: baseFlag, Attached: attached, Worktree: worktree}
			variant := fmt.Sprintf("json=%t limit=%d", jsonOut, limit)
			out, err := c.RelevantCached(ctx, args[0], filter, variant, func(entries []notes.RelevantEntry) ([]byte, error) {
				if limit > 0 && len(entries) > limit {
					entries = entries[:limit]
				}
				var buf bytes.Buffer
				err := printRelevant(&buf, entries, jsonOut)
				return buf.Bytes(), err
			})
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&branchFlag, "branch", "", "branch to weigh against (default: current HEAD branch)")
	flags.StringVar(&baseFlag, "base", "", "merge-base reference for cross-author signals (default: remote default branch)")
	bindLimit(flags, &limit, 10)
	bindJSON(flags, &jsonOut)
	flags.BoolVar(&attached, "attached", false, "keep only entities anchored to the path or a parent directory")
	flags.BoolVar(&worktree, "worktree", false, "drift-check path anchors against uncommitted working-tree edits")
	return cmd
}

// printRelevant writes the ranked entries as relevantDTOs in JSON, or as lean
// lines with the matched reasons (and any drift verdict) appended after tabs.
// A doc line additionally carries a bracketed verdict flag and a "doc show
// <short-id>" hint, and never the long body. Each entry carries its own drift
// verdict; a log never drifts, so its verdict is empty.
func printRelevant(out io.Writer, entries []notes.RelevantEntry, jsonOut bool) error {
	if jsonOut {
		dtos := make([]relevantDTO, len(entries))
		for i, e := range entries {
			dto := relevantDTO{Kind: string(e.Kind), Score: e.Score, Reasons: e.Reasons}
			switch e.Kind {
			case model.KindDoc:
				d := newDocSummaryDTO(e.Doc, string(e.Verdict))
				dto.Doc = &d
			case model.KindLog:
				l := newLogSummaryDTO(e.Log)
				dto.Log = &l
			case model.KindRunbook:
				rb := newRunbookSummaryDTO(e.Runbook)
				dto.Runbook = &rb
			case model.KindLedger:
				l := newLedgerSummaryDTO(e.Ledger)
				dto.Ledger = &l
			case model.KindInvestigation:
				inv := newInvestigationSummaryDTO(e.Investigation)
				dto.Investigation = &inv
			case model.KindPlan:
				p := newPlanSummaryDTO(e.Plan)
				dto.Plan = &p
			case model.KindAnswer:
				a := newAnswerSummaryDTO(e.Answer, string(e.Verdict))
				dto.Answer = &a
			default:
				n := newNoteSummaryDTO(e.Note, string(e.Verdict))
				dto.Note = &n
			}
			dtos[i] = dto
		}
		return printJSON(out, dtos)
	}
	for _, e := range entries {
		var line string
		switch e.Kind {
		case model.KindDoc:
			line = leanDocLine(e.Doc) + "\t" + csvOrDash(e.Reasons)
			if e.Verdict != "" {
				line += "\t" + verdictFlag(string(e.Verdict))
			}
			line += "\tdoc show " + e.Doc.ID.Short()
		case model.KindLog:
			line = leanLogLine(e.Log) + "\t" + csvOrDash(e.Reasons) + "\tlog show " + e.Log.ID.Short()
		case model.KindRunbook:
			line = leanRunbookLine(e.Runbook) + "\t" + csvOrDash(e.Reasons) + "\trunbook show " + e.Runbook.ID.Short()
		case model.KindLedger:
			line = leanLedgerLine(e.Ledger) + "\t" + csvOrDash(e.Reasons) + "\tledger show " + e.Ledger.ID.Short()
		case model.KindInvestigation:
			line = leanInvestigationLine(e.Investigation) + "\t" + csvOrDash(e.Reasons) + "\tinvestigation show " + e.Investigation.ID.Short()
		case model.KindPlan:
			line = leanPlanLine(e.Plan) + "\t" + csvOrDash(e.Reasons) + "\tplan show " + e.Plan.ID.Short()
		case model.KindAnswer:
			line = leanAnswerLine(e.Answer) + "\t" + csvOrDash(e.Reasons)
			if e.Verdict != "" {
				line += "\t" + verdictFlag(string(e.Verdict))
			}
			line += "\tanswer show " + e.Answer.ID.Short()
		default:
			line = leanNoteLine(e.Note) + "\t" + csvOrDash(e.Reasons)
			if e.Verdict != "" {
				line += "\t" + string(e.Verdict)
			}
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

// verdictFlag renders a non-empty verdict as a lowercase bracketed flag, e.g.
// STALE -> "[stale]", so an out-of-date doc surfaces flagged when floated.
func verdictFlag(verdict string) string {
	return "[" + strings.ToLower(verdict) + "]"
}

// hasAnchorIn reports whether anchors contains an anchor of the given kind and
// value.
func hasAnchorIn(anchors []model.Anchor, kind model.AnchorKind, value string) bool {
	return slices.Contains(anchors, model.Anchor{Kind: kind, Value: value})
}
