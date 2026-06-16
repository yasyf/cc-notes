package fusefs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/model"
)

// maxSlugLen caps the slug portion of a note filename.
const maxSlugLen = 40

// ErrPath reports a path outside the fusefs tree shape; the mount maps it
// to ENOENT.
var ErrPath = errors.New("no such path")

// junkNames are the macOS metadata files Finder and Spotlight probe for;
// the mount answers them ENOENT without touching the store.
var junkNames = map[string]bool{
	".DS_Store":             true,
	".Spotlight-V100":       true,
	".fseventsd":            true,
	".Trashes":              true,
	".hidden":               true,
	".metadata_never_index": true,
	".localized":            true,
}

// Node is one parsed mount path: Root, NotesDir, TasksRoot, NoteFile, or
// TaskFile.
type Node interface {
	node()
}

// Root is the mount root.
type Root struct{}

// NotesDir is the /notes directory.
type NotesDir struct{}

// TasksRoot is the /tasks directory.
type TasksRoot struct{}

// NoteFile is a note file under /notes, keyed by its short id prefix so a
// stale slug still resolves.
type NoteFile struct {
	ShortID string
}

// TaskFile is a task file directly under /tasks, keyed by its short id
// prefix. Branch is a folded attribute, not part of the path.
type TaskFile struct {
	ShortID string
}

func (Root) node()      {}
func (NotesDir) node()  {}
func (TasksRoot) node() {}
func (NoteFile) node()  {}
func (TaskFile) node()  {}

// NoteFilename names a note file "<short7>-<slug>.md", dropping the slug
// part when the title yields none.
func NoteFilename(n model.Note) string {
	if s := slug(n.Title); s != "" {
		return n.ID.Short() + "-" + s + ".md"
	}
	return n.ID.Short() + ".md"
}

// TaskFilename names a task file "<short7>.json".
func TaskFilename(t model.Task) string { return t.ID.Short() + ".json" }

// slug lowercases the title and joins its [a-z0-9]+ runs with dashes,
// capped at maxSlugLen.
func slug(title string) string {
	var b strings.Builder
	run := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if !run && b.Len() > 0 {
				b.WriteByte('-')
			}
			run = true
			b.WriteRune(r)
		default:
			run = false
		}
		if b.Len() >= maxSlugLen {
			break
		}
	}
	return strings.TrimSuffix(b.String()[:min(b.Len(), maxSlugLen)], "-")
}

// ShortIDOf extracts the leading hex run of a filename, terminated by '-'
// or '.'. It reports false for names with no such run — junk files, slugs
// without an id, names with nothing after the run.
func ShortIDOf(filename string) (string, bool) {
	i := 0
	for i < len(filename) && isHex(filename[i]) {
		i++
	}
	if i == 0 || i == len(filename) || (filename[i] != '-' && filename[i] != '.') {
		return "", false
	}
	return filename[:i], true
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }

// JunkName reports whether name is macOS Finder or Spotlight metadata the
// mount should reject without touching the store.
func JunkName(name string) bool {
	return junkNames[name] || strings.HasPrefix(name, "._")
}

// ParsePath decodes an absolute mount path into its syntactic Node. Notes
// and tasks are flat: a ".md" name under /notes is a NoteFile and a ".json"
// name under /tasks is a TaskFile, both keyed by short id. Paths outside the
// tree shape fail with ErrPath.
func ParsePath(path string) (Node, error) {
	if path == "/" {
		return Root{}, nil
	}
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return nil, fmt.Errorf("%w: %q is not absolute", ErrPath, path)
	}
	parts := strings.Split(rest, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("%w: %q", ErrPath, path)
		}
	}
	head, tail := parts[0], parts[1:]
	switch head {
	case "notes":
		switch len(tail) {
		case 0:
			return NotesDir{}, nil
		case 1:
			shortID, ok := ShortIDOf(tail[0])
			if !ok || !strings.HasSuffix(tail[0], ".md") {
				return nil, fmt.Errorf("%w: %q", ErrPath, path)
			}
			return NoteFile{ShortID: shortID}, nil
		default:
			return nil, fmt.Errorf("%w: notes do not nest: %q", ErrPath, path)
		}
	case "tasks":
		switch len(tail) {
		case 0:
			return TasksRoot{}, nil
		case 1:
			shortID, ok := ShortIDOf(tail[0])
			if !ok || !strings.HasSuffix(tail[0], ".json") {
				return nil, fmt.Errorf("%w: %q", ErrPath, path)
			}
			return TaskFile{ShortID: shortID}, nil
		default:
			return nil, fmt.Errorf("%w: tasks do not nest: %q", ErrPath, path)
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrPath, path)
	}
}
