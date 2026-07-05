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
	"strconv"
	"time"

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

// Change is one field's delta: a scalar From→To when Scalar is true, otherwise a
// set of Added and Removed elements.
type Change struct {
	Field   string
	Scalar  bool
	From    string
	To      string
	Added   []string
	Removed []string
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
			// A create is "from nothing": render every initial scalar as a
			// plain set, so a numeric default reads "priority: 2", not "0 → 2".
			for j := range changes {
				if changes[j].Scalar {
					changes[j].From = ""
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
		added, removed := diffElements(field, ba, aa)
		if len(added) == 0 && len(removed) == 0 {
			return Change{}, false
		}
		return Change{Field: field, Added: added, Removed: removed}, true
	}
	from := formatScalar(field, before)
	to := formatScalar(field, after)
	if from == to {
		return Change{}, false
	}
	return Change{Field: field, Scalar: true, From: from, To: to}, true
}

func diffElements(field string, before, after []any) (added, removed []string) {
	bset := elementSet(field, before)
	aset := elementSet(field, after)
	for s := range aset {
		if !bset[s] {
			added = append(added, s)
		}
	}
	for s := range bset {
		if !aset[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func elementSet(field string, elems []any) map[string]bool {
	out := make(map[string]bool, len(elems))
	for _, e := range elems {
		out[formatElement(field, e)] = true
	}
	return out
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

// timeFields are unix-seconds scalars rendered as RFC3339 UTC.
var timeFields = map[string]bool{
	"verified_at": true,
	"started_at":  true,
	"closed_at":   true,
	"stale_at":    true,
	"start_date":  true,
	"end_date":    true,
}

func formatScalar(field string, v any) string {
	if v == nil {
		return ""
	}
	if timeFields[field] {
		if n, ok := v.(float64); ok {
			if n == 0 {
				return ""
			}
			return rfc3339(int64(n))
		}
	}
	return scalarString(v)
}

// formatElement renders one set element to a stable, human string: a string
// element verbatim, a known object element (anchor, comment, log entry,
// criterion) summarized, any other object as compact JSON.
func formatElement(field string, v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return scalarString(v)
	}
	switch field {
	case "anchors":
		return fmt.Sprintf("%s:%s", scalarString(m["kind"]), scalarString(m["value"]))
	case "comments":
		return fmt.Sprintf("comment by %s: %q", scalarString(m["author"]), scalarString(m["body"]))
	case "entries":
		return fmt.Sprintf("entry by %s: %q", scalarString(m["author"]), scalarString(m["text"]))
	case "criteria":
		return fmt.Sprintf("%q [%s]", scalarString(m["text"]), scalarString(m["status"]))
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
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

func rfc3339(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) }
