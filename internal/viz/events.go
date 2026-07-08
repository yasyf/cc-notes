package viz

import (
	"context"
	"sort"

	"github.com/yasyf/cc-notes/internal/trail"
)

// eventsAndEntities derives the entity half of the graph from the entity
// trails: every classified lifecycle event, the per-entity legend summaries,
// and — appended onto topo.extra for lanes() to serialize — the deleted-branch
// lanes reconstructed from task trails. Events come back sorted by time, ties
// held in entity-ref then trail order.
func (b *Builder) eventsAndEntities(ctx context.Context, topo *topology) ([]Event, []EntitySummary, error) {
	refTips, err := b.entityRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	taken := takenBranches(topo)
	dead := newDeadBranches()

	var events []Event
	for _, rt := range refTips {
		entries, err := b.trailOf(ctx, rt.ref, rt.tip)
		if err != nil {
			return nil, nil, err
		}
		if len(entries) == 0 {
			continue
		}
		ref := entityRefOf(entries[len(entries)-1].Snapshot)
		for _, entry := range entries {
			if trail.IsCheckpoint(entry.Commit) {
				continue
			}
			branch := branchOf(entry.Snapshot)
			for _, spec := range classify(entry) {
				ev := Event{
					Entity: ref,
					Type:   spec.typ,
					Time:   entry.Commit.AuthorTime,
					Branch: branch,
					SHA:    entry.Commit.SHA,
					Detail: spec.detail,
				}
				events = append(events, ev)
				if ref.Kind == entityTask {
					dead.observe(ev, taken)
				}
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time < events[j].Time })
	topo.extra = dead.lanes(taken)

	entities, err := b.entities(ctx)
	if err != nil {
		return nil, nil, err
	}
	return events, entities, nil
}

// liveBranches is the set of branch names backed by a live ref: the trunk and
// every enumerated branch lane. A deleted-branch lane never shadows one.
func liveBranches(topo *topology) map[string]bool {
	live := make(map[string]bool, len(topo.branches)+1)
	live[topo.trunk.name] = true
	for _, s := range topo.branches {
		live[s.name] = true
	}
	return live
}

// takenBranches is the set of branch names a lane already claims: the live
// branches plus the DAG-mined deleted branches. The task-trail reconstruction
// skips these, so a branch with surviving DAG evidence keeps its mined lane
// rather than being shadowed by a second, weaker task-inferred one.
func takenBranches(topo *topology) map[string]bool {
	taken := liveBranches(topo)
	for _, l := range topo.mined {
		taken[l.Name] = true
	}
	return taken
}

// deadBranch is one branch named only in task history, with no live ref: its
// event extent and, when the trail moved off it onto a live lane, the inferred
// merge into that lane.
type deadBranch struct {
	start    int64
	end      int64
	hasStart bool
	merge    *MergePoint
}

// deadBranches accumulates deleted-branch lanes across the task-trail walk. It
// is single-goroutine build state — eventsAndEntities owns it for one Graph
// call — so it carries no synchronization.
type deadBranches struct {
	m map[string]*deadBranch
}

func newDeadBranches() *deadBranches { return &deadBranches{m: make(map[string]*deadBranch)} }

// observe folds one task event into the deleted-branch accumulator: it extends
// the extent of every dead branch the event names — its own branch, or the from
// and to of a branch move — and, when the move left a dead branch for a taken
// one, records the inferred merge (the latest such move wins). A taken branch —
// live or DAG-mined — never becomes a task-inferred lane.
func (d *deadBranches) observe(ev Event, taken map[string]bool) {
	for _, name := range namedBranches(ev) {
		if name == "" || taken[name] {
			continue
		}
		db := d.at(name)
		if !db.hasStart || ev.Time < db.start {
			db.start, db.hasStart = ev.Time, true
		}
		if ev.Time > db.end {
			db.end = ev.Time
		}
	}
	if ev.Type != evBranchMoved {
		return
	}
	from, to := ev.Detail["from"], ev.Detail["to"]
	if from != "" && !taken[from] && to != "" && taken[to] {
		db := d.at(from)
		if db.merge == nil || ev.Time >= db.merge.Time {
			db.merge = &MergePoint{SHA: ev.SHA, Time: ev.Time, Into: to, Kind: kindInferred}
		}
	}
}

func (d *deadBranches) at(name string) *deadBranch {
	db := d.m[name]
	if db == nil {
		db = &deadBranch{}
		d.m[name] = db
	}
	return db
}

// lanes renders the accumulated dead branches into deleted lanes, sorted by
// name. Each is inferred, ref-less (nil fork and tip), and spans its event
// extent. A branch a mined or live lane already claims is dropped.
func (d *deadBranches) lanes(taken map[string]bool) []Lane {
	lanes := make([]Lane, 0, len(d.m))
	for name, db := range d.m {
		if taken[name] {
			continue
		}
		lanes = append(lanes, Lane{
			Name:     name,
			Status:   statusDeleted,
			Inferred: true,
			Merge:    db.merge,
			Start:    db.start,
			End:      db.end,
		})
	}
	sort.Slice(lanes, func(i, j int) bool { return lanes[i].Name < lanes[j].Name })
	return lanes
}

// namedBranches lists the branch names an event references: its attributed
// branch plus, for a branch move, the from and to of the transition.
func namedBranches(ev Event) []string {
	names := []string{ev.Branch}
	if ev.Type == evBranchMoved {
		names = append(names, ev.Detail["from"], ev.Detail["to"])
	}
	return names
}
