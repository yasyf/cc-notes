package notes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// LedgerSpec is the input to CreateLedger. Title is required; the rest are
// optional. Columns is the advisory field order a reader renders rows in. Rows
// are the initial rows, positioned sequentially in slice order. Anchors are
// attached in commit, path, dir, then branch order.
type LedgerSpec struct {
	Title       string
	Description string
	Columns     []string
	Labels      []string
	Rows        []RowSpec
	Anchors     AnchorSpec
}

// RowSpec is one row of a create or a sync: a key and the fields to write.
type RowSpec struct {
	Key    string
	Fields map[string]string
}

// LedgerEdit is the field mask for EditLedger: a nil Title, Description, or
// Columns leaves the field untouched, a non-nil pointer sets it; the label and
// anchor slices apply in order. An all-empty mask is ErrEmptyEdit.
type LedgerEdit struct {
	Title         *string
	Description   *string
	Columns       *[]string
	AddLabels     []string
	RemoveLabels  []string
	AddAnchors    AnchorSpec
	RemoveAnchors AnchorSpec
}

// empty reports whether the mask sets nothing.
func (e LedgerEdit) empty() bool {
	return e.Title == nil && e.Description == nil && e.Columns == nil &&
		len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0 &&
		e.AddAnchors.isEmpty() && e.RemoveAnchors.isEmpty()
}

// ErrDuplicateKey reports a sync whose row set names one key twice — the two
// rows would silently collapse, so the pass is refused before it writes.
var ErrDuplicateKey = errors.New("duplicate row key")

// SyncResult counts what one SyncRows pass changed: rows the ledger had not
// seen, rows whose fields it rewrote, and rows Prune dropped.
type SyncResult struct {
	Added   int
	Updated int
	Removed int
}

// Changed reports whether the pass wrote anything.
func (r SyncResult) Changed() bool { return r.Added+r.Updated+r.Removed > 0 }

// CreateLedger roots a ledger from spec and returns its folded snapshot. The
// create pack carries the ledger op followed by one UpsertRow op per initial
// row, positioned sequentially in slice order. The returned bool reports that
// Create's best-effort duplicate guard converged on an existing ledger.
func (c *Client) CreateLedger(ctx context.Context, spec LedgerSpec) (model.Ledger, bool, error) {
	anchors, err := c.resolveAnchors(ctx, spec.Anchors)
	if err != nil {
		return model.Ledger{}, false, err
	}
	ops := make([]model.Op, 0, 1+len(spec.Rows))
	ops = append(ops, model.CreateLedger{
		Nonce:       model.NewNonce(),
		Title:       spec.Title,
		Description: spec.Description,
		Columns:     spec.Columns,
		Labels:      spec.Labels,
		Anchors:     anchors,
	})
	last := ""
	for _, row := range spec.Rows {
		pos := model.PositionBetween(last, "")
		ops = append(ops, model.UpsertRow{Key: row.Key, Fields: row.Fields, Position: pos})
		last = pos
	}
	snap, err := c.s.Create(ctx, ops)
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return model.Ledger{}, false, err
	}
	return snap.(model.Ledger), reused, nil
}

// LedgerFilter narrows a ledger listing. The zero value matches every active
// ledger. Labels are ANDed; Anchors constrains to ledgers carrying the given
// anchor; IncludeArchived widens the set to archived ledgers.
type LedgerFilter struct {
	IncludeArchived bool
	Labels          []string
	Anchors         AnchorFilter
}

// Ledgers folds the ledger set the filter selects, in store order (creation
// time then id). Tombstoned ledgers are always dropped; archived ledgers are
// dropped unless IncludeArchived is set.
func (c *Client) Ledgers(ctx context.Context, f LedgerFilter) ([]model.Ledger, error) {
	ledgers, err := c.s.ListLedgers(ctx)
	if err != nil {
		return nil, err
	}
	ledgers = slices.DeleteFunc(ledgers, func(l model.Ledger) bool {
		if !f.IncludeArchived && l.Status != model.LedgerActive {
			return true
		}
		return !hasAll(l.Labels, f.Labels) || !matchesAnchorFilter(l.Anchors, f.Anchors)
	})
	return ledgers, nil
}

// ActivateLedger marks the ledger active. A ledger already active is refused
// with a *ConflictError; an archived one is reactivated.
func (c *Client) ActivateLedger(ctx context.Context, id model.EntityID) (model.Ledger, error) {
	return c.setLedgerStatus(ctx, id, model.LedgerActive)
}

