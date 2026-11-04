package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

type helperCostResult struct {
	managedCostResult
	GraphWork sourcegraph.Work `json:"graph_work"`
	Complete  bool             `json:"complete"`
}

func TestHelperFreshProcessCostChild(t *testing.T) {
	path := os.Getenv("CCN_HELPER_COST_INPUT")
	if path == "" {
		t.Skip("hosted fresh-process helper")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input managedCostInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	ctx, graphWork := sourcegraph.WithWork(t.Context())
	ctx, nativeWork := gitobj.WithReadWork(ctx)
	var metadata atomic.Int64
	ctx = context.WithValue(ctx, answerCorpusMetadataWorkKey{}, &metadata)
	before := managedCostRusage(t)
	started := time.Now()
	if _, err := os.Stderr.WriteString(managedCostBegin); err != nil {
		t.Fatal(err)
	}
	s, err := OpenContext(ctx, input.Dir)
	if err != nil {
		t.Fatal(err)
	}
	index := sourceindex.Index{Backend: s}
	head, err := index.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := "entity:task:" + string(input.Expected.ID)
	id := strings.Repeat("3", 64)
	digest := sha256.Sum256([]byte("helper-cost-request"))
	result := helperCostResult{managedCostResult: managedCostResult{Mode: input.Mode}}
	var descriptor sourceview.Object
	var content []byte
	var operation sourceindex.Operation
	var objects []sourceview.Object
	var deletes []string
	switch input.Mode {
	case "refresh":
	case "page":
		values, total, err := s.SourceObjectRange(ctx, head, 0, 3)
		if err != nil || total < 3 || len(values) != 3 {
			t.Fatalf("page total=%d values=%d: %v", total, len(values), err)
		}
		objects = values
	case "target":
		var found bool
		descriptor, found, err = s.SourceObject(ctx, head, key)
		if err != nil || !found {
			t.Fatalf("target found=%t: %v", found, err)
		}
	case "content":
		var reader io.ReadCloser
		descriptor, reader, err = s.OpenSourceContent(ctx, head, key)
		if err != nil {
			t.Fatal(err)
		}
		content, err = io.ReadAll(reader)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read=%v close=%v", err, closeErr)
		}
	case "mutation":
		prepared, err := s.PrepareAppendAt(ctx, refs.For(model.KindTask, input.Expected.ID), input.Expected.Head, []model.Op{model.SetTitle{Title: "helper cost changed title"}})
		if err != nil {
			t.Fatal(err)
		}
		operation, err = s.CommitSourceOperation(ctx, sourceindex.CommitRequest{Expected: head, OperationID: id, Result: key, RequestDigest: digest, Updates: []gitcmd.RefUpdate{prepared.RefUpdate()}, Snapshots: map[model.SHA]model.Snapshot{prepared.New: prepared.Snapshot}})
		if err != nil {
			t.Fatal(err)
		}
	case "changes":
		var found bool
		operation, found, err = s.ReadOperation(ctx, id)
		if err != nil || !found {
			t.Fatalf("operation found=%t: %v", found, err)
		}
		objects, deletes, err = s.SourceObjectChanges(ctx, operation.Previous, operation.Token)
		if err != nil {
			t.Fatal(err)
		}
	case "inspect":
		var found bool
		operation, found, err = s.ReadOperation(ctx, id)
		if err != nil || !found {
			t.Fatalf("operation found=%t: %v", found, err)
		}
	case "replay":
		operation, err = s.CommitSourceOperation(ctx, sourceindex.CommitRequest{Expected: model.SHA("not-an-issued-token"), OperationID: id, Result: key, RequestDigest: digest})
		if err != nil {
			t.Fatal(err)
		}
	case "ack":
		operation, err = s.SettleSourceOperation(ctx, id, digest, sha256.Sum256([]byte("helper-cost-receipt")), gitobj.SourceOperationAcknowledged)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", input.Mode)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stderr.WriteString(managedCostEnd); err != nil {
		t.Fatal(err)
	}
	result.Elapsed = int64(time.Since(started))
	after := managedCostRusage(t)
	result.UserCPU = managedCostCPU(after.self.Utime, after.children.Utime) - managedCostCPU(before.self.Utime, before.children.Utime)
	result.SystemCPU = managedCostCPU(after.self.Stime, after.children.Stime) - managedCostCPU(before.self.Stime, before.children.Stime)
	result.BlockReads = (after.self.Inblock + after.children.Inblock - before.self.Inblock - before.children.Inblock) * 512
	result.BlockWrites = (after.self.Oublock + after.children.Oublock - before.self.Oublock - before.children.Oublock) * 512
	result.MinorFaults = after.self.Minflt + after.children.Minflt - before.self.Minflt - before.children.Minflt
	result.MajorFaults = after.self.Majflt + after.children.Majflt - before.self.Majflt - before.children.Majflt
	result.RefListings, result.RefEntries, result.PackDecodes = nativeWork.Snapshot()
	result.GraphWork = graphWork.Snapshot()
	result.MetadataLoads = metadata.Load()
	switch input.Mode {
	case "target", "content":
		expected := sourceview.CodecOf(model.KindTask).Render(input.Expected)
		if descriptor.Key != key || descriptor.Kind != sourceview.File || descriptor.Size != int64(len(expected)) || descriptor.Hash != sha256.Sum256(expected) {
			t.Fatalf("descriptor=%+v expected=%q", descriptor, expected)
		}
		if input.Mode == "content" && !bytes.Equal(content, expected) {
			t.Fatalf("content=%q expected=%q", content, expected)
		}
	case "mutation", "inspect", "replay", "changes", "ack":
		if operation.Previous == "" || operation.Token == "" || operation.Result != key || operation.RequestDigest != digest || len(operation.Changes.Upserts) != 1 || len(operation.Changes.Deletes) != 0 {
			t.Fatalf("operation=%+v", operation)
		}
		if input.Mode == "changes" && (len(objects) != 1 || objects[0].Key != key || len(deletes) != 0) {
			t.Fatalf("objects=%+v deletes=%v", objects, deletes)
		}
		if input.Mode == "ack" && (operation.State != gitobj.SourceOperationAcknowledged || operation.Pin != "" || operation.RetentionRoot != "") {
			t.Fatalf("ack=%+v", operation)
		}
	}
	result.Complete = true
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	homeguard.ChildExit(0)
}

