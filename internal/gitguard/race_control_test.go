//go:build race

package gitguard

import (
	"bytes"
	"os"
	"os/exec"
	"sync"
	"testing"
)

func TestGuardianRaceDetectorStillReports(t *testing.T) {
	if os.Getenv("CC_NOTES_GITGUARD_RACE_CONTROL") == "1" {
		var value int
		var workers sync.WaitGroup
		for range 2 {
			workers.Go(func() {
				for range 100000 {
					value++
				}
			})
		}
		workers.Wait()
		t.Log(value)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestGuardianRaceDetectorStillReports$")
	cmd.Env = guardianEnvironment(append(os.Environ(), "CC_NOTES_GITGUARD_RACE_CONTROL=1", "GORACE=log_path=stderr halt_on_error=1 exitcode=77"))
	output, err := cmd.CombinedOutput()
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 77 || !bytes.Contains(output, []byte("WARNING: DATA RACE")) {
		t.Fatalf("race detector = %v, %v, %s", err, cmd.ProcessState, output)
	}
}
