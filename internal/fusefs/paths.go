package fusefs

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/yasyf/cc-notes/model"
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

// Node is one parsed mount path: the roots (Root, NotesDir, DocsDir, LogsDir,
// RunbooksDir, TasksRoot, SprintsDir, ProjectsDir), the flat editable entity
// files (NoteFile, DocFile, LogFile, TaskFile, SprintFile, ProjectFile), the
// read-only flat RunbookFile, and the read-only nested browse tree of sprints
// and projects whose task leaves are symlinks to the flat files.
type Node interface {
	node()
}

// Root is the mount root.
type Root struct{}

// NotesDir is the /notes directory.
type NotesDir struct{}

// DocsDir is the /docs directory.
type DocsDir struct{}

// LogsDir is the /logs directory.
type LogsDir struct{}

// RunbooksDir is the /runbooks directory.
type RunbooksDir struct{}

// TasksRoot is the /tasks directory.
type TasksRoot struct{}

// NoteFile is a note file under /notes, keyed by its short id prefix so a
// stale slug still resolves.
type NoteFile struct {
	ShortID string
}

// DocFile is a doc file under /docs, keyed by its short id prefix so a stale
// slug still resolves.
type DocFile struct {
	ShortID string
}

// LogFile is a log file under /logs, keyed by its short id prefix so a stale
// slug still resolves.
type LogFile struct {
	ShortID string
}

// RunbookFile is a read-only runbook file under /runbooks, keyed by its short
// id prefix so a stale slug still resolves.
type RunbookFile struct {
	ShortID string
}

// TaskFile is a task file directly under /tasks, keyed by its short id
// prefix. Branch is a folded attribute, not part of the path.
type TaskFile struct {
	ShortID string
}

// SprintsDir is the /sprints directory.
type SprintsDir struct{}

// ProjectsDir is the /projects directory.
type ProjectsDir struct{}

// SprintFile is a flat editable sprint file directly under /sprints, keyed by
// its short id prefix.
type SprintFile struct {
	ShortID string
}

// ProjectFile is a flat editable project file directly under /projects, keyed
// by its short id prefix.
type ProjectFile struct {
	ShortID string
}

// ProjectBrowseDir is the read-only /projects/<p> browse directory for one
// project, holding its sprints/ and tasks/ subtrees.
type ProjectBrowseDir struct {
	ProjShort string
}

// ProjectSprintsDir is /projects/<p>/sprints, listing the project's sprints as
// browse subdirectories.
type ProjectSprintsDir struct {
	ProjShort string
}

// ProjectSprintDir is /projects/<p>/sprints/<s>, one of a project's sprints.
type ProjectSprintDir struct {
	ProjShort   string
	SprintShort string
}

// ProjectSprintTasksDir is /projects/<p>/sprints/<s>/tasks, listing that
// sprint's tasks as symlinks to the flat /tasks files.
type ProjectSprintTasksDir struct {
	ProjShort   string
	SprintShort string
}

// ProjectSprintTaskLink is /projects/<p>/sprints/<s>/tasks/<t>.json, a symlink
// to the flat /tasks/<t>.json file.
type ProjectSprintTaskLink struct {
	ProjShort   string
	SprintShort string
	TaskShort   string
}

// ProjectTasksDir is /projects/<p>/tasks, listing the project's tasks as
// symlinks to the flat /tasks files.
type ProjectTasksDir struct {
	ProjShort string
}

// ProjectTaskLink is /projects/<p>/tasks/<t>.json, a symlink to the flat
// /tasks/<t>.json file.
type ProjectTaskLink struct {
	ProjShort string
	TaskShort string
}

// SprintBrowseDir is the read-only /sprints/<s> browse directory for one
// sprint, holding its tasks/ subtree.
type SprintBrowseDir struct {
	SprintShort string
}

// SprintTasksDir is /sprints/<s>/tasks, listing the sprint's tasks as symlinks
// to the flat /tasks files.
type SprintTasksDir struct {
	SprintShort string
}

// SprintTaskLink is /sprints/<s>/tasks/<t>.json, a symlink to the flat
// /tasks/<t>.json file.
type SprintTaskLink struct {
	SprintShort string
	TaskShort   string
}

// AttachmentsDir is the read-only /attachments directory, listing every
// attachment-bearing note, doc, and log by short id.
type AttachmentsDir struct{}

// AttachmentEntityDir is /attachments/<short>, one attachment-bearing note,
// doc, or log keyed by its short id.
type AttachmentEntityDir struct {
	EntityShort string
}

// AttachmentFile is /attachments/<short>/<name>, one attachment's content
// served read-only straight from the local LFS store.
type AttachmentFile struct {
	EntityShort string
	Name        string
}

