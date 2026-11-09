package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/model"
)

func TestSourceHistoryCostHelper(t *testing.T) {
	path := os.Getenv("CCN_SOURCE_HISTORY_COST_INPUT")
	if path == "" {
		t.Skip("deep-history append cost subprocess")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input managedCostInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	s, err := OpenContext(t.Context(), input.Dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := refs.For(model.KindTask, input.Expected.ID)
	if _, err := s.Load(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	ctx, work := gitobj.WithReadWork(t.Context())
	var metadata atomic.Int64
	ctx = context.WithValue(ctx, answerCorpusMetadataWorkKey{}, &metadata)
	s.now = func() time.Time { return time.Unix(1_700_000_100, 0).UTC() }
	before := managedCostRusage(t)
	started := time.Now()
	if _, err := os.Stderr.WriteString(managedCostBegin); err != nil {
		t.Fatal(err)
	}
	title := "deep history cached append"
	var snapshot model.Snapshot
	switch input.Mode {
	case "history-append":
		snapshot, err = s.Append(ctx, ref, []model.Op{model.SetTitle{Title: title}})
	case "history-helper":
		title = "deep history helper append"
		var head sourceindex.Revision
		head, err = s.SourceHead(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var prepared PreparedRef
		prepared, err = s.PrepareAppendAt(ctx, ref, input.Expected.Head, []model.Op{model.SetTitle{Title: title}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.CommitSourceOperation(ctx, sourceindex.CommitRequest{Expected: head.Token, OperationID: strings.Repeat("a", 64), Result: "entity:task:" + string(input.Expected.ID), RequestDigest: sha256.Sum256([]byte("deep helper request")), Updates: []gitcmd.RefUpdate{prepared.RefUpdate()}, Snapshots: map[model.SHA]model.Snapshot{prepared.New: prepared.Snapshot}})
		snapshot = prepared.Snapshot
	default:
		t.Fatalf("unknown history mode %q", input.Mode)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stderr.WriteString(managedCostEnd); err != nil {
		t.Fatal(err)
	}
	result := managedCostResult{Mode: input.Mode, Snapshots: []model.Task{snapshot.(model.Task)}, Elapsed: int64(time.Since(started))}
	result.RefListings, result.RefEntries, result.PackDecodes = work.Snapshot()
	result.MetadataLoads = metadata.Load()
	after := managedCostRusage(t)
	result.UserCPU = managedCostCPU(after.self.Utime, after.children.Utime) - managedCostCPU(before.self.Utime, before.children.Utime)
	result.SystemCPU = managedCostCPU(after.self.Stime, after.children.Stime) - managedCostCPU(before.self.Stime, before.children.Stime)
	result.MinorFaults = after.self.Minflt + after.children.Minflt - before.self.Minflt - before.children.Minflt
	result.MajorFaults = after.self.Majflt + after.children.Majflt - before.self.Majflt - before.children.Majflt
	result.BlockReads = (after.self.Inblock + after.children.Inblock - before.self.Inblock - before.children.Inblock) * 512
	result.BlockWrites = (after.self.Oublock + after.children.Oublock - before.self.Oublock - before.children.Oublock) * 512
	actual := snapshot.(model.Task)
	if actual.ID != input.Expected.ID || actual.Title != title || actual.Head == input.Expected.Head {
		t.Fatalf("history append snapshot=%+v expected original=%+v", actual, input.Expected)
	}
	chain, err := s.Repo.ReadChain(t.Context(), actual.Head)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := fold.Strict(chain)
	if err != nil || !reflect.DeepEqual(actual, complete) {
		t.Fatalf("deep snapshot differs from full fold: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	homeguard.ChildExit(0)
}

func TestSourceCachedAppendCostDoesNotGrowWithHistory(t *testing.T) {
	if os.Getenv("CCN_REQUIRE_MANAGED_COST") != "1" {
		t.Skip("strict hosted deep-history append proof")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	byMode := make(map[string][]managedCostResult)
	for _, depth := range []int{32, 1024} {
		t.Run(strconv.Itoa(depth), func(t *testing.T) {
			s := initStore(t)
			signature := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_000, 0).UTC()}
			var root, tip model.SHA
			for position := range depth {
				var parents []model.SHA
				var ops []model.Op
				if position == 0 {
					ops = []model.Op{model.CreateTask{Nonce: "0123456789abcdef0123456789abcdef", Title: "deep history fixture", Type: model.TypeTask, Priority: 2}}
				} else {
					parents = []model.SHA{tip}
					ops = []model.Op{model.SetTitle{Title: fmt.Sprintf("deep history fixture %08d", position)}}
				}
				var err error
				tip, err = s.Repo.WriteOpsCommit(t.Context(), parents, signature, "deep history fixture", model.Pack{Lamport: model.Lamport(position + 1), Ops: ops})
				if err != nil {
					t.Fatal(err)
				}
				if position == 0 {
					root = tip
				}
			}
			ref := refs.For(model.KindTask, model.EntityID(root))
			if err := s.RecordsGit.UpdateRef(t.Context(), ref, tip, ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Reindex(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.Load(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			dir := s.Git.Dir
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			target := snapshot.(model.Task)
			for _, mode := range []string{"history-append", "history-helper"} {
				result := sourceHistoryCostRun(t, managedCostInput{Dir: dir, Mode: mode, Expected: target}, depth)
				byMode[mode] = append(byMode[mode], result)
				target = result.Snapshots[0]
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("SOURCE_HISTORY_COST_RESULT: %s", data)
				if result.PackDecodes > 8 {
					t.Errorf("deep-history %s decoded %d packs at depth %d", mode, result.PackDecodes, depth)
				}
			}
		})
	}
	for mode, results := range byMode {
		if len(results) != 2 {
			t.Fatalf("history %s populations=%d want 2", mode, len(results))
		}
		small, large := results[0], results[1]
		for _, counter := range []struct {
			name                    string
			small, large, allowance int64
		}{{"read_return_bytes", small.ReadBytes, large.ReadBytes, 256 << 10}, {"write_return_bytes", small.WriteBytes, large.WriteBytes, 256 << 10}, {"minor_faults", small.MinorFaults, large.MinorFaults, 256}, {"git_processes", small.GitProcesses, large.GitProcesses, 8}} {
			if counter.large > counter.small+counter.allowance {
				t.Errorf("deep-history %s grew:32=%d1024=%d allowance=%d", counter.name, counter.small, counter.large, counter.allowance)
			}
		}
		for _, name := range []string{"read", "pread64", "openat", "newfstatat", "fstat", "fsync", "fdatasync"} {
			if large.Syscalls[name] > small.Syscalls[name]+64 {
				t.Errorf("deep-history %s grew:32=%d1024=%d", name, small.Syscalls[name], large.Syscalls[name])
			}
		}
	}
}

func sourceHistoryCostRun(t *testing.T, input managedCostInput, depth int) managedCostResult {
	t.Helper()
	dir := t.TempDir()
	if base := os.Getenv("CCN_MANAGED_COST_ARTIFACT_DIR"); base != "" {
		dir = filepath.Join(base, "history", strconv.Itoa(depth), input.Mode)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "input.json")
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "syscalls")
	child := exec.CommandContext(t.Context(), "strace", "-ff", "-q", "-ttt", "-s", "256", "-e", "trace=read,pread64,readv,preadv,preadv2,write,pwrite64,writev,pwritev,pwritev2,open,openat,openat2,fstat,newfstatat,statx,getdents64,fsync,fdatasync,close,mmap,mremap,munmap,%process", "-o", trace, os.Args[0], "-test.run=^TestSourceHistoryCostHelper$", "-test.count=1")
	child.Env = append(os.Environ(), "CCN_SOURCE_HISTORY_COST_INPUT="+path, "GOMAXPROCS=1")
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	if err := child.Run(); err != nil {
		t.Fatalf("history child=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stderr.String() != managedCostBegin+managedCostEnd {
		t.Fatalf("history stderr=%q", stderr.String())
	}
	var result managedCostResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Mode != input.Mode {
		t.Fatalf("history result=%q err=%v", stdout.String(), err)
	}
	paths, err := filepath.Glob(trace + ".*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("history traces=%v err=%v", paths, err)
	}
	var calls []managedCostCall
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := managedCostParseTrace(data)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, parsed...)
	}
	if err := managedCostAccountTrace(&result, calls); err != nil {
		t.Fatal(err)
	}
	result.Refs = 1
	data, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return result
}
