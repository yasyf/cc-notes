package fold

import (
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type logFolder struct {
	log         model.Log
	tags        map[string]bool
	anchors     map[model.Anchor]bool
	attachments map[string]model.Attachment
	entries     []model.LogEntry
}

func newLogFolder() *logFolder {
	return &logFolder{
		tags:        map[string]bool{},
		anchors:     map[model.Anchor]bool{},
		attachments: map[string]model.Attachment{},
	}
}

func foldLog(ordered []model.PackCommit) (model.Log, error) {
	return run[model.Log](ordered, newLogFolder())
}

func (f *logFolder) fresh(sha model.SHA, createdAt int64) {
	f.log = model.Log{ID: model.EntityID(sha), CreatedAt: createdAt, Entries: []model.LogEntry{}}
	f.entries = []model.LogEntry{}
}

func (f *logFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Log)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-log folded as a log", ErrKindMismatch)
	}
	f.log = seed
	f.entries = slices.Clone(seed.Entries)
	for _, t := range seed.Tags {
		f.tags[t] = true
	}
	for _, a := range seed.Anchors {
		f.anchors[a] = true
	}
	for _, a := range seed.Attachments {
		f.attachments[a.Name] = a
	}
	return nil
}

func (f *logFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateLog)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a log", ErrKindMismatch, op.OpKind())
	}
	f.log.Title, f.log.Author = o.Title, author
	for _, t := range o.Tags {
		f.tags[t] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *logFolder) apply(op model.Op, c model.PackCommit) error {
	if applyTag(f.tags, op) || applyAnchor(f.anchors, op) || applyAttachment(f.attachments, op) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.log.Title = o.Title
	case model.AppendEntry:
		f.entries = append(f.entries, model.LogEntry{Author: c.Author, TS: c.AuthorTime, Text: o.Text})
	case model.DeleteNote:
		f.log.Deleted = true
	default:
		return fmt.Errorf("%w: %s on a log", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *logFolder) touch(c model.PackCommit) {
	f.log.UpdatedAt = c.AuthorTime
}

func (f *logFolder) finalize(head model.SHA) model.Log {
	f.log.Tags = sortedKeys(f.tags)
	f.log.Anchors = sortedAnchors(f.anchors)
	f.log.Attachments = sortedAttachments(f.attachments)
	if f.entries == nil {
		f.entries = []model.LogEntry{}
	}
	f.log.Entries = f.entries
	f.log.Head = head
	return f.log
}
