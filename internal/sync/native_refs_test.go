package sync_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

func TestSyncUsesNativeRefEnumeration(t *testing.T) {
	for _, mode := range []string{"loose", "packed", "bound"} {
		t.Run(mode, func(t *testing.T) {
			var a, b *store.Store
			if mode == "bound" {
				f := newBoundSyncFixture(t)
				var err error
				a, err = store.Open(f.source)
				if err != nil {
					t.Fatal(err)
				}
				ownedSource := a
				t.Cleanup(func() { _ = ownedSource.Close() })
				b = f.thinStore
			} else {
				bare := gittest.InitBare(t)
				a = clone(t, bare, "Alice", "alice@example.com")
				b = clone(t, bare, "Bob", "bob@example.com")
			}
			note := createNote(t, a, "shared note")
			createTask(t, a, "shared task", "main")
			sync(t, a)
			sync(t, b)
			if mode == "packed" {
				gittest.Git(t, b.RecordsGit.Dir, "pack-refs", "--all")
			}
			ref := refs.For(model.KindNote, note.ID)
			tracking := "refs/cc-notes-sync/origin/" + strings.TrimPrefix(ref, "refs/cc-notes/")
			gittest.Git(t, b.RecordsGit.Dir, "symbolic-ref", "refs/cc-notes/notes/symbolic", ref)
			gittest.Git(t, b.RecordsGit.Dir, "symbolic-ref", "refs/cc-notes-sync/origin/notes/symbolic", tracking)
			for _, name := range []string{ref, tracking} {
				path := filepath.Join(b.RecordsCommonDir(), filepath.FromSlash(name+".lock"))
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("ignored lock"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "bound" {
				t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "unrelated.git"))
				t.Setenv("GIT_COMMON_DIR", filepath.Join(t.TempDir(), "unrelated-common.git"))
			}
			trace := filepath.Join(t.TempDir(), "git-trace.json")
			t.Setenv("GIT_TRACE2_EVENT", trace)
			if got, want := sync(t, b), (ccsync.Report{Rounds: 1}); got != want {
				t.Fatalf("unchanged sync report = %+v, want %+v", got, want)
			}
			calls := refEnumerationStarts(t, trace)
			t.Logf("sync local ref enumeration commands = %d", calls)
			if calls != 0 {
				t.Fatalf("sync started %d local ref enumeration commands, want 0", calls)
			}
		})
	}
}

func refEnumerationStarts(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	calls := 0
	for {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			return calls
		} else if err != nil {
			t.Fatal(err)
		}
		if event.Event == "start" && slices.Contains(event.Argv, "for-each-ref") {
			calls++
		}
	}
}

func TestSyncRejectsEmptySelectedRefs(t *testing.T) {
	for _, namespace := range []string{"refs/cc-notes/", "refs/cc-notes-sync/origin/"} {
		t.Run(namespace, func(t *testing.T) {
			bare := gittest.InitBare(t)
			s := clone(t, bare, "Alice", "alice@example.com")
			task := createTask(t, s, "temporary task", "main")
			gittest.Git(t, s.RecordsGit.Dir, "update-ref", "-d", refs.For(model.KindTask, task.ID))
			path := filepath.Join(s.RecordsCommonDir(), filepath.FromSlash(namespace+"tasks/"+string(task.ID)))
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if ignored, err := s.RecordsGit.Refs(t.Context(), namespace); err != nil || len(ignored) != 0 {
				t.Fatalf("git ref listing = %v, %v, want the empty ref omitted", ignored, err)
			}
			_, err := ccsync.Sync(t.Context(), s, "origin", false)
			if !errors.Is(err, dotgit.ErrEmptyRefFile) {
				t.Fatalf("sync with an empty selected ref = %v, want dotgit.ErrEmptyRefFile", err)
			}
			if got := ccRefs(t, bare); len(got) != 0 {
				t.Fatalf("failed sync published refs = %v, want none", got)
			}
		})
	}
}