// ArchiveLedger marks the ledger archived. A ledger already archived is refused
// with a *ConflictError.
func (c *Client) ArchiveLedger(ctx context.Context, id model.EntityID) (model.Ledger, error) {
	return c.setLedgerStatus(ctx, id, model.LedgerArchived)
}

func (c *Client) setLedgerStatus(ctx context.Context, id model.EntityID, status model.LedgerStatus) (model.Ledger, error) {
	l, err := c.Ledger(ctx, id)
	if err != nil {
		return model.Ledger{}, err
	}
	if l.Status == status {
		return model.Ledger{}, &ConflictError{ID: l.ID, Msg: "already " + string(status)}
	}
	return c.appendLedger(ctx, id, []model.Op{model.SetLedgerStatus{Status: status}})
}

// EditLedger applies the mask to the ledger. An all-empty mask is ErrEmptyEdit;
// the ledger must be active. AddAnchors' commits are resolved first, so a bad
// revision mutates nothing. Ops apply in title, description, columns,
// add-label, remove-label, add-anchor, remove-anchor order.
func (c *Client) EditLedger(ctx context.Context, id model.EntityID, edit LedgerEdit) (model.Ledger, error) {
	if edit.empty() {
		return model.Ledger{}, ErrEmptyEdit
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Ledger{}, err
	}
	if _, err := c.activeLedger(ctx, id); err != nil {
		return model.Ledger{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Description != nil {
		ops = append(ops, model.SetDescription{Description: *edit.Description})
	}
	if edit.Columns != nil {
		ops = append(ops, model.SetColumns{Columns: *edit.Columns})
	}
	for _, lbl := range edit.AddLabels {
		ops = append(ops, model.AddLabel{Label: lbl})
	}
	for _, lbl := range edit.RemoveLabels {
		ops = append(ops, model.RemoveLabel{Label: lbl})
	}
	ops = anchorEditOps(ops, addAnchors, edit.RemoveAnchors)
	return c.appendLedger(ctx, id, ops)
}

// RemoveLedger tombstones the ledger, returning the folded snapshot.
func (c *Client) RemoveLedger(ctx context.Context, id model.EntityID) (model.Ledger, error) {
	return c.appendLedger(ctx, id, []model.Op{model.DeleteNote{}})
}

// CommentLedger appends an operational comment; the ledger must be active.
func (c *Client) CommentLedger(ctx context.Context, id model.EntityID, body string) (model.Ledger, error) {
	if _, err := c.activeLedger(ctx, id); err != nil {
		return model.Ledger{}, err
	}
	return c.appendLedger(ctx, id, []model.Op{model.AddComment{Body: body}})
}

// SetRow writes fields into the row keyed by key, inserting the row when the
// ledger has none. Replace drops the fields the map omits instead of leaving
// them standing. The ledger must be active.
func (c *Client) SetRow(ctx context.Context, id model.EntityID, key string, fields map[string]string, replace bool) (model.Ledger, error) {
	l, err := c.activeLedger(ctx, id)
	if err != nil {
		return model.Ledger{}, err
	}
	op := model.UpsertRow{Key: key, Fields: fields, Replace: replace}
	if RowIndex(l, key) < 0 {
		op.Position = nextPosition(l.Rows)
	}
	return c.appendLedger(ctx, id, []model.Op{op})
}

// RemoveRow removes the row keyed by key. A key the ledger does not carry is
// ErrNotFound. The ledger must be active.
func (c *Client) RemoveRow(ctx context.Context, id model.EntityID, key string) (model.Ledger, error) {
	l, err := c.activeLedger(ctx, id)
	if err != nil {
		return model.Ledger{}, err
	}
	if RowIndex(l, key) < 0 {
		return model.Ledger{}, fmt.Errorf("%w: no row keyed %q", ErrNotFound, key)
	}
	return c.appendLedger(ctx, id, []model.Op{model.RemoveRow{Key: key}})
}

// SyncRows writes a whole row set in one commit, the refresh path: each row's
// fields merge into the row already keyed by it, so fields the caller does not
// name keep their value, and rows the ledger has never seen are appended in
// slice order. Prune additionally removes every row the set omits. The ledger
// must be active. A pass that would change nothing writes nothing and returns
// the ledger unchanged with a zero SyncResult.
func (c *Client) SyncRows(ctx context.Context, id model.EntityID, rows []RowSpec, prune bool) (model.Ledger, SyncResult, error) {
	l, err := c.activeLedger(ctx, id)
	if err != nil {
		return model.Ledger{}, SyncResult{}, err
	}
	var ops []model.Op
	var result SyncResult
	last := lastPosition(l.Rows)
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if seen[row.Key] {
			return model.Ledger{}, SyncResult{}, fmt.Errorf("%w: row key %q appears twice in one sync", ErrDuplicateKey, row.Key)
		}
		seen[row.Key] = true
		i := RowIndex(l, row.Key)
		if i < 0 {
			last = model.PositionBetween(last, "")
			ops = append(ops, model.UpsertRow{Key: row.Key, Fields: row.Fields, Position: last})
			result.Added++
			continue
		}
		if sameFields(l.Rows[i].Fields, row.Fields) {
			continue
		}
		ops = append(ops, model.UpsertRow{Key: row.Key, Fields: row.Fields})
		result.Updated++
	}
	if prune {
		for _, row := range l.Rows {
			if !seen[row.Key] {
				ops = append(ops, model.RemoveRow{Key: row.Key})
				result.Removed++
			}
		}
	}
	if len(ops) == 0 {
		return l, result, nil
	}
	synced, err := c.appendLedger(ctx, id, ops)
	if err != nil {
		return model.Ledger{}, SyncResult{}, err
	}
	return synced, result, nil
}

// SearchLedgers ranks the active ledger set against query, filtered by the
// SearchFilter. A ledger matches when its title, a label, its description, or
// any row key or field value contains query.
func (c *Client) SearchLedgers(ctx context.Context, query string, f SearchFilter) ([]model.Ledger, error) {
	ledgers, err := c.Ledgers(ctx, LedgerFilter{})
	if err != nil {
		return nil, err
	}
	return rankDocuments(ledgers, query, f, ledgerRanker), nil
}

var ledgerRanker = documentRanker[model.Ledger]{
	tags:    func(l model.Ledger) []string { return l.Labels },
	author:  func(l model.Ledger) string { return string(l.Author) },
	anchors: func(l model.Ledger) []model.Anchor { return l.Anchors },
	tier:    func(l model.Ledger, q string) int { return textTier(l.Title, l.Labels, ledgerBodies(l), q) },
}

// ledgerBodies is a ledger's searchable body: the description, then each row's
// key and field values in folded order.
func ledgerBodies(l model.Ledger) []string {
	texts := []string{l.Description}
	for _, row := range l.Rows {
		texts = append(texts, row.Key)
		for _, name := range slices.Sorted(maps.Keys(row.Fields)) {
			texts = append(texts, row.Fields[name])
		}
	}
	return texts
}

// EnsureLedgerActive rejects a write to an archived ledger with a
// *ConflictError; every ledger write but activate and archive is gated by it.
func EnsureLedgerActive(l model.Ledger) error {
	if l.Status == model.LedgerArchived {
		return &ConflictError{ID: l.ID, Msg: "is archived"}
	}
	return nil
}

// RowIndex returns the position of the row keyed by key within the ledger's
// folded row order, or -1.
func RowIndex(l model.Ledger, key string) int {
	for i := range l.Rows {
		if l.Rows[i].Key == key {
			return i
		}
	}
	return -1
}

// MatchRows selects the rows whose fields hold every name/value pair in where.
func MatchRows(l model.Ledger, where map[string]string) []model.LedgerRow {
	out := make([]model.LedgerRow, 0, len(l.Rows))
	for _, row := range l.Rows {
		match := true
		for name, want := range where {
			if row.Fields[name] != want {
				match = false
				break
			}
		}
		if match {
			out = append(out, row)
		}
	}
	return out
}

func (c *Client) activeLedger(ctx context.Context, id model.EntityID) (model.Ledger, error) {
	l, err := c.Ledger(ctx, id)
	if err != nil {
		return model.Ledger{}, err
	}
	if err := EnsureLedgerActive(l); err != nil {
		return model.Ledger{}, err
	}
	return l, nil
}

func (c *Client) appendLedger(ctx context.Context, id model.EntityID, ops []model.Op) (model.Ledger, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindLedger, id), ops)
	if err != nil {
		return model.Ledger{}, err
	}
	return snap.(model.Ledger), nil
}

func lastPosition(rows []model.LedgerRow) string {
	if len(rows) == 0 {
		return ""
	}
	return rows[len(rows)-1].Position
}

func nextPosition(rows []model.LedgerRow) string {
	return model.PositionBetween(lastPosition(rows), "")
}

// sameFields reports whether writing want over have would change nothing: every
// named field already holds that value.
func sameFields(have, want map[string]string) bool {
	for name, value := range want {
		if have[name] != value {
			return false
		}
	}
	return true
}
