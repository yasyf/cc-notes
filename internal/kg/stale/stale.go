// Package stale computes the staleness signals a knowledge-graph ranker gates
// and demotes cc-notes records with. It builds on the freshness verdict the
// notes client already computes — EXPIRED / UNVERIFIED / DRIFTED / STALE /
// DANGLING, via ReviewNotes and ReviewDocs — rather than recomputing it, and
// adds what that verdict lacks: lifecycle terminality, branch reconciliation,
// path churn, dead code references, and time decay.
//
// Abstention is a first-class outcome. A gated record is withheld from
// injection entirely and carries weight zero; the rank penalties only reorder.
package stale

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// Signal names the staleness signal that fired for a record.
type Signal string

const (
	// SignalDrift is S1: a witnessed anchor no longer matches live content at
	// HEAD, or a supersede edge points at a tombstoned record.
	SignalDrift Signal = "DRIFT"
	// SignalSuperseded is S2: a live supersede edge points elsewhere.
	SignalSuperseded Signal = "SUPERSEDED"
	// SignalExpired is S3: an agent marked the record out of date.
	SignalExpired Signal = "EXPIRED"
	// SignalExonerated is S4: the investigation's premise was refuted.
	SignalExonerated Signal = "EXONERATED"
	// SignalClosed is S5: the task is terminal or its claim lease expired, or
	// the plan is done or abandoned.
	SignalClosed Signal = "CLOSED"
	// SignalReconciled is S6: the task's branch merged into trunk or is gone.
	SignalReconciled Signal = "RECONCILED"
	// SignalChurn is S7: the anchored paths churned since the record was
	// attested.
	SignalChurn Signal = "CHURN"
	// SignalDeadRef is S8: the record names a code element absent from the tree.
	SignalDeadRef Signal = "DEAD_REF"
	// SignalDecay is S9: time elapsed since the record was last verified.
	SignalDecay Signal = "DECAY"
)

// Default policy thresholds.
const (
	// ChurnHalfLife is the count of churned lines on a record's anchored paths
	// that halves its rank weight (S7).
	ChurnHalfLife = 200
	// DeadRefHalfLife is the count of dead code references that halves a
	// record's rank weight (S8).
	DeadRefHalfLife = 2
	// ReverifyBelow is the per-signal weight under which a penalty enqueues a
	// re-verify.
	ReverifyBelow = 0.5
	// PromoteWeight is the rank multiplier a confirmed root cause earns (S4).
	PromoteWeight = 1.25
	// MaxScanBytes caps the size of a tracked file S8's tree scan tokenizes.
	MaxScanBytes = 1 << 20
)

// Policy is the tunable half-lives and thresholds an evaluation runs under.
// HalfLives drives S9: a kind absent from the map never decays, which is how
// logs stay exempt — they are episodic and never go wrong.
type Policy struct {
	Now             time.Time                    `json:"now"`
	StaleAfter      time.Duration                `json:"stale_after"`
	LeaseTTL        time.Duration                `json:"lease_ttl"`
	HalfLives       map[model.Kind]time.Duration `json:"half_lives"`
	ChurnHalfLife   int                          `json:"churn_half_life"`
	DeadRefHalfLife int                          `json:"dead_ref_half_life"`
	ReverifyBelow   float64                      `json:"reverify_below"`
	PromoteWeight   float64                      `json:"promote_weight"`
	MaxScanBytes    int64                        `json:"max_scan_bytes"`
}

// decayHalfLives maps the kinds S9 decays to their half-life: the very
// threshold S1/S3 flag a record stale after, so one cc-notes.noteStaleAfter
// value tunes review and decay together. Kinds absent from the map never decay.
func decayHalfLives(staleAfter time.Duration) map[model.Kind]time.Duration {
	return map[model.Kind]time.Duration{model.KindNote: staleAfter, model.KindDoc: staleAfter}
}

