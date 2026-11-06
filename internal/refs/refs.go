// Package refs defines the cc-notes ref naming scheme: pure build and parse
// functions with no git access. Notes live at refs/cc-notes/notes/<id>, tasks
// at refs/cc-notes/tasks/<id>, sprints at refs/cc-notes/sprints/<id>, projects
// at refs/cc-notes/projects/<id>, docs at refs/cc-notes/docs/<id>, logs at
// refs/cc-notes/logs/<id>, runbooks at refs/cc-notes/runbooks/<id>,
// investigations at refs/cc-notes/investigations/<id>, plans at
// refs/cc-notes/plans/<id>, and answers at refs/cc-notes/answers/<id>, all flat
// — the entity id is the only component after the namespace, and a task's
// branch is a folded attribute, not part of its ref name. Sync-tracking refs shadow the namespace under
// refs/cc-notes-sync/<remote>/, outside refs/cc-notes/ so the wildcard push
// refspec never republishes them.
package refs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

const (
	namespace     = "refs/cc-notes/"
	syncNamespace = "refs/cc-notes-sync/"
)

// Namespace is the ref prefix holding every cc-notes entity — notes, tasks,
// sprints, projects, docs, logs, runbooks, investigations, plans, and answers —
// including the trailing slash. Listing it enumerates the whole entity set; it
// never matches the refs/cc-notes-sync/ tracking refs.
const Namespace = namespace

// PushRefspec is the plain-push refspec cc-notes installs on a wired remote:
// every entity ref, never forced, so a diverged ref resolves through sync's
// union merge instead of a clobber.
const PushRefspec = namespace + "*:" + namespace + "*"

// OldFetchRefspec is the pre-fix same-namespace force-mirror. A plain git
// fetch --prune uses it to force-clobber a diverged canonical entity ref and to
// prune a locally-created, not-yet-synced one; Install rewrites it to
// FetchRefspec so a plain fetch touches only the tracking namespace.
const OldFetchRefspec = "+" + namespace + "*:" + namespace + "*"

// FetchRefspec is the plain-fetch refspec Install writes for remote: it
// force-mirrors the remote's entity refs into the per-remote tracking
// namespace Sync converges from — refs/cc-notes-sync/<remote>/ — never into
// the canonical refs/cc-notes/ namespace. So fetch.prune can only prune
// tracking copies, which mirror the remote by definition, never a
// locally-created, not-yet-synced canonical ref. It is byte-for-byte the
// refspec Sync fetches with, so a plain fetch pre-populates exactly the
// tracking refs Sync then folds into canonical refs via compare-and-swap.
func FetchRefspec(remote string) string {
	return "+" + namespace + "*:" + syncNamespace + remote + "/*"
}

// CheckpointNamespace holds the fold checkpoints of entity refs. It sits
// outside Namespace, so the installed refspecs, plain git push and fetch, and
// cc-notes releases that predate it never fetch, push, or parse one.
const CheckpointNamespace = "refs/cc-notes-checkpoints/"

// CheckpointOf maps an entity ref, or a prefix of entity refs, to the
// checkpoint ref or prefix that mirrors it under CheckpointNamespace.
func CheckpointOf(ref string) string {
	return CheckpointNamespace + strings.TrimPrefix(ref, namespace)
}

// CheckpointTracking maps a checkpoint ref, or a prefix of them, to the ref or
// prefix a sync fetches remote's copy into, so a local checkpoint that covers
// more than the remote's survives the fetch.
func CheckpointTracking(remote, checkpoint string) string {
	return "refs/cc-notes-checkpoints-sync/" + remote + "/" + strings.TrimPrefix(checkpoint, CheckpointNamespace)
}

var (
	// ErrNotCCNotes reports a ref outside the cc-notes namespaces.
	ErrNotCCNotes = errors.New("not a cc-notes ref")
	// ErrMalformed reports a ref inside the cc-notes namespace that does not
	// match the naming scheme.
	ErrMalformed = errors.New("malformed cc-notes ref")
)

