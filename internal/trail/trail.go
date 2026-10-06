// Package trail turns an entity's linearized fold history into a per-commit
// change trail: it classifies each step as a create, edit, or checkpoint and
// diffs successive snapshots into field-level changes, dropping commits whose
// only effect was bookkeeping or that were idempotent. It owns the step→entries
// mechanics only; presentation (verbs, text, JSON) lives with the caller.
package trail

import (
	"bytes"
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
// no visible field and stays out of the trail. Each snapshot is encoded once and
// serves as the after side of its own step and the before side of the next.
func Entries(steps []fold.Step) ([]Entry, error) {
	var entries []Entry
	var prev *encodedSnapshot
	for i, st := range steps {
		cur, err := encodeSnapshot(st.Snapshot)
		if err != nil {
			return nil, err
		}
		before := prev
		prev = cur
		e := Entry{Commit: st.Commit, Snapshot: st.Snapshot}
		switch {
		case IsCheckpoint(st.Commit):
			e.Kind = "checkpoint"
			e.Covers = checkpointCovers(st.Commit)
		case i == 0:
			e.Kind = "create"
			zero, err := encodeSnapshot(st.Snapshot.Meta().Kind.Zero())
			if err != nil {
				return nil, err
			}
			changes, err := diffSnapshots(zero, cur)
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
			changes, err := diffSnapshots(before, cur)
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

// encodedSnapshot is a snapshot's canonical JSON split one level deep: each
// top-level field's raw encoding, plus the per-field array elements split out
// on first use. Raw equality implies canonical equality, so equal raw bytes
// settle a field or an element as unchanged without decoding it.
type encodedSnapshot struct {
	fields   map[string]json.RawMessage
	elements map[string]rawElements
}

// rawElements is one array field's elements and the set of their raw encodings.
type rawElements struct {
	elems []json.RawMessage
	set   map[string]struct{}
}

func encodeSnapshot(snap model.Snapshot) (*encodedSnapshot, error) {
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return &encodedSnapshot{fields: fields, elements: map[string]rawElements{}}, nil
}

// arrayElements returns the field's elements when its value is a JSON array,
// and nil otherwise — a missing, null, or non-array field has no elements.
func (s *encodedSnapshot) arrayElements(field string) (rawElements, error) {
	if re, ok := s.elements[field]; ok {
		return re, nil
	}
	var re rawElements
	if raw := s.fields[field]; isArray(raw) {
		if err := json.Unmarshal(raw, &re.elems); err != nil {
			return rawElements{}, err
		}
		re.set = make(map[string]struct{}, len(re.elems))
		for _, el := range re.elems {
			re.set[string(el)] = struct{}{}
		}
	}
	s.elements[field] = re
	return re, nil
}

// diffSnapshots reports the fields that changed between two encoded snapshots
// of the same entity, comparing every field but the bookkeeping ones. A field
// whose raw encoding is unchanged is skipped undecoded. Scalars report From→To;
// set-valued fields report Added and Removed elements.
func diffSnapshots(before, after *encodedSnapshot) ([]Change, error) {
	var changes []Change
	for _, field := range unionKeys(before.fields, after.fields) {
		if hiddenFields[field] {
			continue
		}
		b, a := before.fields[field], after.fields[field]
		if bytes.Equal(b, a) {
			continue
		}
		ch, ok, err := diffField(field, before, after)
		if err != nil {
			return nil, err
		}
		if ok {
			changes = append(changes, ch)
		}
	}
	return changes, nil
}

func diffField(field string, before, after *encodedSnapshot) (Change, bool, error) {
	b, a := before.fields[field], after.fields[field]
	if isArray(b) || isArray(a) {
		be, err := before.arrayElements(field)
		if err != nil {
			return Change{}, false, err
		}
		ae, err := after.arrayElements(field)
		if err != nil {
			return Change{}, false, err
		}
		added, removed, err := diffElements(be, ae)
		if err != nil {
			return Change{}, false, err
		}
		if len(added) == 0 && len(removed) == 0 {
			return Change{}, false, nil
		}
		return Change{Field: field, Added: added, Removed: removed}, true, nil
	}
	bv, err := decode(b)
	if err != nil {
		return Change{}, false, err
	}
	av, err := decode(a)
	if err != nil {
		return Change{}, false, err
	}
	if identity(bv) == identity(av) {
		return Change{}, false, nil
	}
	return Change{Field: field, Scalar: true, From: bv, To: av}, true, nil
}

// diffElements set-diffs two arrays by canonical identity. Elements whose raw
// encoding appears on both sides are dropped undecoded; only the remainder is
// decoded and compared.
func diffElements(before, after rawElements) (added, removed []any, err error) {
	bset, err := identitySet(uniqueElements(before.elems, after.set))
	if err != nil {
		return nil, nil, err
	}
	aset, err := identitySet(uniqueElements(after.elems, before.set))
	if err != nil {
		return nil, nil, err
	}
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
	return added, removed, nil
}

func uniqueElements(elems []json.RawMessage, other map[string]struct{}) []json.RawMessage {
	var out []json.RawMessage
	for _, el := range elems {
		if _, ok := other[string(el)]; !ok {
			out = append(out, el)
		}
	}
	return out
}

func identitySet(elems []json.RawMessage) (map[string]any, error) {
	out := make(map[string]any, len(elems))
	for _, el := range elems {
		v, err := decode(el)
		if err != nil {
			return nil, err
		}
		out[identity(v)] = v
	}
	return out, nil
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

// decode turns a raw field or element into its canonical-JSON form; a missing
// field decodes to nil, as JSON null does.
func decode(raw json.RawMessage) (any, error) {
	if raw == nil {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func isArray(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '['
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

func unionKeys(a, b map[string]json.RawMessage) []string {
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

// EntityKind returns the lowercase kind name of a snapshot: note, doc, log,
// task, sprint, project, runbook, investigation, plan, or answer.
func EntityKind(snap model.Snapshot) string {
	return string(snap.Meta().Kind)
}
