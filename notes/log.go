package notes

import (
	"context"
	"errors"
	"slices"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// LogSpec is the input to CreateLog. Title is required. Entry, when non-empty,
// is appended as the log's first entry in a second write after the create.
// Anchors are attached in commit, path, dir, then branch order; commit values
// are resolved to full shas at write time. Attachments are added in the create
// pack, in slice order.
type LogSpec struct {
	Title       string
	Entry       string
	Tags        []string
	Anchors     AnchorSpec
	Attachments []model.Attachment
}

// LogAppend is the input to AppendLog: an entry text with an optional model
// identity, plus attachments. Text may be empty only alongside at least one
// attachment; an empty append with no attachments records nothing and is
// ErrEmptyEdit. An attachment whose name collides with a live one is an
// *AttachmentExistsError unless ReplaceAttachments overwrites it.
type LogAppend struct {
	Text               string
	Model              string
	Attachments        []model.Attachment
	ReplaceAttachments bool
}

// LogEdit is the field mask for EditLog. Entries are append-only, so the mask
// carries no entry field: a nil Title leaves the title untouched, a non-nil
// pointer sets it; the tag and anchor slices apply in order (RemoveAnchors is
// matched verbatim); RemoveAttachments drops attachments by name. An all-empty
// mask is ErrEmptyEdit.
type LogEdit struct {
	Title             *string
	AddTags           []string
	RemoveTags        []string
	AddAnchors        AnchorSpec
	RemoveAnchors     AnchorSpec
	RemoveAttachments []string
}

// empty reports whether the mask sets nothing.
func (e LogEdit) empty() bool {
	return e.Title == nil &&
		len(e.AddTags) == 0 && len(e.RemoveTags) == 0 &&
		e.AddAnchors.isEmpty() && e.RemoveAnchors.isEmpty() &&
		len(e.RemoveAttachments) == 0
}

// LogFilter narrows a log listing. The zero value matches every live log.
// Labels are ANDed; Anchors constrains to logs carrying the given anchor;
// IncludeDeleted widens the set to tombstoned logs.
type LogFilter struct {
	IncludeDeleted bool
	Labels         []string
	Anchors        AnchorFilter
}

// CreateLog roots a log from spec and returns its folded snapshot. A non-empty
// spec.Entry is appended in a second write after the create — the create pack
// carries the log op and the attachment ops but never the first entry, matching
// the two-write shape the store's same-clone dedupe backstop depends on. The
// returned bool reports that Create's best-effort duplicate guard converged on
// an existing log; the first entry is appended to that survivor all the same.
func (c *Client) CreateLog(ctx context.Context, spec LogSpec) (model.Log, bool, error) {
	commits, err := c.resolveCommits(ctx, spec.Anchors.Commits)
	if err != nil {
		return model.Log{}, false, err
	}
	anchors := buildAnchors(AnchorSpec{Commits: commits, Paths: spec.Anchors.Paths, Dirs: spec.Anchors.Dirs, Branches: spec.Anchors.Branches})
	ops := make([]model.Op, 0, 1+len(spec.Attachments))
	ops = append(ops, model.CreateLog{Nonce: model.NewNonce(), Title: spec.Title, Tags: spec.Tags, Anchors: anchors})
	ops = append(ops, attachmentAddOps(spec.Attachments)...)
	snap, err := c.s.Create(ctx, ops)
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return model.Log{}, false, err
	}
	log := snap.(model.Log)
	if spec.Entry == "" {
		return log, reused, nil
	}
	appended, err := c.s.Append(ctx, refs.For(model.KindLog, log.ID), []model.Op{model.AppendEntry{Text: spec.Entry}})
	if err != nil {
		return model.Log{}, false, err
	}
	return appended.(model.Log), reused, nil
}

