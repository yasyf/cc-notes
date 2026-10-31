package gitobj

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestReferenceGenerationNativeStampPrecision(t *testing.T) {
	dir := gittest.InitRepo(t)
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(dir, &filesystem); err != nil {
		t.Fatal(err)
	}
	name := make([]rune, 0, len(filesystem.Fstypename))
	for _, value := range filesystem.Fstypename {
		if value == 0 {
			break
		}
		name = append(name, rune(value))
	}
	if string(name) != "apfs" {
		t.Skipf("native APFS stamp oracle requires APFS, got %q", string(name))
	}
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "first")
	first := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := gittest.Git(t, dir, "rev-parse", "HEAD")
	ref := "refs/cc-notes/docs/" + first
	gittest.Git(t, dir, "update-ref", ref, first)
	repo, err := Open(filepath.Join(dir, ".git"), filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	type observation struct {
		before, prepared, committed       ReferenceGeneration
		beforeAt, preparedAt, committedAt time.Time
		old, next                         string
	}
	readStamp := func() (ReferenceGeneration, time.Time) {
		t.Helper()
		taken := time.Now()
		stamp, err := repo.RefGeneration(t.Context(), "refs/cc-notes/docs/")
		if err != nil {
			t.Fatal(err)
		}
		return stamp, taken
	}
	command := exec.CommandContext(t.Context(), "git", "update-ref", "--stdin")
	command.Dir = dir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	started, waited := false, false
	t.Cleanup(func() {
		_ = input.Close()
		_ = output.Close()
		if started && !waited {
			if err := command.Wait(); err != nil {
				t.Logf("native writer cleanup: %v; stderr %q", err, stderr.String())
			}
		}
	})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	started = true
	reader := bufio.NewReader(output)
	ack := func(want string) {
		t.Helper()
		got, err := reader.ReadString('\n')
		if err != nil || got != want+"\n" {
			t.Fatalf("native writer acknowledgement = %q, %v, want %q", got, err, want)
		}
	}
	observations := make([]observation, 0, 32)
	old := first
	for iteration := range 32 {
		next := second
		if iteration%2 != 0 {
			next = first
		}
		item := observation{old: old, next: next}
		item.before, item.beforeAt = readStamp()
		if _, err := fmt.Fprintf(input, "start\nupdate %s %s %s\nprepare\n", ref, next, old); err != nil {
			t.Fatal(err)
		}
		ack("start: ok")
		ack("prepare: ok")
		item.prepared, item.preparedAt = readStamp()
		if _, err := io.WriteString(input, "commit\n"); err != nil {
			t.Fatal(err)
		}
		ack("commit: ok")
		item.committed, item.committedAt = readStamp()
		if tip, err := repo.Tip(t.Context(), ref); err != nil || tip != model.SHA(next) {
			t.Fatalf("committed tip = %s, %v, want %s", tip, err, next)
		}
		observations = append(observations, item)
		old = next
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	waited = true
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("native writer: %v; stderr %q", err, stderr.String())
	}
	for iteration, item := range observations {
		t.Logf("filesystem=%s cycle=%d old=%s next=%s before_at_ns=%d prepared_at_ns=%d committed_at_ns=%d before=%+v prepared=%+v committed=%+v settled_before=%t settled_prepared=%t settled_committed=%t", string(name), iteration, item.old, item.next, item.beforeAt.UnixNano(), item.preparedAt.UnixNano(), item.committedAt.UnixNano(), item.before, item.prepared, item.committed, item.before.SettledAt(item.beforeAt), item.prepared.SettledAt(item.preparedAt), item.committed.SettledAt(item.committedAt))
		if item.before == item.committed || item.prepared == item.committed {
			t.Errorf("same full namespace stamp despite committed changed tip at cycle %d", iteration)
		}
	}
}
