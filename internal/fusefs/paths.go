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

// Node is one parsed mount path: Root, NotesDir, TasksRoot, TaskBranchDir,
// NoteFile, or TaskFile.
type Node interface {
	node()
}

// Root is the mount root.
type Root struct{}

// NotesDir is the /notes directory.
type NotesDir struct{}

// TasksRoot is the /tasks directory.
type TasksRoot struct{}

// TaskBranchDir is a directory under /tasks. It is a syntactic candidate:
// the path may name a branch, a parent prefix of nested branches (branch
// feature/login puts a plain directory at /tasks/feature), or both at once
// — only the ref list disambiguates, so the mount resolves Branch against
// the known branches and their prefixes.
type TaskBranchDir struct {
	Branch model.Branch
}

// NoteFile is a note file under /notes, keyed by its short id prefix so a
// stale slug still resolves.
type NoteFile struct {
	ShortID string
}

// TaskFile is a task file under a branch directory. It is the primary
// syntactic reading: a branch may legally contain a ".json"-looking
// component, so the mount resolves ShortID against Branch's live tasks
// first and falls back to reading the whole path as a TaskBranchDir.
type TaskFile struct {
	Branch  model.Branch
	ShortID string
}

func (Root) node()          {}
func (NotesDir) node()      {}
func (TasksRoot) node()     {}
func (TaskBranchDir) node() {}
func (NoteFile) node()      {}
func (TaskFile) node()      {}

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

// ParsePath decodes an absolute mount path into its syntactic Node. Task
// branch directories nest — branch feature/login lives at
// /tasks/feature/login — so a path under /tasks reads as a TaskFile when
// its last component carries a short id and the .json extension, and as a
// TaskBranchDir otherwise; both are candidates the mount must resolve
// against the ref list (see the TaskFile and TaskBranchDir docs). Paths
// outside the tree shape fail with ErrPath.
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
		if len(tail) == 0 {
			return TasksRoot{}, nil
		}
		last := tail[len(tail)-1]
		if shortID, ok := ShortIDOf(last); ok && strings.HasSuffix(last, ".json") && len(tail) > 1 {
			return TaskFile{Branch: model.Branch(strings.Join(tail[:len(tail)-1], "/")), ShortID: shortID}, nil
		}
		return TaskBranchDir{Branch: model.Branch(strings.Join(tail, "/"))}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrPath, path)
	}
}
