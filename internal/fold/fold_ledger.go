package fold

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type ledgerFolder struct {
	l            model.Ledger
	labels       map[string]bool
	anchors      map[model.Anchor]bool
	rows         []model.LedgerRow
	rowPositions map[string]int
}

func newLedgerFolder() *ledgerFolder {
	return &ledgerFolder{labels: map[string]bool{}, anchors: map[model.Anchor]bool{}, rowPositions: map[string]int{}}
}

func foldLedger(ordered []model.PackCommit, m mode) (model.Ledger, error) {
	return run[model.Ledger](ordered, newLedgerFolder(), m)
}

func (f *ledgerFolder) fresh(sha model.SHA, createdAt int64) {
	f.l = model.Ledger{ID: model.EntityID(sha), CreatedAt: createdAt, Comments: []model.Comment{}}
	f.rows = []model.LedgerRow{}
	clear(f.rowPositions)
}

func (f *ledgerFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Ledger)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-ledger folded as a ledger", ErrKindMismatch)
	}
	f.l = seed
	f.l.Comments = slices.Clone(seed.Comments)
	f.l.Columns = slices.Clone(seed.Columns)
	f.rows = cloneRows(seed.Rows)
	f.indexRows()
	for _, l := range seed.Labels {
		f.labels[l] = true
	}
	for _, a := range seed.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *ledgerFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateLedger)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a ledger", ErrKindMismatch, op.OpKind())
	}
	f.l.Title, f.l.Description = o.Title, o.Description
	f.l.Columns = slices.Clone(o.Columns)
	f.l.Author = author
	f.l.Status = model.LedgerActive
	for _, l := range o.Labels {
		f.labels[l] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *ledgerFolder) apply(op model.Op, c model.PackCommit) bool {
	if applyLabel(f.labels, op) || applyAnchor(f.anchors, op) || applyComment(&f.l.Comments, op, c) {
		return true
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.l.Title = o.Title
	case model.SetDescription:
		f.l.Description = o.Description
	case model.SetColumns:
		f.l.Columns = slices.Clone(o.Columns)
	case model.SetLedgerStatus:
		applyLedgerStatus(&f.l, o.Status, c.AuthorTime)
	case model.UpsertRow:
		f.upsert(o, c)
	case model.RemoveRow:
		if i, ok := f.rowPositions[o.Key]; ok {
			f.rows = slices.Delete(f.rows, i, i+1)
			f.indexRows()
		}
	case model.DeleteNote:
		f.l.Deleted = true
	default:
		return false
	}
	return true
}

func (f *ledgerFolder) upsert(o model.UpsertRow, c model.PackCommit) {
	i, ok := f.rowPositions[o.Key]
	if !ok {
		f.rowPositions[o.Key] = len(f.rows)
		f.rows = append(f.rows, model.LedgerRow{
			Key:       o.Key,
			Fields:    maps.Clone(nonNilFields(o.Fields)),
			Position:  o.Position,
			UpdatedAt: c.AuthorTime,
			UpdatedBy: c.Author,
		})
		return
	}
	if o.Replace {
		f.rows[i].Fields = maps.Clone(nonNilFields(o.Fields))
	} else {
		maps.Copy(f.rows[i].Fields, o.Fields)
	}
	if o.Position != "" {
		f.rows[i].Position = o.Position
	}
	f.rows[i].UpdatedAt = c.AuthorTime
	f.rows[i].UpdatedBy = c.Author
}

func (f *ledgerFolder) touch(c model.PackCommit) {
	f.l.UpdatedAt = c.AuthorTime
}

func (f *ledgerFolder) finalize(head model.SHA, skipped int) model.Ledger {
	f.l.Labels = sortedKeys(f.labels)
	f.l.Anchors = sortedAnchorsNil(f.anchors)
	slices.SortFunc(f.rows, func(a, b model.LedgerRow) int {
		if c := cmp.Compare(a.Position, b.Position); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	f.indexRows()
	f.l.Rows = f.rows
	if f.l.Columns == nil {
		f.l.Columns = []string{}
	}
	f.l.Head = head
	f.l.SkippedOps = skipped
	return f.l
}

func applyLedgerStatus(l *model.Ledger, status model.LedgerStatus, at int64) {
	l.Status = status
	switch status {
	case model.LedgerArchived:
		l.ArchivedAt = at
	case model.LedgerActive:
		l.ArchivedAt = 0
	}
}

func (f *ledgerFolder) indexRows() {
	clear(f.rowPositions)
	for i := len(f.rows) - 1; i >= 0; i-- {
		f.rowPositions[f.rows[i].Key] = i
	}
}

func nonNilFields(fields map[string]string) map[string]string {
	if fields == nil {
		return map[string]string{}
	}
	return fields
}

// cloneRows deep-copies a seeded checkpoint's rows. slices.Clone alone is not
// enough: each row's Fields map would stay shared with the checkpoint State,
// and fold.History re-folds prefixes over the same decoded chain, so an
// in-place field merge in one prefix fold would corrupt the seed for the next.
func cloneRows(rows []model.LedgerRow) []model.LedgerRow {
	out := slices.Clone(rows)
	for i := range out {
		out[i].Fields = maps.Clone(nonNilFields(out[i].Fields))
	}
	return out
}
