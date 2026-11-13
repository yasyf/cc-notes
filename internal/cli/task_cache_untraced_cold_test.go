package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTaskCacheUntracedColdPoint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("paired release-style comparison requires Linux binaries")
	}
	for _, item := range []struct{ name, binary string }{
		{"candidate", os.Getenv("CCN_PRODUCTIONLIKE_CLI")},
		{"legacy", os.Getenv("CCN_LEGACY_BASELINE_CLI")},
	} {
		if item.binary == "" {
			t.Fatalf("%s binary is required", item.name)
		}
		t.Run(item.name, func(t *testing.T) {
			const count = 10000
			dir := taskDedupeFixture(t, count)
			ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, item.binary, "task", "add", "ordinary cold probe", "--backlog", "--label", "target", "--no-validation-criteria")
			cmd.Dir, cmd.Env = dir, binEnv(actorA)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			started := time.Now()
			err := cmd.Run()
			elapsed := time.Since(started)
			if err != nil || stderr.Len() != 0 || !strings.Contains(stdout.String(), "\topen\tP2\t-\tordinary cold probe\n") {
				t.Fatalf("ordinary cold point: %v; stdout %q; stderr %q", err, stdout.String(), stderr.String())
			}
			t.Logf("untraced_cold_point=%s cli=%q unrelated=%d elapsed_ms=%.3f user_cpu=%v system_cpu=%v raw_direct_child_rusage=%+v", item.name, item.binary, count, float64(elapsed)/float64(time.Millisecond), cmd.ProcessState.UserTime(), cmd.ProcessState.SystemTime(), cmd.ProcessState.SysUsage())
		})
	}
}
