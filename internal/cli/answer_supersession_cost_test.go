package cli_test

import (
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

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestAnswerShowFreshCLICost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	results := make([]map[string]int, 0, 2)
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := initRepo(t)
			gitDir := filepath.Join(dir, ".git")
			repo, err := gitobj.Open(gitDir, gitDir)
			if err != nil {
				t.Fatal(err)
			}
			sig := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			ids := make([]model.EntityID, 0, count+4)
			tips := make([]model.SHA, 0, count+4)
			for i := range count + 4 {
				label := "other"
				if i == 0 {
					label = "target"
				}
				sha, err := repo.WriteOpsCommit(t.Context(), nil, sig, "answer fixture", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: fmt.Sprintf("answer %d", i), Body: fmt.Sprintf("body %d", i), Tags: []string{label}}}})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, model.EntityID(sha))
				tips = append(tips, sha)
			}
			for _, edge := range [][2]int{{0, 2}, {1, 0}, {2, 3}} {
				sha, err := repo.WriteOpsCommit(t.Context(), []model.SHA{tips[edge[0]]}, sig, "answer edge", model.Pack{Lamport: 2, Ops: []model.Op{model.AddSupersededBy{ID: ids[edge[1]]}}})
				if err != nil {
					t.Fatal(err)
				}
				tips[edge[0]] = sha
			}
			packed := make([]string, 0, len(ids))
			for i, id := range ids {
				packed = append(packed, string(tips[i])+" "+refs.For(model.KindAnswer, id))
			}
			slices.Sort(packed)
			if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for attempt := range 2 {
				if attempt > 0 {
					time.Sleep(2100 * time.Millisecond)
				}
				out := mustBin(t, dir, actorA, "answer", "list", "--label", "target", "--include-superseded", "--json")
				var rows []struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].ID != string(ids[0]) {
					t.Fatalf("prime selected answer = %q, %v", out, err)
				}
			}
			if err := os.RemoveAll(filepath.Join(gitDir, "cc-notes", "folds-v2")); err != nil {
				t.Fatal(err)
			}
			out, counts, elapsed := tracedPackedCLI(t, dir, "answer", "show", string(ids[0]), "--json")
			var got struct {
				ID         string   `json:"id"`
				Title      string   `json:"title"`
				Body       string   `json:"body"`
				Supersedes []string `json:"supersedes"`
				LiveHead   []string `json:"live_head"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if got.ID != string(ids[0]) || got.Title != "answer 0" || got.Body != "body 0" || !slices.Equal(got.Supersedes, []string{string(ids[1])}) || !slices.Equal(got.LiveHead, []string{string(ids[3])}) {
				t.Fatalf("selected answer = %+v; want id=%s incoming=%s head=%s", got, ids[0], ids[1], ids[3])
			}
			counts["total_reads"] = counts["read"] + counts["pread64"]
			counts["total_opens"] = counts["open"] + counts["openat"]
			counts["total_stats"] = counts["fstat"] + counts["newfstatat"]
			for _, name := range []string{"total_reads", "total_opens", "total_stats"} {
				if counts[name] > 512 {
					t.Errorf("selected answer %s reads unrelated corpus: %v", name, counts)
				}
			}
			if counts["getdents64"] > 64 {
				t.Errorf("selected answer enumerates unrelated corpus: %v", counts)
			}
			results = append(results, counts)
			t.Logf("unrelated_answers=%d selected=1 incoming=1 terminal=1 fresh_show_ms=%.3f counts=%v", count, float64(elapsed)/float64(time.Millisecond), counts)
		})
	}
	if len(results) == 2 {
		for _, name := range []string{"total_reads", "total_opens", "total_stats", "getdents64"} {
			if results[1][name] > results[0][name]+64 {
				t.Errorf("selected answer %s grew:32=%d 10000=%d", name, results[0][name], results[1][name])
			}
		}
	}
}
