// Package refs defines the cc-notes ref naming scheme: pure build and parse
// functions with no git access. Notes live at refs/cc-notes/notes/<id>, tasks
// at refs/cc-notes/tasks/<id>, sprints at refs/cc-notes/sprints/<id>, and
// projects at refs/cc-notes/projects/<id>, all flat — the entity id is the
// only component after the namespace, and a task's branch is a folded
// attribute, not part of its ref name. Sync-tracking refs shadow the namespace
// under refs/cc-notes-sync/<remote>/, outside refs/cc-notes/ so the wildcard
// push refspec never republishes them.
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
// sprints, and projects — including the trailing slash. Listing it enumerates
// the whole entity set; it never matches the refs/cc-notes-sync/ tracking refs.
const Namespace = namespace

// NotesPrefix is the ref namespace holding all notes, including the trailing
// slash.
const NotesPrefix = namespace + "notes/"

// TasksRoot is the ref namespace holding every task, including the trailing
// slash.
const TasksRoot = namespace + "tasks/"

// SprintsRoot is the ref namespace holding every sprint, including the trailing
// slash.
const SprintsRoot = namespace + "sprints/"

// ProjectsRoot is the ref namespace holding every project, including the
// trailing slash.
const ProjectsRoot = namespace + "projects/"

var (
	// ErrNotCCNotes reports a ref outside the cc-notes namespaces.
	ErrNotCCNotes = errors.New("not a cc-notes ref")
	// ErrMalformed reports a ref inside the cc-notes namespace that does not
	// match the naming scheme.
	ErrMalformed = errors.New("malformed cc-notes ref")
)

// Kind discriminates the entity namespace a ref belongs to.
type Kind string

// Entity namespaces.
const (
	KindNote    Kind = "note"
	KindTask    Kind = "task"
	KindSprint  Kind = "sprint"
	KindProject Kind = "project"
)

// Ref is one parsed cc-notes ref name.
type Ref struct {
	Kind Kind
	ID   model.EntityID
}

// Note returns the ref name for the note with the given id.
func Note(id model.EntityID) string { return NotesPrefix + string(id) }

// Task returns the ref name for the task with the given id.
func Task(id model.EntityID) string { return TasksRoot + string(id) }

// Sprint returns the ref name for the sprint with the given id.
func Sprint(id model.EntityID) string { return SprintsRoot + string(id) }

// Project returns the ref name for the project with the given id.
func Project(id model.EntityID) string { return ProjectsRoot + string(id) }

// Parse decodes a cc-notes ref name. The id is the only component after the
// notes/, tasks/, sprints/, or projects/ namespace. It returns ErrNotCCNotes
// for refs outside refs/cc-notes/ and ErrMalformed for anything that does not
// match the scheme, including ids that are not 40 or 64 lowercase hex
// characters.
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
		if strings.ContainsRune(tail, '/') {
			return Ref{}, fmt.Errorf("%w: nested components in task ref %q", ErrMalformed, ref)
		}
		if !validID(tail) {
			return Ref{}, fmt.Errorf("%w: id %q in %q", ErrMalformed, tail, ref)
		}
		return Ref{Kind: KindTask, ID: model.EntityID(tail)}, nil
	case "sprints":
		if strings.ContainsRune(tail, '/') {
			return Ref{}, fmt.Errorf("%w: nested components in sprint ref %q", ErrMalformed, ref)
		}
		if !validID(tail) {
			return Ref{}, fmt.Errorf("%w: id %q in %q", ErrMalformed, tail, ref)
		}
		return Ref{Kind: KindSprint, ID: model.EntityID(tail)}, nil
	case "projects":
		if strings.ContainsRune(tail, '/') {
			return Ref{}, fmt.Errorf("%w: nested components in project ref %q", ErrMalformed, ref)
		}
		if !validID(tail) {
			return Ref{}, fmt.Errorf("%w: id %q in %q", ErrMalformed, tail, ref)
		}
		return Ref{Kind: KindProject, ID: model.EntityID(tail)}, nil
	default:
		return Ref{}, fmt.Errorf("%w: unknown namespace %q in %q", ErrMalformed, kind, ref)
	}
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
