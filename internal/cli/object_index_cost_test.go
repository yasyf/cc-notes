package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	_ "modernc.org/sqlite"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestPackedObjectIndexFreshCLICost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ccnhome.Env, cli.TestHome)
	results := make([]map[string]int, 0, 2)
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := initRepo(t)
			var ledger struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(mustBin(t, dir, actorA, "ledger", "add", "packed source fixture", "--json")), &ledger); err != nil || ledger.ID == "" {
				t.Fatalf("ledger fixture=%v, %v", ledger, err)
			}
			mustBin(t, dir, actorA, "answer", "add", "existing packed question", "--body", "existing packed answer", "--json")
			gitDir := filepath.Join(dir, ".git")
			repo, err := gitobj.Open(gitDir, gitDir)
			if err != nil {
				t.Fatal(err)
			}
			signature := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			packed := make([]string, 0, count)
			for i := range count {
				sha, err := repo.WriteOpsCommit(t.Context(), nil, signature, "packed unrelated fixture", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: fmt.Sprintf("unrelated%d", i), Type: model.TypeTask, Priority: 2}}})
				if err != nil {
					t.Fatal(err)
				}
				packed = append(packed, string(sha)+" "+refs.For(model.KindTask, model.EntityID(sha)))
			}
			slices.Sort(packed)
			data := "# pack-refs with: peeled fully-peeled sorted\n" + strings.Join(packed, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			gittest.Git(t, dir, "repack", "-adq")
			gittest.Git(t, dir, "multi-pack-index", "write")
			if err := os.RemoveAll(filepath.Join(gitDir, "cc-notes", "folds-v4")); err != nil {
				t.Fatal(err)
			}
			home, err := ccnhome.ForRepo(gitDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(home.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(home.Dir, "objects-v2.sqlite")
			db, err := sql.Open("sqlite", legacy+"?_txlock=immediate")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := db.ExecContext(t.Context(), "CREATE TABLE retained(value INTEGER)"); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Error(err)
				}
			}()
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO retained(value) VALUES(1)"); err != nil {
				t.Fatal(err)
			}
			out, counts, elapsed := tracedPackedCLI(t, dir, "ledger", "show", ledger.ID, "--json")
			var actual struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal([]byte(out), &actual); err != nil || actual.ID != ledger.ID || actual.Title != "packed source fixture" {
				t.Fatalf("packed ledger show=%q, %v", out, err)
			}
			totalReads := counts["read"] + counts["pread64"]
			totalOpens := counts["open"] + counts["openat"]
			totalStats := counts["fstat"] + counts["newfstatat"]
			if totalReads > 512 || totalOpens > 512 || totalStats > 512 || counts["getdents64"] > 64 || counts["fsync"] > 8 || counts["fdatasync"] > 8 {
				t.Errorf("packed single-record read grew corpus work: %v", counts)
			}
			counts["total_reads"], counts["total_opens"], counts["total_stats"] = totalReads, totalOpens, totalStats
			results = append(results, counts)
			t.Logf("unrelated_packed=%d held_legacy_writer=true fresh_ledger_show_ms=%.3f counts=%v", count, float64(elapsed)/float64(time.Millisecond), counts)
			created, writeCounts, writeElapsed := tracedPackedCLI(t, dir, "answer", "add", "new packed-source question", "--body", "new answer body", "--json")
			var answer struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal([]byte(created), &answer); err != nil || answer.ID == "" || answer.Title != "new packed-source question" {
				t.Fatalf("answer create=%q, %v", created, err)
			}
			shown := mustBin(t, dir, actorA, "answer", "show", answer.ID, "--json")
			if !strings.Contains(shown, "new answer body") {
				t.Fatalf("created answer readback=%q", shown)
			}
			t.Logf("unrelated_packed=%d held_legacy_writer=true fresh_answer_create_ms=%.3f counts=%v", count, float64(writeElapsed)/float64(time.Millisecond), writeCounts)
		})
	}
	if len(results) == 2 {
		for _, name := range []string{"total_reads", "total_opens", "total_stats", "getdents64", "write", "pwrite64", "fsync", "fdatasync"} {
			if results[1][name] > results[0][name]+64 {
				t.Errorf("packed %s grew with unrelated objects:32=%d 10000=%d", name, results[0][name], results[1][name])
			}
		}
	}
}

func tracedPackedCLI(t *testing.T, dir string, args ...string) (string, map[string]int, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
	defer cancel()
	report := filepath.Join(t.TempDir(), "syscalls.txt")
	syscalls := []string{"read", "pread64", "write", "pwrite64", "open", "openat", "fstat", "newfstatat", "getdents64", "fsync", "fdatasync", "close"}
	argv := append([]string{"-f", "-qq", "-c", "-o", report, "-e", "trace=" + strings.Join(syscalls, ","), cli.TestBinary}, args...)
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
