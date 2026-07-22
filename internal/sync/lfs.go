package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/store"
)

// client returns the LFS client for operation ("upload" or "download"),
// discovering the remote's endpoint once per run and building one client per
// operation — ssh grants are per-operation, and a Client is not safe for
// concurrent use, which the single-goroutine transfer phases respect.
func (e *engine) client(ctx context.Context, operation string) (*lfs.Client, error) {
	if c, ok := e.clients[operation]; ok {
		return c, nil
	}
	if !e.endpointSet {
		ep, err := lfs.Discover(ctx, e.store.Git, e.remote)
		if err != nil {
			return nil, err
		}
		e.endpoint, e.endpointSet = ep, true
	}
	c, err := lfs.NewClient(ctx, e.store.Git, e.endpoint, operation)
	if err != nil {
		return nil, err
	}
	if e.clients == nil {
		e.clients = map[string]*lfs.Client{}
	}
	e.clients[operation] = c
	return c, nil
}

// uploadAttachments pushes every referenced, locally-present attachment
// object before the ref push — the objects-before-refs invariant: a ref
// visible on the remote never references content the server lacks. The
// batch reply decides what the server already has, so re-upload is a
// natural no-op, and the referenced-∧-present intersection keeps merged-in
// remote refs whose content was never downloaded out of the upload set. Any
// failure — including an LFS-less remote — blocks the push. A repository
// referencing no attachments makes no LFS request at all.
func (e *engine) uploadAttachments(ctx context.Context) error {
	referenced, err := e.store.ReferencedAttachments(ctx)
	if err != nil {
		return fmt.Errorf("upload attachments: %w", err)
	}
	if len(referenced) == 0 {
		return nil
	}
	content := e.store.LFS()
	present := make([]store.ReferencedObject, 0, len(referenced))
	for _, obj := range referenced {
		if content.Has(obj.OID) {
			present = append(present, obj)
		}
	}
	if len(present) == 0 {
		return nil
	}
	c, err := e.client(ctx, "upload")
	if err != nil {
		return transferError("upload", err, present)
	}
	uploaded, err := c.Upload(ctx, content, transferObjects(present))
	e.uploaded += uploaded
	if err != nil {
		return transferError("upload", err, present)
	}
	return nil
}

// downloadAttachments fetches every referenced object missing locally, after
// the push loop converges: refs land before content on the fetch side, so a
// missing object is a first-class local state and an LFS outage never blocks
// publishing refs. The referenced scan is always full, never scoped to what
// this run changed, so a download an earlier sync never finished heals here.
// A repository referencing no attachments makes no LFS request at all.
func (e *engine) downloadAttachments(ctx context.Context) error {
	referenced, err := e.store.ReferencedAttachments(ctx)
	if err != nil {
		return fmt.Errorf("download attachments: %w", err)
	}
	if len(referenced) == 0 {
		return nil
	}
	content := e.store.LFS()
	missing := make([]store.ReferencedObject, 0, len(referenced))
	for _, obj := range referenced {
		if !content.Has(obj.OID) {
			missing = append(missing, obj)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	c, err := e.client(ctx, "download")
	if err != nil {
		return transferError("download", err, missing)
	}
	downloaded, err := c.Download(ctx, content, transferObjects(missing))
	e.downloaded += downloaded
	if err != nil {
		return transferError("download", err, missing)
	}
	return nil
}

// transferObjects projects the scan result onto the batch API's object list.
func transferObjects(objects []store.ReferencedObject) []lfs.Object {
	out := make([]lfs.Object, len(objects))
	for i, obj := range objects {
		out[i] = lfs.Object{OID: obj.OID, Size: obj.Size}
	}
	return out
}

// transferError rewraps a transfer failure with the per-entity detail the
// operator acts on. An unsupported endpoint implicates every candidate
// object; per-object batch errors implicate exactly the failed oids. Each
// implicated oid names the entities referencing it and the remediation:
// remove the attachment and sync again. A download 404 additionally names
// its usual cause — plain `git push` publishes refs/cc-notes/* through the
// installed wildcard refspec without uploading LFS content; only
// `cc-notes sync` uploads objects before refs.
func transferError(operation string, err error, candidates []store.ReferencedObject) error {
	var b strings.Builder
	switch objErrs := objectErrors(err); {
	case errors.Is(err, lfs.ErrUnsupported):
		for _, obj := range candidates {
			writeUses(&b, obj)
		}
		b.WriteString("\nremove the attachments as above and run `cc-notes sync`, or point the remote at an LFS-capable server (`git config lfs.url <url>`)")
	case len(objErrs) > 0:
		byOID := make(map[string]store.ReferencedObject, len(candidates))
		for _, obj := range candidates {
			byOID[obj.OID] = obj
		}
		notFound := false
		for _, oe := range objErrs {
			notFound = notFound || oe.Code == 404
			writeUses(&b, byOID[oe.OID])
		}
		if operation == "download" && notFound {
			b.WriteString("\na plain `git push` publishes refs/cc-notes/* without uploading attachment content — only `cc-notes sync` uploads objects before refs; ask the publisher to run `cc-notes sync`, or remove the attachments as above and run `cc-notes sync`")
		}
	}
	return fmt.Errorf("%s attachments: %w%s", operation, err, b.String())
}

// writeUses appends one line per use of obj: the entity, the attachment
// name, and the exact command that removes the reference.
func writeUses(b *strings.Builder, obj store.ReferencedObject) {
	for _, use := range obj.Uses {
		fmt.Fprintf(b, "\n  oid %s referenced by %s %s attachment %q — remove with `cc-notes %s edit %s --rm-attachment %q`",
			obj.OID, use.Kind, use.Entity.Short(), use.Name, use.Kind, use.Entity.Short(), use.Name)
	}
}

// objectErrors collects every *lfs.ObjectError in err's tree — Upload and
// Download aggregate per-object failures with errors.Join, whose flat
// Unwrap() []error errors.As alone cannot exhaust.
func objectErrors(err error) []*lfs.ObjectError {
	var out []*lfs.ObjectError
	var visit func(error)
	visit = func(e error) {
		if e == nil {
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
			return
		}
		var oe *lfs.ObjectError
		if errors.As(e, &oe) {
			out = append(out, oe)
		}
	}
	visit(err)
	return out
}
