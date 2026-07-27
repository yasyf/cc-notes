package stale

import (
	"context"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// record is one corpus entity joined with the lifecycle fields the staleness
// signals read. The embedded eval.Entity is the same population the retrieval
// harness ranks, so the gate covers exactly what a retriever can surface.
type record struct {
	eval.Entity
	Anchors      []model.Anchor
	Attested     int64
	Verdict      notes.Verdict
	Successors   []model.EntityID
	InvStatus    model.InvestigationStatus
	TaskStatus   model.Status
	Branch       model.Branch
	LeaseExpired bool
}

// branches is the branch topology S6 reads: trunk, the branches that still
// exist, and those already merged into trunk.
type branches struct {
	trunk  model.Branch
	live   map[model.Branch]bool
	merged map[model.Branch]bool
}

// load folds the corpus and joins each entity with its lifecycle fields. The
// freshness verdicts come from ReviewNotes and ReviewDocs — one shared HEAD and
// clock for the batch — rather than a per-entity verdict call.
func (e *Evaluator) load(ctx context.Context) ([]record, error) {
	corpus, err := eval.LoadCorpus(ctx, e.c)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}
	if len(corpus) == 0 {
		return nil, nil
	}

	ns, err := e.c.Notes(ctx, notes.DocumentFilter{IncludeSuperseded: true})
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	docs, err := e.c.Docs(ctx, notes.DocumentFilter{IncludeSuperseded: true})
	if err != nil {
		return nil, fmt.Errorf("list docs: %w", err)
	}
	logs, err := e.c.Logs(ctx, notes.LogFilter{})
	if err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	runbooks, err := e.c.Runbooks(ctx, notes.RunbookFilter{IncludeArchived: true})
	if err != nil {
		return nil, fmt.Errorf("list runbooks: %w", err)
	}
	invs, err := e.c.Investigations(ctx, notes.InvestigationFilter{})
	if err != nil {
		return nil, fmt.Errorf("list investigations: %w", err)
	}
	tasks, err := e.c.Tasks(ctx, notes.TaskFilter{Scope: notes.ScopeAllBranches})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	verdicts, err := e.verdicts(ctx)
	if err != nil {
		return nil, err
	}
	leased, err := e.leaseExpired(ctx)
	if err != nil {
		return nil, err
	}

	noteByID := byID(ns, func(n model.Note) model.EntityID { return n.ID })
	docByID := byID(docs, func(d model.Doc) model.EntityID { return d.ID })
	logByID := byID(logs, func(l model.Log) model.EntityID { return l.ID })
	runbookByID := byID(runbooks, func(rb model.Runbook) model.EntityID { return rb.ID })
	invByID := byID(invs, func(iv model.Investigation) model.EntityID { return iv.ID })
	taskByID := byID(tasks, func(t model.Task) model.EntityID { return t.ID })

	recs := make([]record, len(corpus))
	for i, ent := range corpus {
		r := record{Entity: ent, Attested: ent.UpdatedAt, Verdict: verdicts[ent.ID]}
		switch ent.Kind {
		case model.KindNote:
			n := noteByID[ent.ID]
			r.Anchors, r.Attested = n.Anchors, max(n.CreatedAt, n.VerifiedAt)
		case model.KindDoc:
			d := docByID[ent.ID]
			r.Anchors, r.Attested = d.Anchors, max(d.CreatedAt, d.VerifiedAt)
		case model.KindLog:
			l := logByID[ent.ID]
			r.Anchors, r.Attested = l.Anchors, l.CreatedAt
		case model.KindRunbook:
			rb := runbookByID[ent.ID]
			r.Anchors, r.Attested = rb.Anchors, rb.CreatedAt
		case model.KindInvestigation:
			iv := invByID[ent.ID]
			r.Anchors, r.Attested, r.InvStatus = iv.Anchors, iv.CreatedAt, iv.Status
		case model.KindTask:
			t := taskByID[ent.ID]
			r.Attested, r.TaskStatus, r.Branch, r.LeaseExpired = t.CreatedAt, t.Status, t.Branch, leased[t.ID]
		}
		if len(ent.SupersededBy) > 0 {
			r.Successors, err = e.successors(ctx, ent)
			if err != nil {
				return nil, err
			}
		}
		recs[i] = r
	}
	return recs, nil
}

