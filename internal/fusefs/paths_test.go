package fusefs_test

import (
	"errors"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/model"
)

func TestNoteFilename(t *testing.T) {
	id := model.EntityID("a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0")
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"simple", "Fix the parser", "a1b2c3d-fix-the-parser.md"},
		{"punctuation collapses", "Hello, World! (v2)", "a1b2c3d-hello-world-v2.md"},
		{"digits kept", "v2 2024 roadmap", "a1b2c3d-v2-2024-roadmap.md"},
		{"uppercase lowered", "READ Me", "a1b2c3d-read-me.md"},
		{"unicode-only title", "日本語のメモ", "a1b2c3d.md"},
		{"accents dropped", "Étude no. 5", "a1b2c3d-tude-no-5.md"},
		{"empty title", "", "a1b2c3d.md"},
		{"symbols only", "---???!!!", "a1b2c3d.md"},
		{
			"capped at forty without trailing dash",
			strings.Repeat("abc ", 20),
			"a1b2c3d-" + strings.TrimSuffix(strings.Repeat("abc-", 10), "-") + ".md",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fusefs.NoteFilename(model.Note{ID: id, Title: tc.title})
			if got != tc.want {
				t.Errorf("NoteFilename(%q) = %q, want %q", tc.title, got, tc.want)
			}
			if slug := strings.TrimSuffix(strings.TrimPrefix(got, "a1b2c3d-"), ".md"); len(slug) > 40 {
				t.Errorf("slug %q longer than 40", slug)
			}
		})
	}
}

func TestTaskFilename(t *testing.T) {
	task := model.Task{ID: "0123abcd4567ef890123abcd4567ef890123abcd"}
	if got, want := fusefs.TaskFilename(task), "0123abc.json"; got != want {
		t.Errorf("TaskFilename = %q, want %q", got, want)
	}
}

func TestSprintFilename(t *testing.T) {
	s := model.Sprint{ID: "5555aaaa5555aaaa5555aaaa5555aaaa5555aaaa"}
	if got, want := fusefs.SprintFilename(s), "5555aaa.json"; got != want {
		t.Errorf("SprintFilename = %q, want %q", got, want)
	}
}

func TestProjectFilename(t *testing.T) {
	p := model.Project{ID: "6666dddd6666dddd6666dddd6666dddd6666dddd"}
	if got, want := fusefs.ProjectFilename(p), "6666ddd.json"; got != want {
		t.Errorf("ProjectFilename = %q, want %q", got, want)
	}
}

