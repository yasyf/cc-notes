package cli_test

import (
	"os"
	"runtime"
	"testing"
)

func TestTaskDedupeLegacyCLIListCost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("legacy CLI comparison requires the Linux release artifact")
	}
	binary := os.Getenv("CCN_LEGACY_BASELINE_CLI")
	if binary == "" {
		t.Fatal("the proof job must supply the verified legacy CLI artifact")
	}
	taskDedupeFreshCLIWorkFor(t, binary)
}
