package holderclient

import (
	"testing"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"
)

func TestToolRunnerExecutesAndSettlesOneTask(t *testing.T) {
	runner, err := NewToolRunner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), supervise.Task{
		RecoveryClass: proc.RecoveryTask, Path: "/usr/bin/true",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := runner.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
}
