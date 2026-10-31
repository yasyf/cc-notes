package gitobj

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestNativeReferenceGenerationCounters(t *testing.T) {
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
		t.Skipf("native generation oracle requires APFS, got %q", string(name))
	}
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "first")
	first := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := gittest.Git(t, dir, "rev-parse", "HEAD")
	ref := "refs/cc-notes/docs/" + first
	gittest.Git(t, dir, "update-ref", ref, first)
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/docs/"+second, first)
	repo, err := Open(filepath.Join(dir, ".git"), filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	start := func(command *exec.Cmd) (io.WriteCloser, *bufio.Reader, func()) {
		t.Helper()
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
					t.Logf("oracle child cleanup: %v; stderr %q", err, stderr.String())
				}
			}
		})
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		started = true
		return input, bufio.NewReader(output), func() {
			if err := input.Close(); err != nil {
				t.Fatal(err)
			}
			err := command.Wait()
			waited = true
			if err != nil || stderr.Len() != 0 {
				t.Fatalf("oracle child: %v; stderr %q", err, stderr.String())
			}
		}
	}
	type attribute struct {
		Supported bool    `json:"supported"`
		Value     *uint64 `json:"value"`
	}
	type snapshot struct {
		Device    uint64    `json:"device"`
		Inode     uint64    `json:"inode"`
		Common    attribute `json:"common"`
		Recursive attribute `json:"recursive"`
	}
	probe := exec.CommandContext(t.Context(), "python3", "testdata/ref_generation_counters.py", filepath.Join(dir, ".git", "refs", "cc-notes", "docs"))
	probeIn, probeOut, finishProbe := start(probe)
	raw := make([][]byte, 0, 101)
	read := func() snapshot {
		t.Helper()
		if _, err := io.WriteString(probeIn, "snapshot\n"); err != nil {
			t.Fatal(err)
		}
		line, err := probeOut.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, bytes.TrimSpace(line))
		var value snapshot
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	initial, unchanged := read(), read()
	if !reflect.DeepEqual(initial, unchanged) {
		t.Fatal("counter changed without a directory mutation")
	}
	writer := exec.CommandContext(t.Context(), "git", "update-ref", "--stdin")
	writer.Dir = dir
	input, output, finishWriter := start(writer)
	ack := func(want string) {
		t.Helper()
		got, err := output.ReadString('\n')
		if err != nil || got != want+"\n" {
			t.Fatalf("writer acknowledgement = %q, %v, want %q", got, err, want)
		}
	}
	type interval struct {
		name          string
		before, after snapshot
	}
	intervals := make([]interval, 0, 66)
	old := first
	for cycle := range 32 {
		next := second
		if cycle%2 != 0 {
			next = first
		}
		before := read()
		if _, err := fmt.Fprintf(input, "start\nupdate %s %s %s\nprepare\n", ref, next, old); err != nil {
			t.Fatal(err)
		}
		ack("start: ok")
		ack("prepare: ok")
		prepared := read()
		if _, err := io.WriteString(input, "commit\n"); err != nil {
			t.Fatal(err)
		}
		ack("commit: ok")
		committed := read()
		if tip, err := repo.Tip(t.Context(), ref); err != nil || tip != model.SHA(next) {
			t.Fatalf("committed tip = %s, %v, want %s", tip, err, next)
		}
		intervals = append(intervals, interval{fmt.Sprintf("lock-create-%d", cycle), before, prepared}, interval{fmt.Sprintf("rename-%d", cycle), prepared, committed})
		old = next
	}
	finishWriter()
	beforeDelete := read()
	gittest.Git(t, dir, "update-ref", "-d", ref, old)
	deleted := read()
	gittest.Git(t, dir, "update-ref", ref, first, string(bytes.Repeat([]byte{'0'}, 40)))
	recreated := read()
	if tip, err := repo.Tip(t.Context(), ref); err != nil || tip != model.SHA(first) {
		t.Fatalf("recreated tip = %s, %v, want %s", tip, err, first)
	}
	intervals = append(intervals, interval{"delete", beforeDelete, deleted}, interval{"recreate", deleted, recreated})
	finishProbe()
	for index, line := range raw {
		t.Logf("native_counter[%d]=%s", index, line)
	}
	for _, selector := range []struct {
		name string
		get  func(snapshot) attribute
	}{
		{"common", func(s snapshot) attribute { return s.Common }},
		{"recursive", func(s snapshot) attribute { return s.Recursive }},
	} {
		t.Run(selector.name, func(t *testing.T) {
			if !selector.get(initial).Supported {
				t.Skip("filesystem did not report this counter as supported")
			}
			for _, item := range intervals {
				a, b := selector.get(item.before), selector.get(item.after)
				if item.before.Device != item.after.Device || item.before.Inode != item.after.Inode {
					t.Fatalf("%s: directory identity changed", item.name)
				}
				if !a.Supported || !b.Supported || a.Value == nil || b.Value == nil {
					t.Fatalf("%s: counter support changed", item.name)
				}
				if *a.Value == *b.Value {
					t.Errorf("%s: supported counter did not change across mutation: %d", item.name, *a.Value)
				}
			}
		})
	}
}
