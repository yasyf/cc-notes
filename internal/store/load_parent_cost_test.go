package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type loadParentCostInput struct {
	Dir    string
	Ref    string
	Parent model.SHA
	Head   model.SHA
	Want   json.RawMessage
}

func TestLoadParentCostHelper(t *testing.T) {
	path := os.Getenv("CCN_LOAD_PARENT_COST_INPUT")
	if path == "" {
		t.Skip("fresh-process parent-cache cost helper")
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
	if _, hit := s.cache.get(input.Parent); !hit {
		t.Fatal("fresh reader missed the genuine parent snapshot")
	}
	if _, hit := s.cache.get(input.Head); hit {
		t.Fatal("new tip was already cached")
	}
	var snapshot model.Snapshot
	if os.Getenv("CCN_LOAD_PARENT_COST_MODE") == "head" {
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

func TestLoadParentFreshProcessCost(t *testing.T) {
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
					signature := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1_700_000_000+int64(i), 0).UTC()}
					next, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{parent}, signature, "fixed-state history", model.Pack{Lamport: model.Lamport(i + 1), Ops: []model.Op{model.UpsertRow{Key: "only-row", Fields: map[string]string{"state": strconv.Itoa(i % 2)}}}})
					if err != nil {
						t.Fatal(err)
					}
					parent = next
				}
				if err := s.RecordsGit.UpdateRef(t.Context(), ref, parent, base.Head); err != nil {
					t.Fatal(err)
				}
				if importer, ok := any(s).(interface{ Reindex(context.Context) error }); ok {
					if err := importer.Reindex(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.LoadAt(t.Context(), parent); err != nil {
					t.Fatal(err)
				}
				prepared, err := s.PrepareAppendAt(t.Context(), ref, parent, []model.Op{model.UpsertRow{Key: "only-row", Fields: map[string]string{"state": "final"}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PublishRef(t.Context(), ref, prepared.New, parent); err != nil {
					t.Fatal(err)
				}
				if _, hit := s.cache.get(prepared.New); hit {
					t.Fatal("preparation or publication cached the unread child")
				}
				chain, err := s.Repo.ReadChain(t.Context(), prepared.New)
				if err != nil {
					t.Fatal(err)
				}
				want, err := fold.Fold(chain)
				if err != nil {
					t.Fatal(err)
				}
				ledger := want.(model.Ledger)
				if len(chain) != count+1 || len(ledger.Rows) != 1 || ledger.Rows[0].Fields["state"] != "final" {
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
				allowed := make(map[string]bool)
				for _, suffix := range []string{"", "^{tree}", ":ops.json"} {
					allowed[gittest.Git(t, dir, "rev-parse", string(prepared.New)+suffix)] = true
				}
				if len(allowed) != 3 {
					t.Fatal("fixture did not identify the new commit/tree/ops triple")
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
				cmd := exec.CommandContext(t.Context(), "strace", "-ff", "-qq", "-s", "4096", "-e", "trace=write", "-o", trace, os.Args[0], "-test.run=^TestLoadParentCostHelper$", "-test.count=1")
				cmd.Env = append(os.Environ(), "CCN_LOAD_PARENT_COST_INPUT="+input, "CCN_LOAD_PARENT_COST_MODE="+mode)
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
				t.Logf("history=%d mode=%s fixed_rows=1 full_snapshot=PASS native_requests=%v", count, mode, requests)
				valid := len(requests) == 6
				for key, calls := range requests {
					fields := strings.Fields(key)
					if !allowed[fields[1]] || calls != 1 {
						valid = false
					}
				}
				for hash := range allowed {
					if requests["info "+hash] != 1 || requests["contents "+hash] != 1 {
						valid = false
					}
				}
				if !valid {
					t.Errorf("PARENT_LOAD_HISTORY_REGRESSION: history=%d mode=%s requested ancestors or repeated payloads: %v", count, mode, requests)
				}
			})
		}
	}
}

func loadParentNativeRequests(t *testing.T, raw []byte) map[string]int {
	t.Helper()
	write := regexp.MustCompile(`^\s*(?:[0-9]+\s+)?write\([0-9]+, ("(?:[^"\\]|\\.)*"), ([0-9]+)\)\s+= ([0-9]+)$`)
	requests := make(map[string]int)
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.Contains(line, `"info `) && !strings.Contains(line, `"contents `) {
			continue
		}
		match := write.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("unframed native request: %s", line)
		}
		value, err := strconv.Unquote(match[1])
		if err != nil {
			t.Fatal(err)
		}
		requested, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatal(err)
		}
		written, err := strconv.Atoi(match[3])
		if err != nil || requested != len(value) || written != requested {
			t.Fatalf("partial native request: %s", line)
		}
		fields := strings.Fields(value)
		if len(fields) != 2 || len(fields[1]) != 40 || value != fields[0]+" "+fields[1]+"\n" || (fields[0] != "info" && fields[0] != "contents") {
			t.Fatalf("invalid native request: %q", value)
		}
		if _, err := strconv.ParseUint(fields[1][:16], 16, 64); err != nil {
			t.Fatal(err)
		}
		if _, err := strconv.ParseUint(fields[1][16:32], 16, 64); err != nil {
			t.Fatal(err)
		}
		if _, err := strconv.ParseUint(fields[1][32:], 16, 32); err != nil {
			t.Fatal(err)
		}
		requests[fields[0]+" "+fields[1]]++
	}
	if len(requests) == 0 {
		t.Fatal("no native request evidence")
	}
	return requests
}
