package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestLoadFallbackCostHelper(t *testing.T) {
	path := os.Getenv("CCN_LOAD_FALLBACK_COST_INPUT")
	if path == "" {
		t.Skip("fresh-process fallback-head cost helper")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input loadParentCostInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	s, err := Open(input.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, hit := s.cache.getTop(input.Parent); hit {
		t.Fatal("fallback parent was unexpectedly cached")
	}
	if _, hit := s.cache.get(input.Head); hit {
		t.Fatal("new tip was already cached")
	}
	var snapshot model.Snapshot
	if os.Getenv("CCN_LOAD_FALLBACK_COST_MODE") == "head" {
		snapshot, err = s.LoadAt(t.Context(), input.Head)
	} else {
		snapshot, err = s.Load(t.Context(), input.Ref)
	}
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(snapshot)
	if err != nil || !bytes.Equal(actual, input.Want) {
		t.Fatalf("fresh load = %s, %v; full fold = %s", actual, err, input.Want)
	}
	if _, hit := s.cache.get(input.Head); !hit {
		t.Fatal("fresh load did not cache its new tip")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "PASS_FULL_SNAPSHOT", "head": string(input.Head)}); err != nil {
		t.Fatal(err)
	}
	homeguard.ChildExit(0)
}

func TestLoadFallbackFreshProcessCost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native request accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{32, 512} {
		for _, mode := range []string{"ref", "head"} {
			t.Run(fmt.Sprintf("%d/%s", count, mode), func(t *testing.T) {
				dir := gittest.InitRepo(t)
				s, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				s.now = steppedClock()
				base := create(t, s, ledgerOps("fixed-state history")).(model.Ledger)
				ref := refs.For(model.KindLedger, base.ID)
				parent := base.Head
				for i := 1; i < count; i++ {
					snapshot, err := s.Append(t.Context(), ref, []model.Op{model.UpsertRow{Key: "only-row", Fields: map[string]string{"state": strconv.Itoa(i % 2)}}})
					if err != nil {
						t.Fatal(err)
					}
					parent = snapshot.Meta().Head
				}
				largeState := strings.Repeat("large-state:", 7000)
				prepared, err := s.PrepareAppendAt(t.Context(), ref, parent, []model.Op{model.UpsertRow{Key: "only-row", Fields: map[string]string{"state": largeState}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PublishRef(t.Context(), ref, prepared.New, parent); err != nil {
					t.Fatal(err)
				}
				if _, hit := s.cache.get(prepared.New); hit {
					t.Fatal("preparation or publication cached the unread child")
				}
				s.cache.delete(parent)
				chain, err := s.Repo.ReadChain(t.Context(), prepared.New)
				if err != nil {
					t.Fatal(err)
				}
				want, err := fold.Fold(chain)
				if err != nil {
					t.Fatal(err)
				}
				ledger := want.(model.Ledger)
				if len(chain) != count+1 || len(ledger.Rows) != 1 || ledger.Rows[0].Fields["state"] != largeState {
					t.Fatalf("fixture history/state = %d/%#v", len(chain), ledger.Rows)
				}
				wantJSON, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				gittest.Git(t, dir, "repack", "-adq")
				opsHash := gittest.Git(t, dir, "rev-parse", string(prepared.New)+":ops.json")
				opsBytes, err := strconv.Atoi(gittest.Git(t, dir, "cat-file", "-s", opsHash))
				if err != nil || opsBytes <= 1<<16 {
					t.Fatalf("fallback head is not a native large object: %d, %v", opsBytes, err)
				}
				input := filepath.Join(t.TempDir(), "input.json")
				data, err := json.Marshal(loadParentCostInput{Dir: dir, Ref: ref, Parent: parent, Head: prepared.New, Want: wantJSON})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(input, data, 0o600); err != nil {
					t.Fatal(err)
				}
				trace := filepath.Join(t.TempDir(), "requests.strace")
				cmd := exec.CommandContext(t.Context(), "strace", "-ff", "-qq", "-s", "4096", "-e", "trace=write", "-o", trace, os.Args[0], "-test.run=^TestLoadFallbackCostHelper$", "-test.count=1")
				cmd.Env = append(os.Environ(), "CCN_LOAD_FALLBACK_COST_INPUT="+input, "CCN_LOAD_FALLBACK_COST_MODE="+mode)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("fresh reader: %v: %s", err, out)
				}
				var result map[string]string
				if err := json.Unmarshal(out, &result); err != nil || result["status"] != "PASS_FULL_SNAPSHOT" || result["head"] != string(prepared.New) {
					t.Fatalf("fresh result = %q, %v", out, err)
				}
				paths, err := filepath.Glob(trace + ".*")
				if err != nil || len(paths) == 0 {
					t.Fatalf("native request traces = %v, %v", paths, err)
				}
				var raw []byte
				for _, path := range paths {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					raw = append(raw, data...)
					raw = append(raw, '\n')
				}
				requests := loadParentNativeRequests(t, raw)
				info, contents := requests["info "+opsHash], requests["contents "+opsHash]
				t.Logf("history=%d mode=%s fixed_rows=1 full_snapshot=PASS head_ops_bytes=%d head_info=%d head_contents=%d", count, mode, opsBytes, info, contents)
				if info != 1 || contents != 1 {
					t.Errorf("FALLBACK_HEAD_REREAD_REGRESSION: history=%d mode=%s head_info=%d head_contents=%d", count, mode, info, contents)
				}
			})
		}
	}
}
