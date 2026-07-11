package fold

import (
	"fmt"

	"github.com/yasyf/cc-notes/model"
)

type noteFolder struct {
	note        model.Note
	tags        map[string]bool
	anchors     map[model.Anchor]bool
	superseded  map[model.EntityID]bool
	attachments map[string]model.Attachment
	witness     []model.AnchorWitness
}

func newNoteFolder() *noteFolder {
	return &noteFolder{
		tags:        map[string]bool{},
		anchors:     map[model.Anchor]bool{},
		superseded:  map[model.EntityID]bool{},
		attachments: map[string]model.Attachment{},
	}
}

func foldNote(ordered []model.PackCommit) (model.Note, error) {
	return run[model.Note](ordered, newNoteFolder())
}

func (f *noteFolder) fresh(sha model.SHA, createdAt int64) {
	f.note = model.Note{ID: model.EntityID(sha), CreatedAt: createdAt}
}

func (f *noteFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Note)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a task folded as a note", ErrKindMismatch)
	}
	f.note = seed
	for _, t := range seed.Tags {
		f.tags[t] = true
	}
	for _, a := range seed.Anchors {
		f.anchors[a] = true
	}
	for _, id := range seed.SupersededBy {
		f.superseded[id] = true
	}
	for _, a := range seed.Attachments {
		f.attachments[a.Name] = a
	}
	f.witness = seed.Witness
	return nil
}

func (f *noteFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateNote)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a note", ErrKindMismatch, op.OpKind())
	}
	f.note.Title, f.note.Body, f.note.Author = o.Title, o.Body, author
	for _, t := range o.Tags {
		f.tags[t] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *noteFolder) apply(op model.Op, c model.PackCommit) error {
	if applyTag(f.tags, op) || applyAnchor(f.anchors, op) ||
		applySupersede(f.superseded, op) || applyAttachment(f.attachments, op) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.note.Title = o.Title
	case model.SetBody:
		f.note.Body = o.Body
	case model.DeleteNote:
		f.note.Deleted = true
	case model.VerifyNote:
		f.note.VerifiedAt = c.AuthorTime
		f.note.VerifiedBy = c.Author
		f.note.VerifiedCommit = o.VerifiedCommit
		f.witness = o.Witness
		f.note.StaleAt, f.note.StaleBy, f.note.StaleReason = 0, "", ""
	case model.MarkStale:
		f.note.StaleAt, f.note.StaleBy, f.note.StaleReason = c.AuthorTime, c.Author, o.Reason
	case model.ClearStale:
		f.note.StaleAt, f.note.StaleBy, f.note.StaleReason = 0, "", ""
	default:
		return fmt.Errorf("%w: %s on a note", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *noteFolder) touch(c model.PackCommit) {
	f.note.UpdatedAt = c.AuthorTime
}

func (f *noteFolder) finalize(head model.SHA) model.Note {
	f.note.Tags = sortedKeys(f.tags)
	f.note.Anchors = sortedAnchors(f.anchors)
	f.note.SupersededBy = sortedKeys(f.superseded)
	f.note.Attachments = sortedAttachments(f.attachments)
	f.note.Witness = f.witness
	f.note.Head = head
	return f.note
}
