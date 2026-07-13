package notes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

// HistoryEntry is one commit in an entity's edit trail: the commit metadata plus
// the fields it changed. Kind is the machine class of the commit
// ("create"|"edit"|"checkpoint"); Covers is the number of commits a checkpoint
// compacted (zero otherwise); Time is the author's unix seconds; Changes carries
// each field's delta already rendered to display strings. Entries are returned
// oldest-first; a caller applies reverse and limit.
type HistoryEntry struct {
	SHA     model.SHA
	Author  model.Actor
	Time    int64
	Lamport model.Lamport
	Kind    string
	Covers  int
	Changes []FieldChange
}

// FieldChange is one field's delta in a HistoryEntry, rendered to display
// strings. A scalar change carries From and To (nil when the field was unset on
// that side); a set change carries the Added and Removed elements, each a stable
// human string sorted by that string. Exactly one of the two shapes is
// populated per change.
type FieldChange struct {
	Field   string
	From    *string
	To      *string
	Added   []string
	Removed []string
}

// History returns the edit trail of the entity with the given id: one
// HistoryEntry per commit in linearization order (oldest first), each with the
// fields that commit changed. It resolves id across every kind, so the id need
// not name its kind; a missing id fails with ErrNotFound and an id ambiguous
// across kinds fails with *AmbiguousKindsError. Bookkeeping-only and idempotent
// commits are dropped from the trail. The caller applies any reverse or limit.
func (c *Client) History(ctx context.Context, id model.EntityID) ([]HistoryEntry, error) {
	kind, _, err := c.ResolveEntity(ctx, string(id))
	if err != nil {
		return nil, err
	}
	steps, err := c.s.History(ctx, refs.For(kind, id))
	if err != nil {
		return nil, err
	}
	entries, err := trail.Entries(steps)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, len(entries))
	for i, e := range entries {
		out[i] = newHistoryEntry(e)
	}
	return out, nil
}

func newHistoryEntry(e trail.Entry) HistoryEntry {
	changes := make([]FieldChange, len(e.Changes))
	for i, ch := range e.Changes {
		if ch.Scalar {
			changes[i] = FieldChange{Field: ch.Field, From: render.OptString(formatTrailScalar(ch.Field, ch.From)), To: render.OptString(formatTrailScalar(ch.Field, ch.To))}
		} else {
			changes[i] = FieldChange{Field: ch.Field, Added: formatTrailSet(ch.Field, ch.Added), Removed: formatTrailSet(ch.Field, ch.Removed)}
		}
	}
	return HistoryEntry{
		SHA:     e.Commit.SHA,
		Author:  e.Commit.Author,
		Time:    e.Commit.AuthorTime,
		Lamport: e.Commit.Pack.Lamport,
		Kind:    e.Kind,
		Covers:  e.Covers,
		Changes: changes,
	}
}

// timeFields are unix-seconds scalars rendered as RFC3339 UTC in the trail.
var timeFields = map[string]bool{
	"verified_at": true,
	"started_at":  true,
	"closed_at":   true,
	"stale_at":    true,
	"start_date":  true,
	"end_date":    true,
}

// formatTrailScalar renders a scalar trail value to its history string: "" for a
// nil (unset) field, RFC3339 UTC for a time field, else the plain value.
func formatTrailScalar(field string, v any) string {
	if v == nil {
		return ""
	}
	if timeFields[field] {
		if n, ok := v.(float64); ok {
			if n == 0 {
				return ""
			}
			return render.RFC3339(int64(n))
		}
	}
	return scalarString(v)
}

// formatTrailElement renders one set element to a stable, human string: a string
// element verbatim, a known object element (anchor, comment, log entry,
// criterion) summarized, any other object as compact JSON.
func formatTrailElement(field string, v any) string {
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
	case "steps":
		return fmt.Sprintf("%q", scalarString(m["text"]))
	case "runs":
		return fmt.Sprintf("run by %s [%s]", scalarString(m["runner"]), scalarString(m["status"]))
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// formatTrailSet renders a set field's elements to sorted history strings,
// preserving the trail's former formatted-string ordering; it returns nil for an
// empty set so the JSON DTO omits it.
func formatTrailSet(field string, elems []any) []string {
	if len(elems) == 0 {
		return nil
	}
	out := make([]string, len(elems))
	for i, e := range elems {
		out[i] = formatTrailElement(field, e)
	}
	sort.Strings(out)
	return out
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
