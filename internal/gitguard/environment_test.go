package gitguard

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuardianRaceEnvironmentRestoresGit(t *testing.T) {
	for _, original := range [][]string{{}, {"PATH=/bin"}, {"PATH=/bin", "GORACE="}, {"PATH=/bin", "GORACE=halt_on_error=1 exitcode=77 atexit_sleep_ms=91"}} {
		before := slices.Clone(original)
		guard := guardianEnvironment(original)
		if !reflect.DeepEqual(original, before) {
			t.Fatal("parent environment changed")
		}
		if got := gitEnvironment(guard); !reflect.DeepEqual(got, original) {
			t.Fatalf("git environment = %q, want %q", got, original)
		}
		if got := guard[len(guard)-1]; !strings.HasSuffix(got, " atexit_sleep_ms=0") {
			t.Fatalf("guardian race options = %q", got)
		}
	}
}

func TestGuardianInheritsParentEnvironment(t *testing.T) {
	t.Setenv("GORACE", "halt_on_error=1 exitcode=77 atexit_sleep_ms=91")
	want := os.Environ()
	got := gitEnvironment(guardianEnvironment(nil))
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("inherited git environment = %q, want %q", got, want)
	}
}

func TestGuardianProtocolAndDelayedDrains(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nprintf '%s\\n' \"$GORACE\"\nprintf 'first\\n' >&2\nsleep 0.05\nprintf 'last\\n' >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := make([]*os.File, 0, 4)
	for range 4 {
		f, err := os.CreateTemp(t.TempDir(), "capture")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		files = append(files, f)
	}
	parent := []string{"PATH=" + dir + ":/usr/bin:/bin", "GORACE=halt_on_error=1 exitcode=77 atexit_sleep_ms=91"}
	cmd, err := Command(t.Context(), files[0], files[1], files[2], files[3], parent, []string{"fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	started := time.Now()
	if err := cmd.Run(); err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 7 {
		t.Fatalf("guardian exit = %v, %v, %s", err, cmd.ProcessState, output.String())
	}
	t.Logf("guardian_protocol=PASS delayed_drain=PASS elapsed=%s", time.Since(started))
	for i, want := range []string{"halt_on_error=1 exitcode=77 atexit_sleep_ms=91\n", "first\nlast\n", "exit 7"} {
		got, err := io.ReadAll(io.NewSectionReader(files[i+1], 0, 1<<20))
		if err != nil || string(got) != want {
			t.Fatalf("capture %d = %q, %v, want %q", i, got, err, want)
		}
	}
}

func TestJoinDrainsWaitsForEveryResult(t *testing.T) {
	first, firstWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	second, secondWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstWrite.Close(); _ = secondWrite.Close(); _ = second.Close() })
	var exited atomic.Bool
	drains := []*pipeDrain{startDrain(first, &exited), startDrain(second, &exited)}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	exited.Store(true)
	if err := joinDrains(drains); err == nil {
		t.Fatal("closed drain failure disappeared")
	}
	for _, d := range drains {
		select {
		case err := <-d.done:
			t.Fatalf("drain result was not consumed: %v", err)
		default:
		}
	}
	if _, err := second.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("second drain was not stopped: %v", err)
	}
	_ = second.SetReadDeadline(time.Time{})
}
