package notes

import (
	"context"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// AnswerReview pairs a flagged answer with its review verdict.
type AnswerReview struct {
	Answer  model.Answer
	Verdict Verdict
}

// CreateAnswer roots an answer from spec, born verified against current HEAD,
// mirroring CreateNote: spec.Title is the question and spec.Body the chosen
// answer.
func (c *Client) CreateAnswer(ctx context.Context, spec NoteSpec) (model.Answer, bool, error) {
	commits, err := c.resolveCommits(ctx, spec.Anchors.Commits)
	if err != nil {
		return model.Answer{}, false, err
	}
	anchors := buildAnchors(AnchorSpec{Commits: commits, Paths: spec.Anchors.Paths, Dirs: spec.Anchors.Dirs, Branches: spec.Anchors.Branches})
	ops := make([]model.Op, 0, 1+len(spec.Attachments))
	ops = append(ops, model.CreateAnswer{Nonce: model.NewNonce(), Title: spec.Title, Body: spec.Body, Tags: spec.Tags, Anchors: anchors})
	ops = append(ops, attachmentAddOps(spec.Attachments)...)
	snap, reused, err := c.createDocument(ctx, ops)
	if err != nil {
		return model.Answer{}, false, err
	}
	return snap.(model.Answer), reused, nil
}

// EditAnswer applies the mask to the answer, mirroring EditNote.
func (c *Client) EditAnswer(ctx context.Context, id model.EntityID, edit NoteEdit) (model.Answer, error) {
	if edit.empty() {
		return model.Answer{}, ErrEmptyEdit
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Answer{}, err
	}
	answer, err := c.Answer(ctx, id)
	if err != nil {
		return model.Answer{}, err
	}
	if err := checkAttachmentCollisions(answer.Attachments, edit.Attachments); err != nil {
		return model.Answer{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Body != nil {
		ops = append(ops, model.SetBody{Body: *edit.Body})
	}
	ops = appendEditOps(ops, edit.AddTags, edit.RemoveTags, addAnchors, edit.RemoveAnchors, edit.RemoveAttachments, edit.Attachments, edit.ReplaceAttachments)
	return c.appendAnswer(ctx, id, ops)
}

// RemoveAnswer tombstones the answer, returning the folded snapshot.
func (c *Client) RemoveAnswer(ctx context.Context, id model.EntityID) (model.Answer, error) {
	return c.appendAnswer(ctx, id, []model.Op{model.DeleteNote{}})
}

// VerifyAnswer re-witnesses the answer against current HEAD, mirroring
// VerifyNote.
func (c *Client) VerifyAnswer(ctx context.Context, id model.EntityID) (model.Answer, error) {
	answer, err := c.Answer(ctx, id)
	if err != nil {
		return model.Answer{}, err
	}
	snap, err := c.reVerify(ctx, answer)
	if err != nil {
		return model.Answer{}, err
	}
	return snap.(model.Answer), nil
}

// SupersedeAnswer records that the answer by replaces id, mirroring
// SupersedeNote.
func (c *Client) SupersedeAnswer(ctx context.Context, id, by model.EntityID) (model.Answer, error) {
	if _, err := c.Answer(ctx, by); err != nil {
		return model.Answer{}, err
	}
	return c.appendAnswer(ctx, id, []model.Op{model.AddSupersededBy{ID: by}})
}

// UnsupersedeAnswer clears the edge recording that by replaces id.
func (c *Client) UnsupersedeAnswer(ctx context.Context, id, by model.EntityID) (model.Answer, error) {
	if _, err := c.Answer(ctx, by); err != nil {
		return model.Answer{}, err
	}
	return c.appendAnswer(ctx, id, []model.Op{model.RemoveSupersededBy{ID: by}})
}

// ExpireAnswer flags the answer out-of-date with an optional reason.
func (c *Client) ExpireAnswer(ctx context.Context, id model.EntityID, reason string) (model.Answer, error) {
	return c.appendAnswer(ctx, id, []model.Op{model.MarkStale{Reason: reason}})
}

// UnexpireAnswer clears the answer's out-of-date flag.
func (c *Client) UnexpireAnswer(ctx context.Context, id model.EntityID) (model.Answer, error) {
	return c.appendAnswer(ctx, id, []model.Op{model.ClearStale{}})
}

func (c *Client) appendAnswer(ctx context.Context, id model.EntityID, ops []model.Op) (model.Answer, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindAnswer, id), ops)
	if err != nil {
		return model.Answer{}, err
	}
	return snap.(model.Answer), nil
}

// Answers folds the answer set the filter selects and returns it in
// UpdatedAt-desc, id-asc order.
func (c *Client) Answers(ctx context.Context, f DocumentFilter) ([]model.Answer, error) {
	answers, err := c.s.ListAnswers(ctx, f.IncludeTombstoned, f.IncludeSuperseded)
	if err != nil {
		return nil, err
	}
	answers = slices.DeleteFunc(answers, func(a model.Answer) bool {
		return !matchesFilter(a.Tags, a.Anchors, f)
	})
	sortDocuments(answers)
	return answers, nil
}

// SearchAnswers ranks the live answer set against query, mirroring SearchNotes:
// the question matches at the title tier and the answer at the body tier.
func (c *Client) SearchAnswers(ctx context.Context, query string, f SearchFilter) ([]model.Answer, error) {
	answers, err := c.s.ListAnswers(ctx, false, false)
	if err != nil {
		return nil, err
	}
	return rankDocuments(answers, query, f, answerRanker), nil
}

var answerRanker = documentRanker[model.Answer]{
	tags:    func(a model.Answer) []string { return a.Tags },
	author:  func(a model.Answer) string { return string(a.Author) },
	anchors: func(a model.Answer) []model.Anchor { return a.Anchors },
	tier:    func(a model.Answer, q string) int { return textTier(a.Title, a.Tags, []string{a.Body}, q) },
}

// ReviewAnswers folds the answer review set and returns each flagged answer
// with its verdict, mirroring ReviewNotes.
func (c *Client) ReviewAnswers(ctx context.Context, staleAfter time.Duration) ([]AnswerReview, error) {
	all, err := c.s.ListAnswers(ctx, false, true)
	if err != nil {
		return nil, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	exists := existsSet(all, func(a model.Answer) model.EntityID { return a.ID })
	var out []AnswerReview
	for _, a := range all {
		if len(a.SupersededBy) > 0 {
			if supersedeDangling(a.SupersededBy, exists) {
				out = append(out, AnswerReview{Answer: a, Verdict: VerdictDangling})
			}
			continue
		}
		verdict, err := c.verdictOf(ctx, head, freshFromAnswer(a), now, staleAfter, false)
		if err != nil {
			return nil, err
		}
		if verdict != "" {
			out = append(out, AnswerReview{Answer: a, Verdict: verdict})
		}
	}
	return out, nil
}

// AnswerVerdict computes a's single review verdict against live content,
// mirroring NoteVerdict.
func (c *Client) AnswerVerdict(ctx context.Context, a model.Answer, staleAfter time.Duration, worktree bool) (Verdict, error) {
	head, err := c.head(ctx)
	if err != nil {
		return "", err
	}
	return c.verdictOf(ctx, head, freshFromAnswer(a), time.Now(), staleAfter, worktree)
}

// AnswerSuperseders returns the ids of answers that supersede id, sorted.
func (c *Client) AnswerSuperseders(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	all, err := c.s.ListAnswers(ctx, false, true)
	if err != nil {
		return nil, err
	}
	return superseders(all, answerSupersedeEdge, id), nil
}

// AnswerSupersedeHeads walks the answer supersede edge transitively from id,
// mirroring NoteSupersedeHeads.
func (c *Client) AnswerSupersedeHeads(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	all, err := c.s.ListAnswers(ctx, false, true)
	if err != nil {
		return nil, err
	}
	return supersedeHeads(all, answerSupersedeEdge, id), nil
}

func answerSupersedeEdge(a model.Answer) (model.EntityID, []model.EntityID) {
	return a.ID, a.SupersededBy
}

func freshFromAnswer(a model.Answer) freshDocument {
	return freshDocument{Anchors: a.Anchors, Witness: a.Witness, VerifiedAt: a.VerifiedAt, StaleAt: a.StaleAt, SupersededBy: a.SupersededBy}
}