// AppendLog appends one entry to the log, plus any attachments. Text may be
// empty only alongside at least one attachment; an empty append with no
// attachments is ErrEmptyEdit. Unless ReplaceAttachments is set, an attachment
// whose name collides with a live one is an *AttachmentExistsError. The empty
// check, load, and collision check all run before any write.
func (c *Client) AppendLog(ctx context.Context, id model.EntityID, in LogAppend) (model.Log, error) {
	if in.Text == "" && len(in.Attachments) == 0 {
		return model.Log{}, ErrEmptyEdit
	}
	log, err := c.Log(ctx, id)
	if err != nil {
		return model.Log{}, err
	}
	if !in.ReplaceAttachments {
		if err := checkAttachmentCollisions(log.Attachments, in.Attachments); err != nil {
			return model.Log{}, err
		}
	}
	var ops []model.Op
	if in.Text != "" {
		ops = append(ops, model.AppendEntry{Text: in.Text, Model: in.Model})
	}
	ops = append(ops, attachmentAddOps(in.Attachments)...)
	snap, err := c.s.Append(ctx, refs.For(model.KindLog, id), ops)
	if err != nil {
		return model.Log{}, err
	}
	return snap.(model.Log), nil
}

// EditLog applies the mask to the log. An all-empty mask is ErrEmptyEdit;
// entries are append-only and never touched. AddAnchors' commits are resolved
// first, so a bad revision mutates nothing.
func (c *Client) EditLog(ctx context.Context, id model.EntityID, edit LogEdit) (model.Log, error) {
	if edit.empty() {
		return model.Log{}, ErrEmptyEdit
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Log{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	ops = appendEditOps(ops, edit.AddTags, edit.RemoveTags, addAnchors, edit.RemoveAnchors, edit.RemoveAttachments, nil, nil)
	snap, err := c.s.Append(ctx, refs.For(model.KindLog, id), ops)
	if err != nil {
		return model.Log{}, err
	}
	return snap.(model.Log), nil
}

// RemoveLog tombstones the log, returning the folded snapshot. DeleteNote is a
// soft tombstone — the ref survives, so the log still resolves for show and
// append.
func (c *Client) RemoveLog(ctx context.Context, id model.EntityID) (model.Log, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindLog, id), []model.Op{model.DeleteNote{}})
	if err != nil {
		return model.Log{}, err
	}
	return snap.(model.Log), nil
}

// Logs folds the log set the filter selects and returns it in UpdatedAt-desc,
// id-asc order.
func (c *Client) Logs(ctx context.Context, f LogFilter) ([]model.Log, error) {
	logs, err := c.s.ListLogs(ctx, f.IncludeDeleted)
	if err != nil {
		return nil, err
	}
	logs = slices.DeleteFunc(logs, func(l model.Log) bool {
		return !hasAll(l.Tags, f.Labels) || !matchesAnchorFilter(l.Anchors, f.Anchors)
	})
	sortDocuments(logs)
	return logs, nil
}

// SearchLogs ranks the live log set against query, filtered by the SearchFilter,
// and returns the top results by tier, then UpdatedAt descending, then id
// ascending. A log matches when its title, a tag, or any entry text contains
// query.
func (c *Client) SearchLogs(ctx context.Context, query string, f SearchFilter) ([]model.Log, error) {
	logs, err := c.s.ListLogs(ctx, false)
	if err != nil {
		return nil, err
	}
	return rankDocuments(logs, query, f, logRanker), nil
}

var logRanker = documentRanker[model.Log]{
	tags:    func(l model.Log) []string { return l.Tags },
	author:  func(l model.Log) string { return string(l.Author) },
	anchors: func(l model.Log) []model.Anchor { return l.Anchors },
	tier:    func(l model.Log, q string) int { return textTier(l.Title, l.Tags, logEntryTexts(l), q) },
}

// logEntryTexts is a log's searchable body: the text of each entry, in order.
func logEntryTexts(l model.Log) []string {
	texts := make([]string, len(l.Entries))
	for i, e := range l.Entries {
		texts[i] = e.Text
	}
	return texts
}
