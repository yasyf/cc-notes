package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/lfs"
)

// FetchAttachment makes attachment object oid of size bytes present in the
// local LFS store, downloading it from the first sync remote whose LFS
// endpoint serves it when this clone does not hold it yet, and recording it as
// one that endpoint holds. Content no remote serves wraps lfs.ErrObjectMissing
// with each remote's failure.
func (s *Store) FetchAttachment(ctx context.Context, oid string, size int64) error {
	content := s.LFS()
	if content.Has(oid) {
		return nil
	}
	remotes, err := SyncRemotes(ctx, s.RecordsGit)
	if err != nil {
		return err
	}
	failures := make([]error, 0, len(remotes))
	for _, remote := range remotes {
		err := s.fetchAttachmentFrom(ctx, content, remote, oid, size)
		if err == nil {
			return nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", remote, err))
	}
	return fmt.Errorf("%s: %w: %w", oid, lfs.ErrObjectMissing, errors.Join(failures...))
}

func (s *Store) fetchAttachmentFrom(ctx context.Context, content lfs.Store, remote, oid string, size int64) error {
	ep, err := lfs.Discover(ctx, s.RecordsGit, remote)
	if err != nil {
		return err
	}
	c, err := lfs.NewClient(ctx, s.RecordsGit, ep, "download")
	if err != nil {
		return err
	}
	if _, err := c.Download(ctx, content, []lfs.Object{{OID: oid, Size: size}}); err != nil {
		return err
	}
	return s.RecordUploadedAttachments(ep.Href, []string{oid})
}
