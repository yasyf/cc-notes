package notes

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func relevantTestExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestRelevantCachedKeepsExecutablePathsIndependent(t *testing.T) {
	if os.Getenv("CCN_RELEVANT_EXECUTABLE_FIXTURE") == "1" {
		dir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		gittest.ScrubEnv(t)
		t.Setenv("HOME", filepath.Join(dir, "home"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
		defer SetRelevantRacyWindow(0)()
		c, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		wantRenders, err := strconv.Atoi(os.Getenv("CCN_RELEVANT_EXPECT_RENDERS"))
		if err != nil {
			t.Fatal(err)
		}
		before, hadEntry := relevantEntryOf(t.Context(), t, c, "svc/handler.go", RelevantFilter{})
		renders := 0
		out, err := c.RelevantCached(t.Context(), "svc/handler.go", RelevantFilter{}, "json", func(entries []RelevantEntry) ([]byte, error) {
			renders++
			if len(entries) != 1 || entries[0].Kind != model.KindNote || entries[0].Note.Title != "handler" {
				t.Fatalf("entries = %+v, want the handler note", entries)
			}
			return []byte("handler\n"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, []byte("handler\n")) || renders != wantRenders {
			t.Fatalf("output = %q renders = %d, want handler\\n and %d renders", out, renders, wantRenders)
		}
		after, ok := relevantEntryOf(t.Context(), t, c, "svc/handler.go", RelevantFilter{})
		if !ok || after.Racy || after.Revalidate || after.Exe != relevantTestExecutable(t) {
			t.Fatalf("entry = %+v present = %t, want a settled entry for this executable", after, ok)
		}
		if wantRenders == 0 && (!hadEntry || after.Built != before.Built) {
			t.Fatalf("warm built = %d, before = %d present = %t; want the entry untouched", after.Built, before.Built, hadEntry)
		}
		return
	}

	c, dir := newWBClient(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	if _, _, err := c.CreateNote(t.Context(), NoteSpec{Title: "handler", Anchors: AnchorSpec{Paths: []string{"svc/handler.go"}}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "config"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	executables := make([]string, 2)
	for i, name := range []string{"A", "B"} {
		dest := filepath.Join(t.TempDir(), name)
		src, err := os.Open(relevantTestExecutable(t))
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(f, src)
		closeErr := f.Close()
		sourceCloseErr := src.Close()
		if copyErr != nil || closeErr != nil || sourceCloseErr != nil {
			t.Fatalf("copy test executable: copy %v close %v source close %v", copyErr, closeErr, sourceCloseErr)
		}
		executables[i] = dest
	}
	for i, exe := range []string{executables[0], executables[1], executables[0], executables[1]} {
		wantRenders := 0
		if i < 2 {
			wantRenders = 1
		}
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestRelevantCachedKeepsExecutablePathsIndependent$", "-test.count=1")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CCN_RELEVANT_EXECUTABLE_FIXTURE=1", "CCN_RELEVANT_EXPECT_RENDERS="+strconv.Itoa(wantRenders))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("step %d executable %s: %v\n%s", i+1, filepath.Base(exe), err, out)
		}
		t.Logf("step %d executable %s: output handler\\n, renders %d", i+1, filepath.Base(exe), wantRenders)
	}
}
