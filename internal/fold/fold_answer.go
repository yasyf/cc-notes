package fold

import (
	"fmt"

	"github.com/yasyf/cc-notes/model"
)

type answerFolder struct {
	answer      model.Answer
	tags        map[string]bool
	anchors     map[model.Anchor]bool
	superseded  map[model.EntityID]bool
	attachments map[string]model.Attachment
	witness     []model.AnchorWitness
}

func newAnswerFolder() *answerFolder {
	return &answerFolder{
		tags:        map[string]bool{},
		anchors:     map[model.Anchor]bool{},
		superseded:  map[model.EntityID]bool{},
		attachments: map[string]model.Attachment{},
	}
}

func foldAnswer(ordered []model.PackCommit, m mode) (model.Answer, error) {
	return run[model.Answer](ordered, newAnswerFolder(), m)
}

func (f *answerFolder) fresh(sha model.SHA, createdAt int64) {
	f.answer = model.Answer{ID: model.EntityID(sha), CreatedAt: createdAt}
}

func (f *answerFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Answer)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-answer folded as an answer", ErrKindMismatch)
	}
	f.answer = seed
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

func (f *answerFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateAnswer)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as an answer", ErrKindMismatch, op.OpKind())
	}
	f.answer.Title, f.answer.Body, f.answer.Author = o.Title, o.Body, author
	for _, t := range o.Tags {
		f.tags[t] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *answerFolder) apply(op model.Op, c model.PackCommit) bool {
	if applyTag(f.tags, op) || applyAnchor(f.anchors, op) ||
		applySupersede(f.superseded, op) || applyAttachment(f.attachments, op) {
		return true
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.answer.Title = o.Title
	case model.SetBody:
		f.answer.Body = o.Body
	case model.DeleteNote:
		f.answer.Deleted = true
	case model.VerifyNote:
		f.answer.VerifiedAt = c.AuthorTime
		f.answer.VerifiedBy = c.Author
		f.answer.VerifiedCommit = o.VerifiedCommit
		f.witness = o.Witness
		f.answer.StaleAt, f.answer.StaleBy, f.answer.StaleReason = 0, "", ""
	case model.MarkStale:
		f.answer.StaleAt, f.answer.StaleBy, f.answer.StaleReason = c.AuthorTime, c.Author, o.Reason
	case model.ClearStale:
		f.answer.StaleAt, f.answer.StaleBy, f.answer.StaleReason = 0, "", ""
	default:
		return false
	}
	return true
}

func (f *answerFolder) touch(c model.PackCommit) {
	f.answer.UpdatedAt = c.AuthorTime
}

func (f *answerFolder) finalize(head model.SHA, skipped int) model.Answer {
	f.answer.Tags = sortedKeys(f.tags)
	f.answer.Anchors = sortedAnchors(f.anchors)
	f.answer.SupersededBy = sortedKeys(f.superseded)
	f.answer.Attachments = sortedAttachments(f.attachments)
	f.answer.Witness = f.witness
	f.answer.Head = head
	f.answer.SkippedOps = skipped
	return f.answer
}