func TestShortIDOf(t *testing.T) {
	cases := []struct {
		filename string
		want     string
		ok       bool
	}{
		{"a1b2c3d-fix-the-parser.md", "a1b2c3d", true},
		{"a1b2c3d.md", "a1b2c3d", true},
		{"0123abc.json", "0123abc", true},
		{"e.md", "e", true},
		{"deadbeef", "", false},
		{"readme.md", "", false},
		{"ABC1234.md", "", false},
		{"abc1234extra.md", "", false},
		{"-fix.md", "", false},
		{".DS_Store", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			got, ok := fusefs.ShortIDOf(tc.filename)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ShortIDOf(%q) = (%q, %v), want (%q, %v)", tc.filename, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestJunkName(t *testing.T) {
	junk := []string{
		".DS_Store", "._anything", "._", ".Spotlight-V100", ".fseventsd",
		".Trashes", ".hidden", ".metadata_never_index", ".localized",
	}
	for _, name := range junk {
		if !fusefs.JunkName(name) {
			t.Errorf("JunkName(%q) = false, want true", name)
		}
	}
	clean := []string{"a1b2c3d-note.md", ".gitignore", "notes", ".trashes", "DS_Store"}
	for _, name := range clean {
		if fusefs.JunkName(name) {
			t.Errorf("JunkName(%q) = true, want false", name)
		}
	}
}

func TestParsePath(t *testing.T) {
	cases := []struct {
		path string
		want fusefs.Node
	}{
		{"/", fusefs.Root{}},
		{"/notes", fusefs.NotesDir{}},
		{"/tasks", fusefs.TasksRoot{}},
		{"/notes/a1b2c3d-fix-the-parser.md", fusefs.NoteFile{ShortID: "a1b2c3d"}},
		{"/notes/a1b2c3d.md", fusefs.NoteFile{ShortID: "a1b2c3d"}},
		{"/tasks/0123abc.json", fusefs.TaskFile{ShortID: "0123abc"}},
		{"/tasks/0123abc-slug.json", fusefs.TaskFile{ShortID: "0123abc"}},
		// Flat sprint and project dirs and files.
		{"/sprints", fusefs.SprintsDir{}},
		{"/projects", fusefs.ProjectsDir{}},
		{"/sprints/5555aaa.json", fusefs.SprintFile{ShortID: "5555aaa"}},
		{"/projects/6666ddd.json", fusefs.ProjectFile{ShortID: "6666ddd"}},
		// Project browse tree: dirs without .json, task leaves as links.
		{"/projects/6666ddd", fusefs.ProjectBrowseDir{ProjShort: "6666ddd"}},
		{"/projects/6666ddd/tasks", fusefs.ProjectTasksDir{ProjShort: "6666ddd"}},
		{"/projects/6666ddd/tasks/0123abc.json", fusefs.ProjectTaskLink{ProjShort: "6666ddd", TaskShort: "0123abc"}},
		{"/projects/6666ddd/sprints", fusefs.ProjectSprintsDir{ProjShort: "6666ddd"}},
		{"/projects/6666ddd/sprints/5555aaa", fusefs.ProjectSprintDir{ProjShort: "6666ddd", SprintShort: "5555aaa"}},
		{"/projects/6666ddd/sprints/5555aaa/tasks", fusefs.ProjectSprintTasksDir{ProjShort: "6666ddd", SprintShort: "5555aaa"}},
		{"/projects/6666ddd/sprints/5555aaa/tasks/0123abc.json", fusefs.ProjectSprintTaskLink{ProjShort: "6666ddd", SprintShort: "5555aaa", TaskShort: "0123abc"}},
		// Sprint browse tree.
		{"/sprints/5555aaa", fusefs.SprintBrowseDir{SprintShort: "5555aaa"}},
		{"/sprints/5555aaa/tasks", fusefs.SprintTasksDir{SprintShort: "5555aaa"}},
		{"/sprints/5555aaa/tasks/0123abc.json", fusefs.SprintTaskLink{SprintShort: "5555aaa", TaskShort: "0123abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := fusefs.ParsePath(tc.path)
			if err != nil {
				t.Fatalf("ParsePath(%q): %v", tc.path, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParsePath(%q) = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

func TestParsePathErrors(t *testing.T) {
	paths := []string{
		"", "notes", "relative/path", "/unknown", "/notes/", "/tasks/",
		"/notes/readme.md", "/notes/a1b2c3d.json", "/notes/deep/a1b2c3d.md",
		"/tasks//main", "/tasks/./x", "/tasks/../x", "/notes/..",
		// Tasks are flat: a non-id name, a non-.json name, and any nesting
		// under /tasks all fail.
		"/tasks/main", "/tasks/0123abc.md", "/tasks/main/0123abc.json",
		"/tasks/feature/login/0123abc.json",
		// Sprint and project dirs reject trailing slashes, non-hex browse
		// dirs, wrong extensions, unknown branches, and over-deep leaves.
		"/sprints/", "/projects/",
		"/projects/nothex", "/sprints/5555aaa.md", "/sprints/5555aaa.json/tasks",
		"/projects/6666ddd/bogus", "/sprints/5555aaa/bogus",
		"/projects/6666ddd/sprints/5555aaa/bogus",
		"/projects/6666ddd/sprints/5555aaa/tasks/bad.md",
		"/projects/6666ddd/tasks/0123abc.json/extra",
		"/sprints/5555aaa/tasks/0123abc.json/extra",
		"/projects/6666ddd/sprints/5555aaa/tasks/0123abc.json/extra",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			if _, err := fusefs.ParsePath(path); !errors.Is(err, fusefs.ErrPath) {
				t.Fatalf("ParsePath(%q) err %v, want ErrPath", path, err)
			}
		})
	}
}

func TestSymlinkTarget(t *testing.T) {
	cases := []struct {
		link    string
		flatRel string
		want    string
	}{
		{"/projects/6666ddd/tasks/0123abc.json", "tasks/0123abc.json", "../../../tasks/0123abc.json"},
		{"/sprints/5555aaa/tasks/0123abc.json", "tasks/0123abc.json", "../../../tasks/0123abc.json"},
		{"/projects/6666ddd/sprints/5555aaa/tasks/0123abc.json", "tasks/0123abc.json", "../../../../../tasks/0123abc.json"},
	}
	for _, tc := range cases {
		t.Run(tc.link, func(t *testing.T) {
			got := fusefs.SymlinkTarget(tc.link, tc.flatRel)
			if got != tc.want {
				t.Errorf("SymlinkTarget(%q, %q) = %q, want %q", tc.link, tc.flatRel, got, tc.want)
			}
			if resolved := path.Join(path.Dir(tc.link), got); resolved != "/tasks/0123abc.json" {
				t.Errorf("path.Join(dir, %q) = %q, want /tasks/0123abc.json", got, resolved)
			}
		})
	}
}
