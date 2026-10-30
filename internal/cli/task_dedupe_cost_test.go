package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestTaskDedupeFreshCLICost(t *testing.T) {
	warmCounts := taskDedupeFreshCLIWork(t)
	for _, warm := range warmCounts {
		if warm["read"] > 512 || warm["openat"] > 512 {
			t.Errorf("warm fresh CLI read unrelated task bodies: %v", warm)
		}
	}
	if len(warmCounts) == 2 {
		for _, name := range []string{"read", "openat"} {
			if warmCounts[1][name] > warmCounts[0][name]+64 {
				t.Errorf("warm %s grew with unrelated tasks: %d at 32, %d at 10000", name, warmCounts[0][name], warmCounts[1][name])
			}
		}
	}
}

func taskDedupeFreshCLIWork(t *testing.T) []map[string]int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	binary := os.Getenv("CCN_PRODUCTIONLIKE_CLI")
	if binary == "" {
		t.Fatal("production-like candidate binary is required")
	}
	info, err := exec.Command("go", "version", "-m", cli.TestBinary).CombinedOutput()
	if err != nil {
		t.Fatalf("functional test binary build info: %v; %s", err, info)
	}
	t.Logf("functional_test_binary=%s build_info=%s", cli.TestBinary, info)
	return taskDedupeFreshCLIWorkFor(t, binary)
}

func taskDedupeFreshCLIWorkFor(t *testing.T, binary string) []map[string]int {
	t.Helper()
	t.Logf("cli=%s sequence=cold-create,target-prime,warm-create,first-unfiltered-list", binary)
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	var warmCounts []map[string]int
	var consecutiveCounts [][3]map[string]int
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := initRepo(t)
			gitDir := filepath.Join(dir, ".git")
			repo, err := gitobj.Open(gitDir, gitDir)
			if err != nil {
				t.Fatal(err)
			}
			sig := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			var packed []string
			for i := range count + 1 {
				label := "other"
				if i == 0 {
					label = "target"
				}
				sha, err := repo.WriteOpsCommit(t.Context(), nil, sig, "task fixture", model.Pack{
					Lamport: 1,
					Ops:     []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: fmt.Sprintf("fixture %d", i), Type: model.TypeTask, Priority: 2, Labels: []string{label}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				packed = append(packed, string(sha)+" "+refs.For(model.KindTask, model.EntityID(sha)))
			}
			slices.Sort(packed)
			if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cold := tracedTaskCreate(t, dir, binary, "cold probe")
			time.Sleep(2100 * time.Millisecond)
			tracedTaskCLI(t, dir, binary, "task", "list", "--all", "--label", "target")
			warm := tracedTaskCreate(t, dir, binary, "warm probe")
			warmCounts = append(warmCounts, warm)
			listed, fullList, listElapsed := tracedTaskCLI(t, dir, binary, "task", "list", "--all", "--all-branches")
			if rows := strings.Count(listed, "\n"); rows != count+3 {
				t.Fatalf("full task list returned %d rows, want %d", rows, count+3)
			}
			t.Logf("unrelated=%d cold=%v warm=%v full_list=%v full_list_ms=%.3f", count, cold, warm, fullList, float64(listElapsed)/float64(time.Millisecond))
			fullListOpens := fullList["open"] + fullList["openat"]
			if fullListOpens > count+515 || fullList["fsync"] > 8 || fullList["fdatasync"] > 8 || fullList["pwrite64"] > 64 {
				t.Errorf("full task list added per-hit cache opens or writes: %v", fullList)
			}
			if cold["read"] <= count {
				t.Fatalf("cold population did not account for %d fixture histories: %v", count, cold)
			}
			if binary == os.Getenv("CCN_PRODUCTIONLIKE_CLI") {
				var consecutive [3]map[string]int
				for i := range consecutive {
					consecutive[i] = tracedTaskCreate(t, dir, binary, fmt.Sprintf("consecutive probe %d", i+1))
					if consecutive[i]["read"] > 512 || consecutive[i]["openat"] > 512 {
						t.Errorf("consecutive fresh CLI %d read unrelated task bodies: %v", i+1, consecutive[i])
					}
				}
				consecutiveCounts = append(consecutiveCounts, consecutive)
				t.Logf("unrelated=%d consecutive_without_reprime=%v", count, consecutive)
			}
		})
	}
	if len(consecutiveCounts) == 2 {
		for i := range consecutiveCounts[0] {
			for _, name := range []string{"read", "openat"} {
				if consecutiveCounts[1][i][name] > consecutiveCounts[0][i][name]+64 {
					t.Errorf("consecutive %d %s grew with unrelated tasks: %d at 32, %d at 10000", i+1, name, consecutiveCounts[0][i][name], consecutiveCounts[1][i][name])
				}
			}
		}
	}
	return warmCounts
}

func tracedTaskCreate(t *testing.T, dir, binary, title string) map[string]int {
	t.Helper()
	out, counts, elapsed := tracedTaskCLI(t, dir, binary, "task", "add", title, "--backlog", "--label", "target", "--no-validation-criteria")
	if !strings.Contains(out, "\topen\tP2\t-\t"+title+"\n") {
		t.Fatalf("fresh CLI task create output: %q", out)
	}
	t.Logf("create=%q elapsed_ms=%.3f counters=%v", title, float64(elapsed)/float64(time.Millisecond), counts)
	return counts
}

func tracedTaskCLI(t *testing.T, dir, binary string, args ...string) (string, map[string]int, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
	defer cancel()
	report := filepath.Join(t.TempDir(), "syscalls.txt")
	syscalls := []string{"read", "pread64", "write", "pwrite64", "open", "openat", "fstat", "newfstatat", "getdents64", "fsync", "fdatasync", "close"}
	argv := append([]string{"-f", "-qq", "-c", "-o", report, "-e", "trace=" + strings.Join(syscalls, ","), binary}, args...)
	cmd := exec.CommandContext(ctx, "strace", argv...)
	cmd.Dir = dir
	cmd.Env = binEnv(actorA)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("fresh CLI %v: %v; stdout %q; stderr %q", args, err, stdout.String(), stderr.String())
	}
	elapsed := time.Since(started)
	if stderr.Len() != 0 {
		t.Fatalf("fresh CLI %v stderr: %q", args, stderr.String())
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !slices.Contains(syscalls, fields[len(fields)-1]) {
			continue
		}
		calls, err := strconv.Atoi(fields[3])
		if err != nil {
			t.Fatal(err)
		}
		counts[fields[len(fields)-1]] = calls
	}
	if counts["read"] == 0 || counts["openat"] == 0 {
		t.Fatalf("missing syscall counters: %s", data)
	}
	return stdout.String(), counts, elapsed
}