// verdicts collects the note and doc freshness verdicts in two batched review
// passes. A record absent from the map is fresh by the notes-side definition.
func (e *Evaluator) verdicts(ctx context.Context) (map[model.EntityID]notes.Verdict, error) {
	noteReviews, err := e.c.ReviewNotes(ctx, e.policy.StaleAfter)
	if err != nil {
		return nil, fmt.Errorf("review notes: %w", err)
	}
	docReviews, err := e.c.ReviewDocs(ctx, e.policy.StaleAfter)
	if err != nil {
		return nil, fmt.Errorf("review docs: %w", err)
	}
	out := make(map[model.EntityID]notes.Verdict, len(noteReviews)+len(docReviews))
	for _, r := range noteReviews {
		out[r.Note.ID] = r.Verdict
	}
	for _, r := range docReviews {
		out[r.Doc.ID] = r.Verdict
	}
	return out, nil
}

// leaseExpired indexes the in-progress tasks whose claim has gone idle past the
// policy's lease TTL, reusing the same StaleTasks fold `task stale` reports.
func (e *Evaluator) leaseExpired(ctx context.Context) (map[model.EntityID]bool, error) {
	tasks, err := e.c.StaleTasks(ctx, e.policy.LeaseTTL)
	if err != nil {
		return nil, fmt.Errorf("stale tasks: %w", err)
	}
	out := make(map[model.EntityID]bool, len(tasks))
	for _, t := range tasks {
		out[t.ID] = true
	}
	return out, nil
}

// successors resolves what a reader of a superseded record should read instead.
// Notes and docs go through the client's cycle-safe transitive walkers; no
// other kind has a supersede verb, so its edge is surfaced verbatim.
func (e *Evaluator) successors(ctx context.Context, ent eval.Entity) ([]model.EntityID, error) {
	switch ent.Kind {
	case model.KindNote:
		heads, err := e.c.NoteSupersedeHeads(ctx, ent.ID)
		if err != nil {
			return nil, fmt.Errorf("note supersede heads %s: %w", ent.ID.Short(), err)
		}
		return heads, nil
	case model.KindDoc:
		heads, err := e.c.DocSupersedeHeads(ctx, ent.ID)
		if err != nil {
			return nil, fmt.Errorf("doc supersede heads %s: %w", ent.ID.Short(), err)
		}
		return heads, nil
	}
	return ent.SupersededBy, nil
}

// loadBranches resolves trunk and the merged and live branch sets S6 compares a
// task's branch against.
func (e *Evaluator) loadBranches(ctx context.Context) (branches, error) {
	trunk, err := e.git.TrunkBranch(ctx)
	if err != nil {
		return branches{}, fmt.Errorf("resolve trunk: %w", err)
	}
	tip, err := e.git.ResolveCommit(ctx, "refs/heads/"+string(trunk))
	if err != nil {
		return branches{}, fmt.Errorf("resolve trunk tip: %w", err)
	}
	tips, err := e.git.RefTips(ctx, headsPrefix)
	if err != nil {
		return branches{}, err
	}
	mergedRefs, err := e.git.MergedRefs(ctx, tip, headsPrefix)
	if err != nil {
		return branches{}, err
	}
	br := branches{trunk: trunk, live: make(map[model.Branch]bool, len(tips)), merged: make(map[model.Branch]bool, len(mergedRefs))}
	for _, t := range tips {
		br.live[branchOf(t.Ref)] = true
	}
	for _, ref := range mergedRefs {
		br.merged[branchOf(ref)] = true
	}
	return br, nil
}

// headsPrefix is the for-each-ref pattern matching every local branch.
const headsPrefix = "refs/heads/"

// branchOf strips the ref namespace off a local branch ref.
func branchOf(ref string) model.Branch {
	return model.Branch(strings.TrimPrefix(ref, headsPrefix))
}

// byID indexes a listing by entity id.
func byID[T any](all []T, id func(T) model.EntityID) map[model.EntityID]T {
	m := make(map[model.EntityID]T, len(all))
	for _, e := range all {
		m[id(e)] = e
	}
	return m
}

// oldest returns the earliest attestation time in the batch — the churn log's
// window, since no record can be demoted for churn that predates it.
func oldest(recs []record) int64 {
	out := recs[0].Attested
	for _, r := range recs[1:] {
		out = min(out, r.Attested)
	}
	return out
}

// joinComma renders a short list for a detail line.
func joinComma(parts []string) string { return strings.Join(parts, ", ") }