func (Root) node()                  {}
func (NotesDir) node()              {}
func (DocsDir) node()               {}
func (LogsDir) node()               {}
func (RunbooksDir) node()           {}
func (TasksRoot) node()             {}
func (NoteFile) node()              {}
func (DocFile) node()               {}
func (LogFile) node()               {}
func (RunbookFile) node()           {}
func (TaskFile) node()              {}
func (SprintsDir) node()            {}
func (ProjectsDir) node()           {}
func (SprintFile) node()            {}
func (ProjectFile) node()           {}
func (ProjectBrowseDir) node()      {}
func (ProjectSprintsDir) node()     {}
func (ProjectSprintDir) node()      {}
func (ProjectSprintTasksDir) node() {}
func (ProjectSprintTaskLink) node() {}
func (ProjectTasksDir) node()       {}
func (ProjectTaskLink) node()       {}
func (SprintBrowseDir) node()       {}
func (SprintTasksDir) node()        {}
func (SprintTaskLink) node()        {}
func (AttachmentsDir) node()        {}
func (AttachmentEntityDir) node()   {}
func (AttachmentFile) node()        {}

// NoteFilename names a note file "<short7>-<slug>.md", dropping the slug
// part when the title yields none.
func NoteFilename(n model.Note) string {
	if s := slug(n.Title); s != "" {
		return n.ID.Short() + "-" + s + ".md"
	}
	return n.ID.Short() + ".md"
}

// DocFilename names a doc file "<short7>-<slug>.md", dropping the slug part
// when the title yields none.
func DocFilename(d model.Doc) string {
	if s := slug(d.Title); s != "" {
		return d.ID.Short() + "-" + s + ".md"
	}
	return d.ID.Short() + ".md"
}

// LogFilename names a log file "<short7>-<slug>.md", dropping the slug part
// when the title yields none.
func LogFilename(l model.Log) string {
	if s := slug(l.Title); s != "" {
		return l.ID.Short() + "-" + s + ".md"
	}
	return l.ID.Short() + ".md"
}

// RunbookFilename names a runbook file "<short7>-<slug>.md", dropping the slug
// part when the title yields none.
func RunbookFilename(r model.Runbook) string {
	if s := slug(r.Title); s != "" {
		return r.ID.Short() + "-" + s + ".md"
	}
	return r.ID.Short() + ".md"
}

// TaskFilename names a task file "<short7>.json".
func TaskFilename(t model.Task) string { return t.ID.Short() + ".json" }

// SprintFilename names a sprint file "<short7>.json".
func SprintFilename(s model.Sprint) string { return s.ID.Short() + ".json" }

// ProjectFilename names a project file "<short7>.json".
func ProjectFilename(p model.Project) string { return p.ID.Short() + ".json" }

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

// ParsePath decodes an absolute mount path into its syntactic Node. Notes,
// docs, logs, runbooks, and tasks are flat: a ".md" name under /notes is a
// NoteFile, a ".md" name under /docs is a DocFile, a ".md" name under /logs is
// a LogFile, a ".md" name under /runbooks is a RunbookFile, and a ".json" name
// under /tasks is a TaskFile, all keyed by short id. Paths outside the tree
// shape fail with ErrPath.
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
	case "docs":
		switch len(tail) {
		case 0:
			return DocsDir{}, nil
		case 1:
			shortID, ok := ShortIDOf(tail[0])
			if !ok || !strings.HasSuffix(tail[0], ".md") {
				return nil, fmt.Errorf("%w: %q", ErrPath, path)
			}
			return DocFile{ShortID: shortID}, nil
		default:
			return nil, fmt.Errorf("%w: docs do not nest: %q", ErrPath, path)
		}
	case "logs":
		switch len(tail) {
		case 0:
			return LogsDir{}, nil
		case 1:
			shortID, ok := ShortIDOf(tail[0])
			if !ok || !strings.HasSuffix(tail[0], ".md") {
				return nil, fmt.Errorf("%w: %q", ErrPath, path)
			}
			return LogFile{ShortID: shortID}, nil
		default:
			return nil, fmt.Errorf("%w: logs do not nest: %q", ErrPath, path)
		}
	case "runbooks":
		switch len(tail) {
		case 0:
			return RunbooksDir{}, nil
		case 1:
			shortID, ok := ShortIDOf(tail[0])
			if !ok || !strings.HasSuffix(tail[0], ".md") {
				return nil, fmt.Errorf("%w: %q", ErrPath, path)
			}
			return RunbookFile{ShortID: shortID}, nil
		default:
			return nil, fmt.Errorf("%w: runbooks do not nest: %q", ErrPath, path)
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
	case "sprints":
		return parseSprints(path, tail)
	case "projects":
		return parseProjects(path, tail)
	case "attachments":
		switch len(tail) {
		case 0:
			return AttachmentsDir{}, nil
		case 1:
			short, ok := shortIDDir(tail[0])
			if !ok {
				return nil, errPath(path)
			}
			return AttachmentEntityDir{EntityShort: short}, nil
		case 2:
			short, ok := shortIDDir(tail[0])
			if !ok {
				return nil, errPath(path)
			}
			return AttachmentFile{EntityShort: short, Name: tail[1]}, nil
		default:
			return nil, fmt.Errorf("%w: attachments hold no subdirectories: %q", ErrPath, path)
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrPath, path)
	}
}

