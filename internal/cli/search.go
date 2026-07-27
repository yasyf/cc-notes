package cli

import (
	"cmp"
	"context"
	"fmt"
	"slices"
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
	Investigation *investigationSummaryDTO `json:"investigation,omitempty"`
}

// searchHit pairs one matched entity with its kind's own rank tier, so the
// per-kind result lists merge into a single consistently ordered set.
type searchHit struct {
	snap model.Snapshot
	tier int
}

// newSearchCmd builds the top-level "cc-notes search QUERY": one ranked search
// fanned out across every kind, merged kind-tagged. Like show and history it is
// global because a query needs no noun; the noun-scoped "<kind> search"
// commands remain for a single-kind search with that kind's full filter set
// (e.g. --author).
func newSearchCmd() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across every note, doc, log, task, runbook, and investigation",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			// SearchFilter's 0 means zero results; the CLI's "0 = all" maps to
			// its negative no-cap form.
			kindLimit := limit
			if limit == 0 {
				kindLimit = -1
			}
			hits, err := searchAllKinds(cmd.Context(), c, args[0], notes.SearchFilter{
				Labels:  labels,
				Anchors: anchorFiltersToNotes(filters),
				Limit:   kindLimit,
			})
			if err != nil {
				return err
			}
			if limit > 0 && len(hits) > limit {
				hits = hits[:limit]
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

// searchAllKinds fans query out to each kind's ranked search and merges the
// results under the per-kind comparator (tier descending, UpdatedAt descending,
// id ascending), so the interleave preserves each kind's own order. The tier is
// re-derived with textTier over the same fields each kind's ranker reads.
func searchAllKinds(ctx context.Context, c *notes.Client, query string, f notes.SearchFilter) ([]searchHit, error) {
	q := strings.ToLower(query)
	var hits []searchHit

	ns, err := c.SearchNotes(ctx, query, f)
	if err != nil {
		return nil, err
	}
	for _, n := range ns {
		hits = append(hits, searchHit{snap: n, tier: textTier(n.Title, n.Tags, []string{n.Body}, q)})
	}

	docs, err := c.SearchDocs(ctx, query, f)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		hits = append(hits, searchHit{snap: d, tier: textTier(d.Title, d.Tags, []string{d.Body, d.When}, q)})
	}

	tasks, err := c.SearchTasks(ctx, query, f)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		hits = append(hits, searchHit{snap: t, tier: textTier(t.Title, t.Labels, []string{t.Description}, q)})
	}

	logs, err := c.SearchLogs(ctx, query, f)
	if err != nil {
		return nil, err
	}
	for _, l := range logs {
		entries := make([]string, len(l.Entries))
		for i, e := range l.Entries {
			entries[i] = e.Text
		}
		hits = append(hits, searchHit{snap: l, tier: textTier(l.Title, l.Tags, entries, q)})
	}

	runbooks, err := c.SearchRunbooks(ctx, query, f)
	if err != nil {
		return nil, err
	}
	for _, rb := range runbooks {
		bodies := make([]string, 0, 1+len(rb.Steps))
		bodies = append(bodies, rb.Description)
		for _, st := range rb.Steps {
			bodies = append(bodies, st.Text)
		}
		hits = append(hits, searchHit{snap: rb, tier: textTier(rb.Title, rb.Labels, bodies, q)})
	}

	invs, err := c.SearchInvestigations(ctx, query, f)
	if err != nil {
		return nil, err
	}
	for _, inv := range invs {
		hits = append(hits, searchHit{snap: inv, tier: textTier(inv.Title, inv.Tags, investigationSearchBodies(inv), q)})
	}

	slices.SortFunc(hits, compareSearchHits)
	return hits, nil
}

func compareSearchHits(a, b searchHit) int {
	if c := cmp.Compare(b.tier, a.tier); c != 0 {
		return c
	}
	if c := b.snap.Meta().UpdatedAt.Compare(a.snap.Meta().UpdatedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.snap.EntityID(), b.snap.EntityID())
}

// printSearchHits writes the merged hits as searchDTOs in JSON, or as each
// kind's lean line prefixed with a kind tag column. The JSON path folds the
// reverse dependency index once, for every task hit at a time.
func printSearchHits(cmd *cobra.Command, c *notes.Client, hits []searchHit, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		blocks, err := c.TasksBlockingIndex(cmd.Context())
		if err != nil {
			return err
		}
		dtos := make([]searchDTO, len(hits))
		for i, h := range hits {
			dto := searchDTO{Kind: string(h.snap.Meta().Kind)}
			switch v := h.snap.(type) {
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
			case model.Investigation:
				inv := newInvestigationSummaryDTO(v)
				dto.Investigation = &inv
			default:
				panic(fmt.Sprintf("searchAllKinds returned unknown snapshot %T", h.snap))
			}
			dtos[i] = dto
		}
		return printJSON(out, dtos)
	}
	for _, h := range hits {
		var lean string
		switch v := h.snap.(type) {
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
		case model.Investigation:
			lean = leanInvestigationLine(v)
		default:
			panic(fmt.Sprintf("searchAllKinds returned unknown snapshot %T", h.snap))
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\n", h.snap.Meta().Kind, lean); err != nil {
			return err
		}
	}
	return nil
}

func investigationSearchBodies(inv model.Investigation) []string {
	bodies := make([]string, 0, 3+len(inv.Entries)+len(inv.Findings))
	bodies = append(bodies, inv.Premise, inv.Body, inv.RootCause)
	for _, entry := range inv.Entries {
		bodies = append(bodies, entry.Text)
	}
	for _, finding := range inv.Findings {
		bodies = append(bodies, finding.Text)
	}
	return bodies
}
