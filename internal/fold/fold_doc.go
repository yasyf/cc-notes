package fold

import (
	"fmt"

	"github.com/yasyf/cc-notes/model"
)

type docFolder struct {
	doc         model.Doc
	tags        map[string]bool
	anchors     map[model.Anchor]bool
	superseded  map[model.EntityID]bool
	attachments map[string]model.Attachment
	witness     []model.AnchorWitness
}

func newDocFolder() *docFolder {
	return &docFolder{
		tags:        map[string]bool{},
		anchors:     map[model.Anchor]bool{},
		superseded:  map[model.EntityID]bool{},
		attachments: map[string]model.Attachment{},
	}
}

func foldDoc(ordered []model.PackCommit) (model.Doc, error) {
	return run[model.Doc](ordered, newDocFolder())
}

func (f *docFolder) fresh(sha model.SHA, createdAt int64) {
	f.doc = model.Doc{ID: model.EntityID(sha), CreatedAt: createdAt}
}

func (f *docFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Doc)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-doc folded as a doc", ErrKindMismatch)
	}
	f.doc = seed
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

func (f *docFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateDoc)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a doc", ErrKindMismatch, op.OpKind())
	}
	f.doc.Title, f.doc.Body, f.doc.When, f.doc.Author = o.Title, o.Body, o.When, author
	for _, t := range o.Tags {
		f.tags[t] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *docFolder) apply(op model.Op, c model.PackCommit) error {
	if applyTag(f.tags, op) || applyAnchor(f.anchors, op) ||
		applySupersede(f.superseded, op) || applyAttachment(f.attachments, op) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.doc.Title = o.Title
	case model.SetBody:
		f.doc.Body = o.Body
	case model.SetWhen:
		f.doc.When = o.When
	case model.DeleteNote:
		f.doc.Deleted = true
	case model.VerifyNote:
		f.doc.VerifiedAt = c.AuthorTime
		f.doc.VerifiedBy = c.Author
		f.doc.VerifiedCommit = o.VerifiedCommit
		f.witness = o.Witness
		f.doc.StaleAt, f.doc.StaleBy, f.doc.StaleReason = 0, "", ""
	case model.MarkStale:
		f.doc.StaleAt, f.doc.StaleBy, f.doc.StaleReason = c.AuthorTime, c.Author, o.Reason
	case model.ClearStale:
		f.doc.StaleAt, f.doc.StaleBy, f.doc.StaleReason = 0, "", ""
	default:
		return fmt.Errorf("%w: %s on a doc", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *docFolder) touch(c model.PackCommit) {
	f.doc.UpdatedAt = c.AuthorTime
}

func (f *docFolder) finalize(head model.SHA) model.Doc {
	f.doc.Tags = sortedKeys(f.tags)
	f.doc.Anchors = sortedAnchors(f.anchors)
	f.doc.SupersededBy = sortedKeys(f.superseded)
	f.doc.Attachments = sortedAttachments(f.attachments)
	f.doc.Witness = f.witness
	f.doc.Head = head
	return f.doc
}
