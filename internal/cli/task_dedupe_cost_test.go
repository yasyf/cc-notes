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
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	var warmCounts []map[string]int
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
			cold := tracedTaskCreate(t, dir, "cold probe")
			time.Sleep(2100 * time.Millisecond)
			mustBin(t, dir, actorA, "task", "list", "--all", "--label", "target")
			warm := tracedTaskCreate(t, dir, "warm probe")
			warmCounts = append(warmCounts, warm)
			t.Logf("unrelated=%d cold=%v warm=%v", count, cold, warm)
			if cold["read"] <= count {
				t.Fatalf("cold population did not account for %d fixture histories: %v", count, cold)
			}
			if warm["read"] > 512 || warm["openat"] > 512 || warm["newfstatat"] > 512 || warm["getdents64"] > 64 {
				t.Fatalf("warm fresh CLI visited unrelated task corpus: %v", warm)
			}
		})
	}
	if len(warmCounts) == 2 {
		for _, name := range []string{"read", "openat", "newfstatat", "getdents64"} {
			if warmCounts[1][name] > warmCounts[0][name]+64 {
				t.Errorf("warm %s grew with unrelated tasks: %d at 32, %d at 10000", name, warmCounts[0][name], warmCounts[1][name])
			}
		}
	}
}

func tracedTaskCreate(t *testing.T, dir, title string) map[string]int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
	defer cancel()
	report := filepath.Join(t.TempDir(), "syscalls.txt")
	cmd := exec.CommandContext(ctx, "strace", "-f", "-qq", "-c", "-o", report, "-e", "trace=read,openat,newfstatat,getdents64", cli.TestBinary,
		"task", "add", title, "--backlog", "--label", "target", "--no-validation-criteria")
	cmd.Dir = dir
	cmd.Env = binEnv(actorA)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fresh CLI task create: %v; stdout %q; stderr %q", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "\topen\tP2\t-\t"+title+"\n") {
		t.Fatalf("fresh CLI output: stdout %q; stderr %q", stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !slices.Contains([]string{"read", "openat", "newfstatat", "getdents64"}, fields[len(fields)-1]) {
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
	return counts
}
