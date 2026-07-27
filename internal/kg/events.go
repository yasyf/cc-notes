package kg

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/lifecycle"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

// entityEvents classifies one entity's chain into its lifecycle events and
// returns them with the tip snapshot the chain folds to. It reads the episodic
// layer the visualization already reads — fold.History, trail.Entries, and the
// lifecycle verbs — so both surfaces agree on what happened and when.
func entityEvents(ref string, chain []model.PackCommit) ([]Event, model.Snapshot, error) {
	steps, err := fold.History(chain)
	if err != nil {
		return nil, nil, fmt.Errorf("history %s: %w", ref, err)
	}
	entries, err := trail.Entries(steps)
	if err != nil {
		return nil, nil, fmt.Errorf("trail %s: %w", ref, err)
	}
	tip := steps[len(steps)-1].Snapshot
	self := EntityNode(tip.Meta().Kind, tip.EntityID())

	var events []Event
	for _, entry := range entries {
		if trail.IsCheckpoint(entry.Commit) {
			continue
		}
		branch := lifecycle.Branch(entry.Snapshot)
		for _, ev := range lifecycle.Classify(entry) {
			events = append(events, Event{
				Entity:  self,
				Type:    ev.Type,
				At:      entry.Commit.AuthorTime,
				Lamport: entry.Commit.Pack.Lamport,
				Session: entry.Commit.Pack.Session,
				Actor:   string(entry.Commit.Author),
				Branch:  branch,
				SHA:     entry.Commit.SHA,
			})
		}
	}
	return events, tip, nil
}

// sessionSpan is one session's contribution to one entity: how many of the
// entity's lifecycle events it carried, and when the last of them landed.
type sessionSpan struct {
	events int
	last   int64
}

// sessionSpans tallies each session's contribution to an entity's events. A
// pack written outside a session carries none.
func sessionSpans(events []Event) map[string]sessionSpan {
	spans := map[string]sessionSpan{}
	for _, e := range events {
		if e.Session == "" {
			continue
		}
		span := spans[e.Session]
		span.events++
		span.last = max(span.last, e.At)
		spans[e.Session] = span
	}
	return spans
}

// addSessionEdges links an entity to every session that wrote it, weighted by
// how many of the entity's lifecycle events that session carried: a session
// that shaped an entity over forty ops is stronger evidence of what the entity
// is about than one that touched it once.
func (b *builder) addSessionEdges(self NodeID, events []Event) {
	spans := sessionSpans(events)
	for _, id := range slices.Sorted(maps.Keys(spans)) {
		to := SessionNode(id)
		b.addNode(Node{ID: to, Kind: NodeSession, Value: id, UpdatedAt: spans[id].last})
		b.addEdge(Edge{From: self, To: to, Kind: EdgeSession, Weight: float64(spans[id].events)})
	}
}

// sortEvents orders events by time, then by entity and its lamport clock, so a
// time range is one scan, a stored graph is byte-identical across builds, and
// one entity's events stay in the order the fold replayed them even when a
// whole burst shares an author-second.
func sortEvents(events []Event) {
	slices.SortFunc(events, func(a, z Event) int {
		if c := cmp.Compare(a.At, z.At); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Entity, z.Entity); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Lamport, z.Lamport); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Type, z.Type); c != 0 {
			return c
		}
		return cmp.Compare(a.SHA, z.SHA)
	})
}