// DefaultPolicy builds the policy from the repository's own configured
// thresholds — the same cc-notes.noteStaleAfter and cc-notes.leaseTTL
// precedence note review and task leases already resolve — plus the package
// default weights, evaluated as of now.
func DefaultPolicy(ctx context.Context, c *notes.Client, now time.Time) (Policy, error) {
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return Policy{}, err
	}
	ttl, err := c.LeaseTTL(ctx)
	if err != nil {
		return Policy{}, err
	}
	return Policy{
		Now:             now,
		StaleAfter:      staleAfter,
		LeaseTTL:        ttl,
		HalfLives:       decayHalfLives(staleAfter),
		ChurnHalfLife:   ChurnHalfLife,
		DeadRefHalfLife: DeadRefHalfLife,
		ReverifyBelow:   ReverifyBelow,
		PromoteWeight:   PromoteWeight,
		MaxScanBytes:    MaxScanBytes,
	}, nil
}

// Penalty is one rank demotion (S7-S9): the signal that produced it, its
// multiplicative weight in (0,1], and the cause in words.
type Penalty struct {
	Signal Signal  `json:"signal"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail"`
}

// Assessment is one record's staleness verdict, shaped for a ranker that holds
// no graph: Gated is the hard non-injection decision, Weight the multiplicative
// rank factor (zero when gated), and Signal names what fired — the gate signal
// when gated, otherwise the heaviest penalty, or empty when the record is
// fresh. Verdict carries the notes-side freshness verdict this reuses, empty
// for the kinds that hold no anchor witness.
type Assessment struct {
	ID        model.EntityID   `json:"id"`
	Kind      model.Kind       `json:"kind"`
	Gated     bool             `json:"gated"`
	Signal    Signal           `json:"signal,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	Verdict   notes.Verdict    `json:"verdict,omitempty"`
	Successor []model.EntityID `json:"successor,omitempty"`
	Promoted  bool             `json:"promoted,omitempty"`
	Reverify  bool             `json:"reverify,omitempty"`
	Weight    float64          `json:"weight"`
	Penalties []Penalty        `json:"penalties,omitempty"`
}

// Evaluator assesses a repository's whole cc-notes corpus in one pass, sharing
// the identifier index and the churn log across every record.
type Evaluator struct {
	c      *notes.Client
	git    gitcmd.Git
	policy Policy
}

// New builds an evaluator over the repository at dir, which c must already be
// open on.
func New(c *notes.Client, dir string, p Policy) *Evaluator {
	return &Evaluator{c: c, git: gitcmd.Git{Dir: dir}, policy: p}
}

// Assess evaluates every record in the corpus and returns the assessments
// sorted by entity id. The tree scan and the churn log run once for the whole
// batch, so cost is one git log and one working-tree walk regardless of corpus
// size.
func (e *Evaluator) Assess(ctx context.Context) ([]Assessment, error) {
	recs, err := e.load(ctx)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	br, err := e.loadBranches(ctx)
	if err != nil {
		return nil, err
	}
	tree, err := ScanTree(ctx, e.git, e.policy.MaxScanBytes)
	if err != nil {
		return nil, err
	}
	touches, err := churnLog(ctx, e.git, time.Unix(oldest(recs), 0))
	if err != nil {
		return nil, err
	}
	out := make([]Assessment, len(recs))
	for i, r := range recs {
		out[i] = e.assess(r, br, tree, touches)
	}
	return out, nil
}

// Index keys assessments by entity id — the shape a ranker looks each candidate
// up in.
func Index(as []Assessment) map[model.EntityID]Assessment {
	m := make(map[model.EntityID]Assessment, len(as))
	for _, a := range as {
		m[a.ID] = a
	}
	return m
}

// ReverifyQueue lists the records whose penalties call for a fresh verify
// through the note_verify / doc_verify surface, weakest weight first.
func ReverifyQueue(as []Assessment) []model.EntityID {
	queued := make([]Assessment, 0, len(as))
	for _, a := range as {
		if a.Reverify {
			queued = append(queued, a)
		}
	}
	slices.SortFunc(queued, func(a, b Assessment) int {
		if c := cmp.Compare(a.Weight, b.Weight); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	out := make([]model.EntityID, len(queued))
	for i, a := range queued {
		out[i] = a.ID
	}
	return out
}

// assess resolves one record: the hard gate first, then the rank penalties.
func (e *Evaluator) assess(r record, br branches, tree *Tree, touches []touch) Assessment {
	a := Assessment{ID: r.ID, Kind: r.Kind, Verdict: r.Verdict, Weight: 1}
	if sig, detail := gate(r, br); sig != "" {
		a.Gated, a.Signal, a.Detail, a.Weight = true, sig, detail, 0
		if sig == SignalSuperseded {
			a.Successor = r.Successors
		}
		return a
	}
	a.Promoted = r.Kind == model.KindInvestigation && r.InvStatus == model.InvestigationConfirmed
	if a.Promoted {
		a.Weight = e.policy.PromoteWeight
		a.Detail = "confirmed root cause"
	}
	a.Penalties = e.penalties(r, a.Promoted, tree, touches)
	for _, p := range a.Penalties {
		a.Weight *= p.Weight
		if p.Weight < e.policy.ReverifyBelow {
			a.Reverify = true
		}
	}
	if len(a.Penalties) > 0 {
		heaviest := slices.MinFunc(a.Penalties, func(x, y Penalty) int {
			return cmp.Compare(x.Weight, y.Weight)
		})
		a.Signal, a.Detail = heaviest.Signal, heaviest.Detail
	}
	return a
}

// gate resolves the hard non-injection signals S1-S6 in signal order, first
// match winning. S1 and S3 both read the notes-side verdict, which is a single
// value, so they are mutually exclusive by construction.
func gate(r record, br branches) (Signal, string) {
	switch r.Verdict {
	case notes.VerdictDrifted:
		return SignalDrift, "a witnessed anchor no longer matches HEAD"
	case notes.VerdictDangling:
		return SignalDrift, "the supersede target is tombstoned"
	}
	if len(r.SupersededBy) > 0 {
		return SignalSuperseded, fmt.Sprintf("superseded by %s", shortIDs(r.Successors))
	}
	if r.Verdict == notes.VerdictExpired {
		return SignalExpired, "marked out of date"
	}
	switch r.Kind {
	case model.KindInvestigation:
		if r.InvStatus == model.InvestigationExonerated {
			return SignalExonerated, "the premise was refuted"
		}
	case model.KindTask:
		return taskGate(r, br)
	case model.KindPlan:
		switch r.PlanStatus {
		case model.PlanDone, model.PlanAbandoned:
			return SignalClosed, fmt.Sprintf("plan %s", r.PlanStatus)
		}
	}
	return "", ""
}

// taskGate resolves S5 and S6: a task whose work is over, or whose branch
// context reconcile has already carried into trunk.
func taskGate(r record, br branches) (Signal, string) {
	switch r.TaskStatus {
	case model.StatusDone, model.StatusCancelled:
		return SignalClosed, fmt.Sprintf("task %s", r.TaskStatus)
	}
	if r.LeaseExpired {
		return SignalClosed, "claim lease expired"
	}
	if r.Branch == "" || r.Branch == br.trunk {
		return "", ""
	}
	if !br.live[r.Branch] {
		return SignalReconciled, fmt.Sprintf("branch %s no longer exists — re-anchor to %s or archive", r.Branch, br.trunk)
	}
	if br.merged[r.Branch] {
		return SignalReconciled, fmt.Sprintf("branch %s merged into %s — re-anchor or archive", r.Branch, br.trunk)
	}
	return "", ""
}

// shortIDs renders an id list for a detail line.
func shortIDs(ids []model.EntityID) string {
	if len(ids) == 0 {
		return "a record no longer reachable"
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.Short()
	}
	return joinComma(out)
}