func TestHelperFreshProcessCost(t *testing.T) {
	if os.Getenv("CCN_REQUIRE_HELPER_COST") != "1" {
		t.Skip("strict hosted helper cost")
	}
	t.Setenv("CCN_MANAGED_COST_REINDEX", "1")
	byMode := make(map[string][]helperCostResult)
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir, target := managedCostFixture(t, count)
			for _, mode := range []string{"refresh", "page", "target", "content", "mutation", "changes", "inspect", "replay", "ack"} {
				result := helperCostRun(t, managedCostInput{Dir: dir, Mode: mode, Expected: target}, count)
				byMode[mode] = append(byMode[mode], result)
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("HELPER_COST_RESULT: %s", data)
				w := result.GraphWork
				if !result.Complete || result.RefListings > 4 || result.RefEntries > 8 || result.PackDecodes > 16 || w.NodeReads > 1024 || w.NodeWrites > 1024 || w.ValueReads > 32 || w.ValueWrites > 32 || w.KeysVisited > 64 || w.DiffPairs > 512 {
					t.Errorf("HELPER_COST_REGRESSION: refs=%d mode=%s result=%s", count, mode, data)
				}
			}
		})
	}
	for mode, pair := range byMode {
		if len(pair) != 2 {
			t.Errorf("HELPER_COST_REGRESSION: %s populations=%d", mode, len(pair))
			continue
		}
		low, high := pair[0], pair[1]
		for _, c := range []struct {
			name                 string
			low, high, allowance int64
		}{
			{"read_return_bytes", low.ReadBytes, high.ReadBytes, 256 << 10},
			{"write_return_bytes", low.WriteBytes, high.WriteBytes, 256 << 10},
			{"minor_faults", low.MinorFaults, high.MinorFaults, 256},
			{"git_processes", low.GitProcesses, high.GitProcesses, 8},
		} {
			if c.high > c.low+c.allowance {
				t.Errorf("HELPER_COST_REGRESSION: %s %s32=%d10000=%d allowance=%d", mode, c.name, c.low, c.high, c.allowance)
			}
		}
		for _, name := range []string{"read", "pread64", "open", "openat", "newfstatat", "fstat", "getdents64", "fsync", "fdatasync"} {
			if high.Syscalls[name] > low.Syscalls[name]+64 {
				t.Errorf("HELPER_COST_REGRESSION: %s %s32=%d10000=%d", mode, name, low.Syscalls[name], high.Syscalls[name])
			}
		}
	}
}

func helperCostRun(t *testing.T, input managedCostInput, count int) helperCostResult {
	t.Helper()
	dir := t.TempDir()
	if base := os.Getenv("CCN_HELPER_COST_ARTIFACT_DIR"); base != "" {
		dir = filepath.Join(base, strconv.Itoa(count), input.Mode)
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
	cmd := exec.CommandContext(t.Context(), "strace", "--seccomp-bpf", "-ff", "-q", "-ttt", "-s", "8192", "-e", "trace=read,pread64,readv,preadv,preadv2,write,pwrite64,writev,pwritev,pwritev2,open,openat,openat2,fstat,newfstatat,statx,getdents64,fsync,fdatasync,close,mmap,mremap,munmap,%process", "-o", trace, os.Args[0], "-test.run=^TestHelperFreshProcessCostChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CCN_HELPER_COST_INPUT="+path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fresh %s: %v stdout=%q stderr=%q", input.Mode, err, stdout.String(), stderr.String())
	}
	if stderr.String() != managedCostBegin+managedCostEnd {
		t.Fatalf("fresh %s stderr=%q", input.Mode, stderr.String())
	}
	var result helperCostResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Mode != input.Mode {
		t.Fatalf("fresh %s result=%q: %v", input.Mode, stdout.String(), err)
	}
	paths, err := filepath.Glob(trace + ".*")
	if err != nil || len(paths) == 0 {
		t.Fatalf("missing syscall traces: %v: %v", paths, err)
	}
	var calls []managedCostCall
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := managedCostParseTrace(data)
		if err != nil {
			t.Fatalf("trace %s: %v", path, err)
		}
		calls = append(calls, parsed...)
	}
	if err := managedCostAccountTrace(&result.managedCostResult, calls); err != nil {
		t.Fatal(err)
	}
	result.Refs = count
	data, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return result
}
