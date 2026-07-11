// Package render holds pure formatting helpers shared across the cc-notes
// output surfaces: RFC3339 timestamp stamps, optional time and string
// rendering, entity-id and sha string lists, empty-not-nil slice
// normalization, anchor value extraction, and short wire ids.
package render

import (
	"time"

	"github.com/yasyf/cc-notes/model"
)

// RFC3339 renders a Unix timestamp as an RFC3339 UTC string.
func RFC3339(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) }

// OptTime renders ts as an RFC3339 UTC pointer, or nil when ts is zero.
func OptTime(ts int64) *string {
	if ts == 0 {
		return nil
	}
	s := RFC3339(ts)
	return &s
}

// OptTimeString renders ts as an RFC3339 UTC string, or "" when ts is zero.
func OptTimeString(ts int64) string {
	if ts == 0 {
		return ""
	}
	return RFC3339(ts)
}

// OptString returns a pointer to s, or nil when s is empty.
func OptString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// IDStrings renders entity ids as their full string forms.
func IDStrings(ids []model.EntityID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// SHAStrings renders shas as their full string forms.
func SHAStrings(shas []model.SHA) []string {
	out := make([]string, 0, len(shas))
	for _, s := range shas {
		out = append(out, string(s))
	}
	return out
}

// EmptyNotNil returns items unchanged, or an empty non-nil slice when items is
// nil, so JSON serializes an empty array rather than null.
func EmptyNotNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// AnchorValues extracts the values of anchors matching kind, in order.
func AnchorValues(anchors []model.Anchor, kind model.AnchorKind) []string {
	var values []string
	for _, a := range anchors {
		if a.Kind == kind {
			values = append(values, a.Value)
		}
	}
	return values
}

// ShortWireID clamps an opaque runbook wire id (step or run) to its 7-char
// display prefix, tolerating ids shorter than 7 — a pack synced from another
// client may carry one. EntityID.Short slices unconditionally and is only safe
// for full-length entity shas.
func ShortWireID(id string) string {
	if len(id) < 7 {
		return id
	}
	return id[:7]
}