// parseSprints decodes the /sprints subtree: the flat "<short7>.json" sprint
// file, the "<short7>" browse directory, and the nested tasks/ symlink leaves.
// It is purely syntactic — membership of <t> in <s> is enforced by fs.go.
func parseSprints(full string, tail []string) (Node, error) {
	switch len(tail) {
	case 0:
		return SprintsDir{}, nil
	case 1:
		if strings.HasSuffix(tail[0], ".json") {
			shortID, ok := ShortIDOf(tail[0])
			if !ok {
				return nil, errPath(full)
			}
			return SprintFile{ShortID: shortID}, nil
		}
		sprint, ok := shortIDDir(tail[0])
		if !ok {
			return nil, errPath(full)
		}
		return SprintBrowseDir{SprintShort: sprint}, nil
	}
	sprint, ok := shortIDDir(tail[0])
	if !ok || tail[1] != "tasks" {
		return nil, errPath(full)
	}
	switch len(tail) {
	case 2:
		return SprintTasksDir{SprintShort: sprint}, nil
	case 3:
		task, ok := taskLinkID(tail[2])
		if !ok {
			return nil, errPath(full)
		}
		return SprintTaskLink{SprintShort: sprint, TaskShort: task}, nil
	}
	return nil, errPath(full)
}

// parseProjects decodes the /projects subtree: the flat "<short7>.json" project
// file, the "<short7>" browse directory, and the nested sprints/ and tasks/
// branches whose symlink leaves point at the flat task files. It is purely
// syntactic — membership of <s> in <p> and <t> in <s>/<p> is enforced by fs.go.
func parseProjects(full string, tail []string) (Node, error) {
	switch len(tail) {
	case 0:
		return ProjectsDir{}, nil
	case 1:
		if strings.HasSuffix(tail[0], ".json") {
			shortID, ok := ShortIDOf(tail[0])
			if !ok {
				return nil, errPath(full)
			}
			return ProjectFile{ShortID: shortID}, nil
		}
		proj, ok := shortIDDir(tail[0])
		if !ok {
			return nil, errPath(full)
		}
		return ProjectBrowseDir{ProjShort: proj}, nil
	}
	proj, ok := shortIDDir(tail[0])
	if !ok {
		return nil, errPath(full)
	}
	switch tail[1] {
	case "tasks":
		switch len(tail) {
		case 2:
			return ProjectTasksDir{ProjShort: proj}, nil
		case 3:
			task, ok := taskLinkID(tail[2])
			if !ok {
				return nil, errPath(full)
			}
			return ProjectTaskLink{ProjShort: proj, TaskShort: task}, nil
		}
	case "sprints":
		switch len(tail) {
		case 2:
			return ProjectSprintsDir{ProjShort: proj}, nil
		case 3, 4, 5:
			sprint, ok := shortIDDir(tail[2])
			if !ok {
				return nil, errPath(full)
			}
			if len(tail) == 3 {
				return ProjectSprintDir{ProjShort: proj, SprintShort: sprint}, nil
			}
			if tail[3] != "tasks" {
				return nil, errPath(full)
			}
			if len(tail) == 4 {
				return ProjectSprintTasksDir{ProjShort: proj, SprintShort: sprint}, nil
			}
			task, ok := taskLinkID(tail[4])
			if !ok {
				return nil, errPath(full)
			}
			return ProjectSprintTaskLink{ProjShort: proj, SprintShort: sprint, TaskShort: task}, nil
		}
	}
	return nil, errPath(full)
}

// SymlinkTarget returns the relative symlink target from an absolute link path
// to a flat file flatRel under the mount root. It emits one "../" per path
// component in path.Dir(linkPath) — enough to climb back to the root — then
// flatRel. For /projects/<p>/sprints/<s>/tasks/<t>.json with flatRel
// "tasks/<t>.json" it returns "../../../../../tasks/<t>.json".
func SymlinkTarget(linkPath, flatRel string) string {
	return strings.Repeat("../", strings.Count(path.Dir(linkPath), "/")) + flatRel
}

// shortIDDir validates a bare browse-directory component: a non-empty run of
// hex with no suffix, naming a project or sprint subtree. The ".json" flat-file
// names are matched before this, so a browse dir never collides with a file.
func shortIDDir(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	for i := range len(name) {
		if !isHex(name[i]) {
			return "", false
		}
	}
	return name, true
}

// taskLinkID extracts the task short id from a "<short7>.json" symlink leaf.
func taskLinkID(name string) (string, bool) {
	shortID, ok := ShortIDOf(name)
	if !ok || !strings.HasSuffix(name, ".json") {
		return "", false
	}
	return shortID, true
}

func errPath(full string) error {
	return fmt.Errorf("%w: %q", ErrPath, full)
}
