package cli

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// searchDTO is one merged hit of the top-level search: a kind discriminator
// plus the matching entity DTO, mutually exclusive like relevantDTO's fields.
type searchDTO struct {
	Kind    string      `json:"kind"`
	Note    *noteDTO    `json:"note,omitempty"`
	Doc     *docDTO     `json:"doc,omitempty"`
	Log     *logDTO     `json:"log,omitempty"`
	Runbook *runbookDTO `json:"runbook,omitempty"`
}

// searchHit pairs one matched entity with its kind's own rank tier, so the
// per-kind result lists merge into a single consistently ordered set.
type searchHit struct {
	snap model.Snapshot
	tier int
}

// newSearchCmd builds the top-level "cc-notes search QUERY": one ranked search
// fanned out across notes, docs, logs, and runbooks, merged kind-tagged. Like
// show and history it is global because a query needs no noun; the noun-scoped
// "<kind> search" commands remain for a single-kind search with that kind's
// full filter set (e.g. --author).
func newSearchCmd() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across every note, doc, log, and runbook",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, c, err := openStoreClient()
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
			return printSearchHits(cmd, s, hits, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	bindLimit(flags, &limit, 20)
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

// searchAllKinds fans query out to every kind's ranked search and merges the
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
		hits = append(hits, searchHit{snap: d, tier: textTier(d.Title, d.Tags, []string{d.Body}, q)})
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
// kind's lean line prefixed with a kind tag column.
func printSearchHits(cmd *cobra.Command, s *store.Store, hits []searchHit, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]searchDTO, len(hits))
		for i, h := range hits {
			dto := searchDTO{Kind: string(h.snap.Meta().Kind)}
			switch v := h.snap.(type) {
			case model.Note:
				atts, err := entityAttachments(cmd.Context(), s, v.Attachments)
				if err != nil {
					return err
				}
				n := newNoteDTO(v, "", atts)
				dto.Note = &n
			case model.Doc:
				atts, err := entityAttachments(cmd.Context(), s, v.Attachments)
				if err != nil {
					return err
				}
				d := newDocDTO(v, "", atts)
				dto.Doc = &d
			case model.Log:
				atts, err := entityAttachments(cmd.Context(), s, v.Attachments)
				if err != nil {
					return err
				}
				l := newLogDTO(v, atts)
				dto.Log = &l
			case model.Runbook:
				rb := newRunbookDTO(v)
				dto.Runbook = &rb
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
		case model.Runbook:
			lean = leanRunbookLine(v)
		default:
			panic(fmt.Sprintf("searchAllKinds returned unknown snapshot %T", h.snap))
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\n", h.snap.Meta().Kind, lean); err != nil {
			return err
		}
	}
	return nil
}
