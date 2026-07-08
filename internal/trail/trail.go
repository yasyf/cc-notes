// Package trail turns an entity's linearized fold history into a per-commit
// change trail: it classifies each step as a create, edit, or checkpoint and
// diffs successive snapshots into field-level changes, dropping commits whose
// only effect was bookkeeping or that were idempotent. It owns the step→entries
// mechanics only; presentation (verbs, text, JSON) lives with the caller.
package trail

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// Entry is one commit in an entity's trail: the commit metadata, the machine
// kind ("create"|"edit"|"checkpoint"), the covered-commit count for
// checkpoints, the fields it changed, and the folded post-state snapshot at
// this step.
type Entry struct {
	Commit   model.PackCommit
	Kind     string
	Covers   int
	Changes  []Change
	Snapshot model.Snapshot
}

// Change is one field's delta: a scalar From→To when Scalar is true, otherwise
// the Added and Removed set elements. Values are the fields' canonical-JSON
// forms — string, float64, bool, nil, or map[string]any — not rendered strings;
// presentation lives with the caller. Set elements are deduplicated and ordered
// by canonical-JSON identity, which is also the scalar equality.
type Change struct {
	Field   string
	Scalar  bool
	From    any
	To      any
	Added   []any
	Removed []any
}

// Entries turns the linearized steps of an entity into its change trail: one
// Entry per commit in linearization order, classifying each as create, edit, or
// checkpoint and diffing successive snapshots into field changes. A commit whose
// only effect was bookkeeping (a lease heartbeat) or that was idempotent changes
// no visible field and stays out of the trail.
func Entries(steps []fold.Step) ([]Entry, error) {
	var entries []Entry
	for i, st := range steps {
		e := Entry{Commit: st.Commit, Snapshot: st.Snapshot}
		switch {
		case IsCheckpoint(st.Commit):
			e.Kind = "checkpoint"
			e.Covers = checkpointCovers(st.Commit)
		case i == 0:
			e.Kind = "create"
			changes, err := diffSnapshots(zeroLike(st.Snapshot), st.Snapshot)
			if err != nil {
				return nil, err
			}
			// A create is "from nothing": clear every initial scalar's From so a
			// caller renders "priority: 2", not "0 → 2".
			for j := range changes {
				if changes[j].Scalar {
					changes[j].From = nil
				}
			}
			e.Changes = changes
		default:
			e.Kind = "edit"
			changes, err := diffSnapshots(steps[i-1].Snapshot, st.Snapshot)
			if err != nil {
				return nil, err
			}
			if len(changes) == 0 {
				continue
			}
			e.Changes = changes
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// diffSnapshots reports the fields that changed between two snapshots of the
// same entity, by marshaling each to its canonical JSON map and comparing every
// field but the bookkeeping ones. Scalars report From→To; set-valued fields
// report Added and Removed elements.
func diffSnapshots(before, after model.Snapshot) ([]Change, error) {
	bm, err := snapshotMap(before)
	if err != nil {
		return nil, err
	}
	am, err := snapshotMap(after)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, field := range unionKeys(bm, am) {
		if hiddenFields[field] {
			continue
		}
		if ch, ok := diffField(field, bm[field], am[field]); ok {
			changes = append(changes, ch)
		}
	}
	return changes, nil
}

func diffField(field string, before, after any) (Change, bool) {
	ba, baIsArray := before.([]any)
	aa, aaIsArray := after.([]any)
	if baIsArray || aaIsArray {
		added, removed := diffElements(ba, aa)
		if len(added) == 0 && len(removed) == 0 {
			return Change{}, false
		}
		return Change{Field: field, Added: added, Removed: removed}, true
	}
	if identity(before) == identity(after) {
		return Change{}, false
	}
	return Change{Field: field, Scalar: true, From: before, To: after}, true
}

func diffElements(before, after []any) (added, removed []any) {
	bset := identitySet(before)
	aset := identitySet(after)
	for id, v := range aset {
		if _, ok := bset[id]; !ok {
			added = append(added, v)
		}
	}
	for id, v := range bset {
		if _, ok := aset[id]; !ok {
			removed = append(removed, v)
		}
	}
	sortByIdentity(added)
	sortByIdentity(removed)
	return added, removed
}

func identitySet(elems []any) map[string]any {
	out := make(map[string]any, len(elems))
	for _, e := range elems {
		out[identity(e)] = e
	}
	return out
}

func sortByIdentity(elems []any) {
	sort.Slice(elems, func(i, j int) bool { return identity(elems[i]) < identity(elems[j]) })
}

// identity is the canonical-JSON encoding of a snapshot value: the key by which
// set elements are deduplicated and scalars compared. Go marshals maps with
// sorted keys, so the encoding is deterministic.
func identity(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("trail: canonical value not marshalable: %v", err))
	}
	return string(b)
}

// hiddenFields are snapshot fields excluded from the audit diff: bookkeeping
// that moves on every commit, or derived content witnesses — none of which is a
// user edit.
var hiddenFields = map[string]bool{
	"id":                true,
	"author":            true,
	"created_at":        true,
	"updated_at":        true,
	"head":              true,
	"heartbeat_at":      true,
	"heartbeat_lamport": true,
	"witness":           true,
	"verified_commit":   true,
}

func snapshotMap(snap model.Snapshot) (map[string]any, error) {
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// IsCheckpoint reports whether a commit carries only Checkpoint ops — a
// compaction marker, not a user edit.
func IsCheckpoint(c model.PackCommit) bool {
	if len(c.Pack.Ops) == 0 {
		return false
	}
	for _, op := range c.Pack.Ops {
		if _, ok := op.(model.Checkpoint); !ok {
			return false
		}
	}
	return true
}

func checkpointCovers(c model.PackCommit) int {
	n := 0
	for _, op := range c.Pack.Ops {
		if cp, ok := op.(model.Checkpoint); ok {
			n += len(cp.CoversShas)
		}
	}
	return n
}

func zeroLike(snap model.Snapshot) model.Snapshot {
	switch snap.(type) {
	case model.Note:
		return model.Note{}
	case model.Doc:
		return model.Doc{}
	case model.Log:
		return model.Log{}
	case model.Task:
		return model.Task{}
	case model.Sprint:
		return model.Sprint{}
	case model.Project:
		return model.Project{}
	default:
		panic(fmt.Sprintf("trail: unknown snapshot type %T", snap))
	}
}

// EntityKind returns the lowercase kind name of a snapshot: note, doc, log,
// task, sprint, or project.
func EntityKind(snap model.Snapshot) string {
	switch snap.(type) {
	case model.Note:
		return "note"
	case model.Doc:
		return "doc"
	case model.Log:
		return "log"
	case model.Task:
		return "task"
	case model.Sprint:
		return "sprint"
	case model.Project:
		return "project"
	default:
		panic(fmt.Sprintf("trail: unknown snapshot type %T", snap))
	}
}