// roots maps each entity kind to its ref namespace root, trailing slash
// included. The root strings are ref-namespace layout — part of the storage
// format — so they are frozen; changing one strands existing entities. Root and
// For build ref names from this table and Parse reverse-maps it, so it is the
// single source of the kind-to-namespace binding. It must cover exactly
// model.Kinds(), asserted by TestRootsCoverKinds.
var roots = map[model.Kind]string{
	model.KindNote:          namespace + "notes/",
	model.KindTask:          namespace + "tasks/",
	model.KindSprint:        namespace + "sprints/",
	model.KindProject:       namespace + "projects/",
	model.KindDoc:           namespace + "docs/",
	model.KindLog:           namespace + "logs/",
	model.KindRunbook:       namespace + "runbooks/",
	model.KindInvestigation: namespace + "investigations/",
	model.KindPlan:          namespace + "plans/",
	model.KindAnswer:        namespace + "answers/",
	model.KindLedger:        namespace + "ledgers/",
}

// kindBySegment reverses roots by ref path segment (the plural namespace token,
// e.g. "notes") for Parse.
var kindBySegment = func() map[string]model.Kind {
	m := make(map[string]model.Kind, len(roots))
	for k, root := range roots {
		seg := strings.TrimSuffix(strings.TrimPrefix(root, namespace), "/")
		m[seg] = k
	}
	return m
}()

// Ref is one parsed cc-notes ref name.
type Ref struct {
	Kind model.Kind
	ID   model.EntityID
}

// Root returns the ref namespace holding every entity of kind, trailing slash
// included: refs/cc-notes/<segment>/. It panics on a kind with no root, a
// programmer error the registry cannot express.
func Root(kind model.Kind) string {
	root, ok := roots[kind]
	if !ok {
		panic(fmt.Sprintf("refs: no root for kind %q", kind))
	}
	return root
}

// For returns the ref name for the entity of kind with the given id.
func For(kind model.Kind, id model.EntityID) string {
	return Root(kind) + string(id)
}

// Parse decodes a cc-notes ref name. The id is the only component after the
// notes/, tasks/, sprints/, projects/, docs/, logs/, runbooks/,
// investigations/, plans/, or answers/ namespace. It returns ErrNotCCNotes for refs
// outside refs/cc-notes/ and ErrMalformed for anything that does not match the
// scheme, including ids that are not 40 or 64 lowercase hex characters.
func Parse(ref string) (Ref, error) {
	rest, ok := strings.CutPrefix(ref, namespace)
	if !ok {
		return Ref{}, fmt.Errorf("%w: %q", ErrNotCCNotes, ref)
	}
	seg, tail, ok := strings.Cut(rest, "/")
	if !ok || tail == "" {
		return Ref{}, fmt.Errorf("%w: missing id in %q", ErrMalformed, ref)
	}
	kind, ok := kindBySegment[seg]
	if !ok {
		return Ref{}, fmt.Errorf("%w: unknown namespace %q in %q", ErrMalformed, seg, ref)
	}
	if strings.ContainsRune(tail, '/') {
		return Ref{}, fmt.Errorf("%w: nested components in %s ref %q", ErrMalformed, kind, ref)
	}
	if !validID(tail) {
		return Ref{}, fmt.Errorf("%w: id %q in %q", ErrMalformed, tail, ref)
	}
	return Ref{Kind: kind, ID: model.EntityID(tail)}, nil
}

// DirectChild reports whether ref names an immediate child of prefix: the
// non-empty remainder after the prefix contains no further slash.
func DirectChild(prefix, ref string) bool {
	rest, ok := strings.CutPrefix(ref, prefix)
	return ok && rest != "" && !strings.ContainsRune(rest, '/')
}

// Tracking maps a cc-notes ref to its sync-tracking ref for remote:
// refs/cc-notes-sync/<remote>/ plus the suffix after refs/cc-notes/. It
// returns ErrNotCCNotes if ref is outside refs/cc-notes/.
func Tracking(remote, ref string) (string, error) {
	rest, ok := strings.CutPrefix(ref, namespace)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNotCCNotes, ref)
	}
	return syncNamespace + remote + "/" + rest, nil
}

// ParseTracking inverts Tracking: it splits a sync-tracking ref into the
// remote name and the refs/cc-notes/ ref it shadows. It is a pure namespace
// transform — validate the returned ref with Parse. It returns ErrNotCCNotes
// for refs outside refs/cc-notes-sync/ and ErrMalformed when the remote or
// suffix is missing.
func ParseTracking(tracking string) (remote, ref string, err error) {
	rest, ok := strings.CutPrefix(tracking, syncNamespace)
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrNotCCNotes, tracking)
	}
	remote, suffix, ok := strings.Cut(rest, "/")
	if !ok || remote == "" || suffix == "" {
		return "", "", fmt.Errorf("%w: tracking ref %q", ErrMalformed, tracking)
	}
	return remote, namespace + suffix, nil
}

func validID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
