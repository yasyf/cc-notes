package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// client returns the LFS client for operation ("upload"),
// discovering the remote's endpoint once per run and building one client per
// operation — ssh grants are per-operation, and a Client is not safe for
// concurrent use, which the single-goroutine transfer phases respect.
func (e *engine) client(ctx context.Context, operation string) (*lfs.Client, error) {
	if c, ok := e.clients[operation]; ok {
		return c, nil
	}
	ep, err := e.lfsEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	c, err := lfs.NewClient(ctx, e.store.RecordsGit, ep, operation)
	if err != nil {
		return nil, err
	}
	if e.clients == nil {
		e.clients = map[string]*lfs.Client{}
	}
	e.clients[operation] = c
	return c, nil
}

func (e *engine) lfsEndpoint(ctx context.Context) (lfs.Endpoint, error) {
	if !e.endpointSet {
		ep, err := lfs.Discover(ctx, e.store.RecordsGit, e.remote)
		if err != nil {
			return lfs.Endpoint{}, err
		}
		e.endpoint, e.endpointSet = ep, true
	}
	return e.endpoint, nil
}

// uploadAttachments pushes every referenced, locally-present attachment
// object before the ref push and records in e.blocked the refs the push must
// hold back and in e.uploadErr why — the objects-before-refs invariant: a ref visible on the remote never
// references content the server lacks. The batch reply decides what the
// server already has, so re-upload is a natural no-op; an object the
// endpoint's upload log already confirms skips the batch unless the sync is
// full, and every object a batch confirms joins the log. Only objects this
// clone holds can upload, and a clone holding none the log does not already
// confirm skips the reference scan, so a fresh clone pushes without reading
// every chain. An object only secluded entities reference never uploads. A
// failure holds back only the refs referencing an implicated object — exactly
// the failed oids for per-object errors, every candidate for an endpoint or
// transport failure such as an LFS-less remote — and every other ref still
// pushes; e.uploadErr names each implicated reference. Only a failed reference
// scan returns an error. A repository holding no attachment content makes no
// LFS request at all.
func (e *engine) uploadAttachments(ctx context.Context, secluded map[string]bool) error {
	defer Phase(ctx, "uploading attachments")()
	e.blocked, e.uploadErr = nil, nil
	if !e.holdsAttachmentKinds() {
		return nil
	}
	content := e.store.LFS()
	local, err := content.OIDs()
	if err != nil || len(local) == 0 {
		return err
	}
	ep, epErr := e.lfsEndpoint(ctx)
	if epErr == nil && !e.full {
		confirmed, err := e.store.UploadedAttachments(ep.Href)
		if err != nil {
			return fmt.Errorf("upload attachments: %w", err)
		}
		local = slices.DeleteFunc(local, func(oid string) bool { return confirmed[oid] })
		if len(local) == 0 {
			return nil
		}
	}
	referenced, err := e.referenced(ctx)
	if err != nil {
		return fmt.Errorf("upload attachments: %w", err)
	}
	present := slices.DeleteFunc(referenced, func(obj store.ReferencedObject) bool {
		return !slices.Contains(local, obj.OID) || onlySecluded(obj, secluded)
	})
	if len(present) == 0 {
		return nil
	}
	if epErr != nil {
		e.blocked, e.uploadErr = blockedBy(present, epErr), transferError(epErr, present)
		return nil
	}
	c, err := e.client(ctx, "upload")
	if err == nil {
		var uploaded int
		uploaded, err = c.Upload(ctx, content, transferObjects(present))
		e.uploaded += uploaded
	}
	if err := e.store.RecordUploadedAttachments(ep.Href, confirmedBy(present, err)); err != nil {
		return fmt.Errorf("upload attachments: %w", err)
	}
	if err != nil {
		e.blocked, e.uploadErr = blockedBy(present, err), transferError(err, present)
	}
	return nil
}

func confirmedBy(candidates []store.ReferencedObject, err error) []string {
	objErrs := objectErrors(err)
	if err != nil && len(objErrs) == 0 {
		return nil
	}
	failed := make(map[string]bool, len(objErrs))
	for _, oe := range objErrs {
		failed[oe.OID] = true
	}
	var oids []string
	for _, obj := range candidates {
		if !failed[obj.OID] {
			oids = append(oids, obj.OID)
		}
	}
	return oids
}

// blockedBy returns the refs referencing an object err implicates: the failed
// oids when err carries per-object errors, else every candidate.
func blockedBy(candidates []store.ReferencedObject, err error) map[string]bool {
	implicated := candidates
	if objErrs := objectErrors(err); len(objErrs) > 0 {
		failed := make(map[string]bool, len(objErrs))
		for _, oe := range objErrs {
			failed[oe.OID] = true
		}
		implicated = slices.DeleteFunc(slices.Clone(candidates), func(obj store.ReferencedObject) bool { return !failed[obj.OID] })
	}
	blocked := map[string]bool{}
	for _, obj := range implicated {
		for _, use := range obj.Uses {
			blocked[refs.For(use.Kind, use.Entity)] = true
		}
	}
	return blocked
}

// holdsAttachmentKinds reports whether this run's prefix covers any kind that
// carries attachments.
func (e *engine) holdsAttachmentKinds() bool {
	return slices.ContainsFunc(store.AttachmentKinds[:], func(kind model.Kind) bool {
		root := refs.Root(kind)
		return strings.HasPrefix(root, e.prefix) || strings.HasPrefix(e.prefix, root)
	})
}

func (e *engine) referenced(ctx context.Context) ([]store.ReferencedObject, error) {
	referenced, err := e.store.ReferencedAttachments(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(referenced, func(obj store.ReferencedObject) bool {
		return !slices.ContainsFunc(obj.Uses, func(use store.AttachmentUse) bool {
			return strings.HasPrefix(refs.For(use.Kind, use.Entity), e.prefix)
		})
	}), nil
}

func onlySecluded(obj store.ReferencedObject, secluded map[string]bool) bool {
	for _, use := range obj.Uses {
		if !secluded[refs.For(use.Kind, use.Entity)] {
			return false
		}
	}
	return true
}

// transferObjects projects the scan result onto the batch API's object list.
func transferObjects(objects []store.ReferencedObject) []lfs.Object {
	out := make([]lfs.Object, len(objects))
	for i, obj := range objects {
		out[i] = lfs.Object{OID: obj.OID, Size: obj.Size}
	}
	return out
}

// transferError rewraps an upload failure with the per-entity detail the
// operator acts on. An unsupported endpoint implicates every candidate object;
// per-object batch errors implicate exactly the failed oids. Each implicated
// oid names the entities referencing it and the remediation: remove the
// attachment and sync again.
func transferError(err error, candidates []store.ReferencedObject) error {
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
		for _, oe := range objErrs {
			writeUses(&b, byOID[oe.OID])
		}
		b.WriteString("\nthe entities above stay unpushed and every other ref pushed; the next `cc-notes sync` retries them")
	}
	return fmt.Errorf("upload attachments: %w%s", err, b.String())
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
