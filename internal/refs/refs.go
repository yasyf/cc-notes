// Package refs defines the cc-notes ref naming scheme: pure build and parse
// functions with no git access. Notes live at refs/cc-notes/notes/<id>;
// tasks live at refs/cc-notes/tasks/<branch>/<id> with the branch embedded
// verbatim — slashes included, since any branch is a valid ref path segment
// by construction (it already exists under refs/heads/). Parsing is
// positional from the right: the last component is always the entity id.
// Sync-tracking refs shadow the namespace under refs/cc-notes-sync/<remote>/,
// outside refs/cc-notes/ so the wildcard push refspec never republishes them.
package refs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/model"
)

const (
	namespace     = "refs/cc-notes/"
	syncNamespace = "refs/cc-notes-sync/"
)

// NotesPrefix is the ref namespace holding all notes, including the trailing
// slash.
const NotesPrefix = namespace + "notes/"

const tasksRoot = namespace + "tasks/"

var (
	// ErrNotCCNotes reports a ref outside the cc-notes namespaces.
	ErrNotCCNotes = errors.New("not a cc-notes ref")
	// ErrEmptyBranch reports a task ref with no branch between the tasks
	// prefix and the entity id.
	ErrEmptyBranch = errors.New("empty branch in task ref")
	// ErrMalformed reports a ref inside the cc-notes namespace that does not
	// match the naming scheme.
	ErrMalformed = errors.New("malformed cc-notes ref")
)

// Kind discriminates the entity namespace a ref belongs to.
type Kind string

// Entity namespaces.
const (
	KindNote Kind = "note"
	KindTask Kind = "task"
)

// Ref is one parsed cc-notes ref name. Branch is empty for notes.
type Ref struct {
	Kind   Kind
	Branch model.Branch
	ID     model.EntityID
}

// Note returns the ref name for the note with the given id.
func Note(id model.EntityID) string { return NotesPrefix + string(id) }

// Task returns the ref name for the task with the given id on the given
// branch, with the branch embedded verbatim.
func Task(branch model.Branch, id model.EntityID) string {
	return TasksPrefix(branch) + string(id)
}

// TasksPrefix returns the ref namespace holding one branch's tasks,
// including the trailing slash.
func TasksPrefix(branch model.Branch) string {
	return tasksRoot + string(branch) + "/"
}

// Parse decodes a cc-notes ref name. Parsing is positional from the right:
// the last component is always the entity id, and for tasks everything
// between the tasks prefix and the id is the branch verbatim. It returns
// ErrNotCCNotes for refs outside refs/cc-notes/, ErrEmptyBranch for a task
// ref with no branch, and ErrMalformed for anything else that does not match
// the scheme, including ids that are not 40 or 64 lowercase hex characters.
func Parse(ref string) (Ref, error) {
	rest, ok := strings.CutPrefix(ref, namespace)
	if !ok {
		return Ref{}, fmt.Errorf("%w: %q", ErrNotCCNotes, ref)
	}
	kind, tail, ok := strings.Cut(rest, "/")
	if !ok || tail == "" {
		return Ref{}, fmt.Errorf("%w: missing id in %q", ErrMalformed, ref)
	}
	switch kind {
	case "notes":
		if strings.ContainsRune(tail, '/') {
			return Ref{}, fmt.Errorf("%w: nested components in note ref %q", ErrMalformed, ref)
		}
		if !validID(tail) {
			return Ref{}, fmt.Errorf("%w: id %q in %q", ErrMalformed, tail, ref)
		}
		return Ref{Kind: KindNote, ID: model.EntityID(tail)}, nil
	case "tasks":
		i := strings.LastIndexByte(tail, '/')
		if i <= 0 {
			return Ref{}, fmt.Errorf("%w: %q", ErrEmptyBranch, ref)
		}
		branch, id := tail[:i], tail[i+1:]
		if !validID(id) {
			return Ref{}, fmt.Errorf("%w: id %q in %q", ErrMalformed, id, ref)
		}
		return Ref{Kind: KindTask, Branch: model.Branch(branch), ID: model.EntityID(id)}, nil
	default:
		return Ref{}, fmt.Errorf("%w: unknown namespace %q in %q", ErrMalformed, kind, ref)
	}
}

// DirectChild reports whether ref names an immediate child of prefix: the
// non-empty remainder after the prefix contains no further slash. Listing a
// branch's tasks with DirectChild(TasksPrefix(branch), ref) excludes
// sub-branch namespaces, so branch "a" does not pick up tasks on "a/b".
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
