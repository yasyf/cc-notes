package notes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// AttachmentInfo pairs an attachment with whether its bytes are present in the
// local LFS store.
type AttachmentInfo struct {
	model.Attachment
	Present bool
}

// AttachFile hashes path's content into the local LFS store and returns the
// attachment referencing it, named path's base name. guarded reports that this
// call installed the git-lfs prune guard — set once per repository, on the
// first attach — so the caller can announce it that one time. Attaching is
// offline: content moves to the remote only at sync.
func (c *Client) AttachFile(ctx context.Context, path string) (model.Attachment, bool, error) {
	return c.s.AttachFile(ctx, path)
}

// ResolveAttachable expands an id prefix across the attachment-bearing kinds —
// note, doc, and log — to its kind and full id. No match fails with ErrNotFound;
// a prefix matching more than one kind fails with an *AmbiguousKindsError.
func (c *Client) ResolveAttachable(ctx context.Context, prefix string) (model.Kind, model.EntityID, error) {
	matched := make([]string, 0, 3)
	for _, kind := range []model.Kind{model.KindNote, model.KindDoc, model.KindLog} {
		ref, err := c.s.Resolve(ctx, kind, prefix)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		matched = append(matched, ref)
	}
	switch len(matched) {
	case 0:
		return "", "", fmt.Errorf("%w: no note, doc, or log matches %q", ErrNotFound, prefix)
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

// OpenAttachment loads the entity, finds the attachment named name, and opens
// its bytes. An unknown name fails wrapping ErrNotFound listing the names the
// entity carries; bytes absent from the local LFS store fail with a
// *MissingContentError.
func (c *Client) OpenAttachment(ctx context.Context, kind model.Kind, id model.EntityID, name string) (model.Attachment, io.ReadCloser, error) {
	att, content, err := c.lookupAttachment(ctx, kind, id, name)
	if err != nil {
		return model.Attachment{}, nil, err
	}
	f, err := content.Open(att.OID)
	if errors.Is(err, lfs.ErrObjectMissing) {
		return model.Attachment{}, nil, &MissingContentError{Attachment: att}
	}
	if err != nil {
		return model.Attachment{}, nil, fmt.Errorf("open attachment %s: %w", att.Name, err)
	}
	return att, f, nil
}

// AttachmentPath returns the absolute local object path of the entity's
// attachment named name for a zero-copy read. Bytes absent from the local LFS
// store fail with a *MissingContentError.
func (c *Client) AttachmentPath(ctx context.Context, kind model.Kind, id model.EntityID, name string) (model.Attachment, string, error) {
	att, content, err := c.lookupAttachment(ctx, kind, id, name)
	if err != nil {
		return model.Attachment{}, "", err
	}
	if !content.Has(att.OID) {
		return model.Attachment{}, "", &MissingContentError{Attachment: att}
	}
	return att, content.Path(att.OID), nil
}

// AttachmentInfos probes each attachment for local presence, always returning a
// non-nil slice. The LFS store is opened only when there is something to probe.
func (c *Client) AttachmentInfos(ctx context.Context, atts []model.Attachment) ([]AttachmentInfo, error) {
	out := make([]AttachmentInfo, 0, len(atts))
	if len(atts) == 0 {
		return out, nil
	}
	content, err := c.s.LFS(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range atts {
		out = append(out, AttachmentInfo{Attachment: a, Present: content.Has(a.OID)})
	}
	return out, nil
}

// lookupAttachment loads the entity of kind and id and returns its attachment
// named name plus the repository's local LFS store. An unknown name fails
// wrapping ErrNotFound listing the entity's attachment names.
func (c *Client) lookupAttachment(ctx context.Context, kind model.Kind, id model.EntityID, name string) (model.Attachment, lfs.Store, error) {
	snapshot, err := c.s.Load(ctx, refs.For(kind, id))
	if err != nil {
		return model.Attachment{}, lfs.Store{}, err
	}
	atts := snapshot.Meta().Attachments
	for _, a := range atts {
		if a.Name == name {
			content, err := c.s.LFS(ctx)
			if err != nil {
				return model.Attachment{}, lfs.Store{}, err
			}
			return a, content, nil
		}
	}
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	has := "-"
	if len(names) > 0 {
		has = strings.Join(names, ",")
	}
	return model.Attachment{}, lfs.Store{}, fmt.Errorf("%w: no attachment %q on %s (has: %s)",
		ErrNotFound, name, snapshot.EntityID().Short(), has)
}
