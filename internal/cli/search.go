package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// searchDTO is one merged hit of the top-level search: a kind discriminator
// plus the matching entity's summary DTO, mutually exclusive like relevantDTO's
// fields.
type searchDTO struct {
	Kind          string                   `json:"kind"`
	Note          *noteSummaryDTO          `json:"note,omitempty"`
	Doc           *docSummaryDTO           `json:"doc,omitempty"`
	Log           *logSummaryDTO           `json:"log,omitempty"`
	Task          *taskSummaryDTO          `json:"task,omitempty"`
	Runbook       *runbookSummaryDTO       `json:"runbook,omitempty"`
	Ledger        *ledgerSummaryDTO        `json:"ledger,omitempty"`
	Investigation *investigationSummaryDTO `json:"investigation,omitempty"`
	Plan          *planSummaryDTO          `json:"plan,omitempty"`
	Answer        *answerSummaryDTO        `json:"answer,omitempty"`
}

// newSearchCmd builds the top-level "cc-notes search QUERY": one ranked search
// fanned out across the record kinds, merged kind-tagged. Like show and history
// it is global because a query needs no noun; the noun-scoped "<kind> search"
// commands remain for a single-kind search with that kind's full filter set
// (e.g. --author).
func newSearchCmd() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across every note, doc, log, task, runbook, investigation, plan, and answer",
		Args:  minArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			// SearchFilter's 0 means zero results; the CLI's "0 = all" maps to
			// its negative no-cap form.
			limitAll := limit
			if limit == 0 {
				limitAll = -1
			}
			hits, err := c.Search(cmd.Context(), strings.Join(args, " "), notes.SearchFilter{
				Labels:  labels,
				Anchors: anchorFiltersToNotes(filters),
				Limit:   limitAll,
			})
			if err != nil {
				return err
			}
			return printSearchHits(cmd, c, hits, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	bindLimit(flags, &limit, 20)
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func printSearchHits(cmd *cobra.Command, c *notes.Client, hits []model.Snapshot, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		var ids []model.EntityID
		for _, hit := range hits {
			if task, ok := hit.(model.Task); ok {
				ids = append(ids, task.ID)
			}
		}
		blocks, err := c.TasksBlockingFor(cmd.Context(), ids)
		if err != nil {
			return err
		}
		dtos := make([]searchDTO, len(hits))
		for i, h := range hits {
			dto := searchDTO{Kind: string(h.Meta().Kind)}
			switch v := h.(type) {
			case model.Note:
				n := newNoteSummaryDTO(v, "")
				dto.Note = &n
			case model.Doc:
				d := newDocSummaryDTO(v, "")
				dto.Doc = &d
			case model.Log:
				l := newLogSummaryDTO(v)
				dto.Log = &l
			case model.Task:
				t := newTaskSummaryDTO(v, blocks[v.ID])
				dto.Task = &t
			case model.Runbook:
				rb := newRunbookSummaryDTO(v)
				dto.Runbook = &rb
			case model.Ledger:
				l := newLedgerSummaryDTO(v)
				dto.Ledger = &l
			case model.Investigation:
				inv := newInvestigationSummaryDTO(v)
				dto.Investigation = &inv
			case model.Plan:
				p := newPlanSummaryDTO(v)
				dto.Plan = &p
			case model.Answer:
				a := newAnswerSummaryDTO(v, "")
				dto.Answer = &a
			default:
				panic(fmt.Sprintf("Search returned unknown snapshot %T", h))
			}
			dtos[i] = dto
		}
		return printJSON(out, dtos)
	}
	for _, h := range hits {
		var lean string
		switch v := h.(type) {
		case model.Note:
			lean = leanNoteLine(v)
		case model.Doc:
			lean = leanDocLine(v)
		case model.Log:
			lean = leanLogLine(v)
		case model.Task:
			lean = leanTaskLine(v)
		case model.Runbook:
			lean = leanRunbookLine(v)
		case model.Ledger:
			lean = leanLedgerLine(v)
		case model.Investigation:
			lean = leanInvestigationLine(v)
		case model.Plan:
			lean = leanPlanLine(v)
		case model.Answer:
			lean = leanAnswerLine(v)
		default:
			panic(fmt.Sprintf("Search returned unknown snapshot %T", h))
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\n", h.Meta().Kind, lean); err != nil {
			return err
		}
	}
	return nil
}
