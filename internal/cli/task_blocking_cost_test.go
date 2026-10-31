package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestTaskBlockingFreshCLICost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	var allCounts []map[string]int
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := initRepo(t)
			gitDir := filepath.Join(dir, ".git")
			repo, err := gitobj.Open(gitDir, gitDir)
			if err != nil {
				t.Fatal(err)
			}
			sig := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			packed := make([]string, 0, count+5)
			var target model.EntityID
			var wantBlocks []string
			for i := range count + 5 {
				label := "other"
				branch := model.Branch("elsewhere")
				if i == 0 {
					label, branch = "target", ""
				}
				ops := []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: fmt.Sprintf("fixture %d", i), Type: model.TypeTask, Labels: []string{label}, Branch: branch}}
				if i > 0 && i < 5 {
					ops = append(ops, model.AddDep{ID: target})
				}
				switch i {
				case 1:
					ops = append(ops, model.SetStatus{Status: model.StatusDone})
				case 2:
					ops = append(ops, model.SetStatus{Status: model.StatusCancelled})
				case 4:
					ops = append(ops, model.DeleteNote{})
				}
				sha, err := repo.WriteOpsCommit(t.Context(), nil, sig, "task fixture", model.Pack{Lamport: 1, Ops: ops})
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					target = model.EntityID(sha)
				}
				if i > 0 && i < 4 {
					wantBlocks = append(wantBlocks, string(sha))
				}
				packed = append(packed, string(sha)+" "+refs.For(model.KindTask, model.EntityID(sha)))
			}
			slices.Sort(packed)
			slices.Sort(wantBlocks)
			if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustBin(t, dir, actorA, "task", "list", "--backlog", "--label", "target")
			time.Sleep(2100 * time.Millisecond)
			mustBin(t, dir, actorA, "task", "list", "--backlog", "--label", "target")
			ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
			defer cancel()
			report := filepath.Join(t.TempDir(), "syscalls.txt")
			cmd := exec.CommandContext(ctx, "strace", "-f", "-qq", "-c", "-o", report, "-e", "trace=read,pread64,open,openat,fstat,newfstatat,getdents64", cli.TestBinary, "task", "list", "--backlog", "--label", "target", "--json")
			cmd.Dir, cmd.Env = dir, binEnv(actorA)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			started := time.Now()
			if err := cmd.Run(); err != nil {
				t.Fatalf("fresh JSON list: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			elapsed := time.Since(started)
			var rows []struct {
				ID     string   `json:"id"`
				Blocks []string `json:"blocks"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 || len(rows) != 1 || rows[0].ID != string(target) || !slices.Equal(rows[0].Blocks, wantBlocks) {
				t.Fatalf("selected JSON rows=%+v stderr=%q want target=%s blocks=%v", rows, stderr.String(), target, wantBlocks)
			}
			data, err := os.ReadFile(report)
			if err != nil {
				t.Fatal(err)
			}
			counts := make(map[string]int)
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 5 || !slices.Contains([]string{"read", "pread64", "open", "openat", "fstat", "newfstatat", "getdents64"}, fields[len(fields)-1]) {
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
			for _, name := range []string{"read", "openat"} {
				if counts[name] > 512 {
					t.Errorf("selected JSON %s reads unrelated corpus: %v", name, counts)
				}
			}
			allCounts = append(allCounts, counts)
			t.Logf("unrelated=%d selected=1 outside_filter_blocks=3 elapsed=%v syscalls=%v", count, elapsed, counts)
		})
	}
	if len(allCounts) == 2 {
		for _, name := range []string{"read", "openat"} {
			if allCounts[1][name] > allCounts[0][name]+64 {
				t.Errorf("selected JSON %s grew:32=%d10000=%d", name, allCounts[0][name], allCounts[1][name])
			}
		}
	}
}
