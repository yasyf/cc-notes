package helperclient

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/daemonkit/worker"
)

func TestToolRunnerExecutesAndSettlesOneTask(t *testing.T) {
	runner, err := NewToolRunner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), worker.CommandRequest{
		Path: "/usr/bin/true", Dir: "/", TotalTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	if err := runner.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestRunProvisionRejectsInexactExecutable(t *testing.T) {
	if err := RunProvision(t.Context(), "relative", t.TempDir()); err == nil {
		t.Fatal("RunProvision accepted a relative executable")
	}
	target := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(target, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "helper")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := RunProvision(t.Context(), link, t.TempDir()); err == nil {
		t.Fatal("RunProvision accepted a symlinked executable")
	}
}
