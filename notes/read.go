package notes

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// Note loads the note with the given id and folds it. A missing entity fails
// with ErrRefNotFound.
func (c *Client) Note(ctx context.Context, id model.EntityID) (model.Note, error) {
	snapshot, err := c.s.Load(ctx, refs.For(model.KindNote, id))
	if err != nil {
		return model.Note{}, err
	}
	return snapshot.(model.Note), nil
}

// Doc loads the doc with the given id and folds it. A missing entity fails with
// ErrRefNotFound.
func (c *Client) Doc(ctx context.Context, id model.EntityID) (model.Doc, error) {
	snapshot, err := c.s.Load(ctx, refs.For(model.KindDoc, id))
	if err != nil {
		return model.Doc{}, err
	}
	return snapshot.(model.Doc), nil
}

// Log loads the log with the given id and folds it. A missing entity fails with
// ErrRefNotFound.
func (c *Client) Log(ctx context.Context, id model.EntityID) (model.Log, error) {
	snapshot, err := c.s.Load(ctx, refs.For(model.KindLog, id))
	if err != nil {
		return model.Log{}, err
	}
	return snapshot.(model.Log), nil
}

// Runbook loads the runbook with the given id and folds it. A missing entity
// fails with ErrRefNotFound.
func (c *Client) Runbook(ctx context.Context, id model.EntityID) (model.Runbook, error) {
	snapshot, err := c.s.Load(ctx, refs.For(model.KindRunbook, id))
	if err != nil {
		return model.Runbook{}, err
	}
	return snapshot.(model.Runbook), nil
}

// Project loads the project with the given id and folds it. A missing entity
// fails with ErrRefNotFound.
func (c *Client) Project(ctx context.Context, id model.EntityID) (model.Project, error) {
	snapshot, err := c.s.Load(ctx, refs.For(model.KindProject, id))
	if err != nil {
		return model.Project{}, err
	}
	return snapshot.(model.Project), nil
}

// Sprint loads the sprint with the given id and folds it. A missing entity
// fails with ErrRefNotFound.
func (c *Client) Sprint(ctx context.Context, id model.EntityID) (model.Sprint, error) {
	snapshot, err := c.s.Load(ctx, refs.For(model.KindSprint, id))
	if err != nil {
		return model.Sprint{}, err
	}
	return snapshot.(model.Sprint), nil
}

// Task loads the task with the given id and folds it. A missing entity fails
// with ErrRefNotFound.
func (c *Client) Task(ctx context.Context, id model.EntityID) (model.Task, error) {
	snapshot, err := c.s.Load(ctx, refs.For(model.KindTask, id))
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// ResolveProject expands a project id prefix to its full EntityID. No match
// fails with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveProject(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, model.KindProject, prefix)
}

// ResolveSprint expands a sprint id prefix to its full EntityID. No match
// fails with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveSprint(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, model.KindSprint, prefix)
}

// ResolveTask expands a task id prefix to its full EntityID. No match fails
// with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveTask(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, model.KindTask, prefix)
}

// ResolveNote expands a note id prefix to its full EntityID. No match fails
// with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveNote(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, model.KindNote, prefix)
}

// ResolveDoc expands a doc id prefix to its full EntityID. No match fails with
// ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveDoc(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, model.KindDoc, prefix)
}

// ResolveLog expands a log id prefix to its full EntityID. No match fails with
// ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveLog(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, model.KindLog, prefix)
}

// ResolveRunbook expands a runbook id prefix to its full EntityID. No match
// fails with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveRunbook(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, model.KindRunbook, prefix)
}

// ResolveEntity expands a kind-agnostic id prefix by resolving it against every
// kind. Ids are globally unique, so at most one kind matches a full id; a
// prefix matching entities in more than one kind fails with an
// *AmbiguousKindsError listing each match, while a prefix ambiguous within a
// single kind surfaces that kind's *AmbiguousError. No match fails with
// ErrNotFound.
func (c *Client) ResolveEntity(ctx context.Context, prefix string) (model.Kind, model.EntityID, error) {
	matched := make([]string, 0, len(model.Kinds()))
	for _, kind := range model.Kinds() {
		ref, err := c.s.Resolve(ctx, kind, prefix)
		switch {
		case err == nil:
			matched = append(matched, ref)
		case errors.Is(err, ErrNotFound):
			continue
		default:
			return "", "", err
		}
	}
	switch len(matched) {
	case 0:
		return "", "", fmt.Errorf("%w: no entity matches %q", ErrNotFound, prefix)
	case 1:
		parsed, err := refs.Parse(matched[0])
		if err != nil {
			return "", "", err
		}
		return parsed.Kind, parsed.ID, nil
	default:
		return "", "", c.ambiguousKinds(ctx, prefix, matched)
	}
}

// ambiguousKinds builds an *AmbiguousKindsError from the refs a cross-kind
// prefix matched, loading each to name its kind, id, and title.
func (c *Client) ambiguousKinds(ctx context.Context, prefix string, matched []string) error {
	matches := make([]KindMatch, 0, len(matched))
	for _, ref := range matched {
		parsed, err := refs.Parse(ref)
		if err != nil {
			return err
		}
		snapshot, err := c.s.Load(ctx, ref)
		if err != nil {
			return err
		}
		matches = append(matches, KindMatch{Kind: parsed.Kind, ID: snapshot.EntityID(), Title: snapshot.Meta().Title})
	}
	return &AmbiguousKindsError{Prefix: prefix, Matches: matches}
}

func (c *Client) resolve(ctx context.Context, kind model.Kind, prefix string) (model.EntityID, error) {
	ref, err := c.s.Resolve(ctx, kind, prefix)
	if err != nil {
		return "", err
	}
	parsed, err := refs.Parse(ref)
	if err != nil {
		return "", err
	}
	return parsed.ID, nil
}
