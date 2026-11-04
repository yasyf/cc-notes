package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const (
	managedCostBegin = "CCN_MANAGED_COST_BEGIN\n"
	managedCostEnd   = "CCN_MANAGED_COST_END\n"
)

type managedCostInput struct {
	Dir      string
	Mode     string
	Expected model.Task
}

type managedCostResult struct {
	Mode          string           `json:"mode"`
	Refs          int              `json:"refs"`
	Snapshots     []model.Task     `json:"snapshots"`
	RefListings   int64            `json:"ref_listings"`
	RefEntries    int64            `json:"ref_entries"`
	PackDecodes   int64            `json:"pack_decodes"`
	MetadataLoads int64            `json:"metadata_loads"`
	GitProcesses  int64            `json:"git_processes"`
	ReadBytes     int64            `json:"read_return_bytes"`
	WriteBytes    int64            `json:"write_return_bytes"`
	FileMapBytes  int64            `json:"file_mapped_bytes"`
	BlockReads    int64            `json:"block_read_bytes"`
	BlockWrites   int64            `json:"block_write_bytes"`
	MinorFaults   int64            `json:"minor_faults"`
	MajorFaults   int64            `json:"major_faults"`
	UserCPU       int64            `json:"user_cpu_ns"`
	SystemCPU     int64            `json:"system_cpu_ns"`
	Elapsed       int64            `json:"elapsed_ns"`
	Syscalls      map[string]int64 `json:"syscalls"`
}

type managedCostUsage struct {
	self     syscall.Rusage
	children syscall.Rusage
}

type managedCostCall struct {
	stamp  time.Duration
	name   string
	args   string
	result int64
}

func TestManagedFreshProcessCostHelper(t *testing.T) {
	path := os.Getenv("CCN_MANAGED_COST_INPUT")
	if path == "" {
		t.Skip("fresh-process managed writer cost helper")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input managedCostInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	ctx, work := gitobj.WithReadWork(t.Context())
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
	s.now = func() time.Time { return time.Unix(1_700_000_100, 0).UTC() }
	result := managedCostResult{Mode: input.Mode}
	switch input.Mode {
	case "open":
	case "read":
		ref, err := s.Resolve(ctx, model.KindTask, string(input.Expected.ID))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.Load(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		result.Snapshots = []model.Task{snapshot.(model.Task)}
	case "query-warm", "query-after-create", "query-after-write":
		label := "selected"
		if input.Mode == "query-after-create" {
			label = "created-in-probe"
		}
		if input.Mode == "query-after-write" {
			label = "after-managed-write"
		}
		values, hit, err := s.IndexedTasks(ctx, []string{label})
		if err != nil || !hit {
			t.Fatalf("indexed query hit=%t: %v", hit, err)
		}
		result.Snapshots = values
	case "create":
		snapshot, err := s.Create(ctx, []model.Op{model.CreateTask{
			Nonce: "0123456789abcdef0123456789abcdef", Title: "managed cost created task",
			Type: model.TypeTask, Priority: 2, Labels: []string{"created-in-probe"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		result.Snapshots = []model.Task{snapshot.(model.Task)}
	case "append":
		snapshot, err := s.Append(ctx, refs.For(model.KindTask, input.Expected.ID), []model.Op{
			model.SetTitle{Title: "managed cost updated selection"}, model.AddLabel{Label: "after-managed-write"},
		})
		if err != nil {
			t.Fatal(err)
		}
		result.Snapshots = []model.Task{snapshot.(model.Task)}
	default:
		t.Fatalf("unknown managed cost mode %q", input.Mode)
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
	result.RefListings, result.RefEntries, result.PackDecodes = work.Snapshot()
	result.MetadataLoads = metadata.Load()
	managedCostVerify(t, s, input, result.Snapshots)
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	homeguard.ChildExit(0)
}

func TestManagedFreshProcessCost(t *testing.T) {
	if os.Getenv("CCN_REQUIRE_MANAGED_COST") != "1" {
		t.Skip("strict hosted managed-writer cost proof")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	byMode := make(map[string][]managedCostResult)
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir, target := managedCostFixture(t, count)
			created := target
			for _, mode := range []string{"open", "read", "query-warm", "create", "query-after-create", "append", "query-after-write"} {
				want := target
				if mode == "query-after-create" {
					want = created
				}
				result := managedCostRun(t, managedCostInput{Dir: dir, Mode: mode, Expected: want}, count)
				byMode[mode] = append(byMode[mode], result)
				if mode == "create" {
					created = result.Snapshots[0]
				}
				if mode == "append" {
					target = result.Snapshots[0]
				}
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("MANAGED_COST_RESULT: %s", data)
				if result.RefListings > 4 || result.RefEntries > 8 || result.PackDecodes > 8 || result.MetadataLoads > 2 {
					t.Errorf("MANAGED_AUTHORITY_COST_REGRESSION: refs=%d mode=%s native_lists=%d native_entries=%d pack_decodes=%d metadata_loads=%d", count, mode, result.RefListings, result.RefEntries, result.PackDecodes, result.MetadataLoads)
				}
			}
		})
	}
	for mode, results := range byMode {
		if len(results) != 2 {
			t.Errorf("MANAGED_AUTHORITY_COST_REGRESSION: mode=%s has %d populations", mode, len(results))
			continue
		}
		small, large := results[0], results[1]
		for _, counter := range []struct {
			name      string
			low, high int64
			allowance int64
		}{
			{"read_return_bytes", small.ReadBytes, large.ReadBytes, 256 << 10},
			{"write_return_bytes", small.WriteBytes, large.WriteBytes, 256 << 10},
			{"minor_faults", small.MinorFaults, large.MinorFaults, 256},
			{"git_processes", small.GitProcesses, large.GitProcesses, 8},
		} {
			if counter.high > counter.low+counter.allowance {
				t.Errorf("MANAGED_AUTHORITY_COST_REGRESSION: mode=%s %s grew:32=%d10000=%d allowance=%d", mode, counter.name, counter.low, counter.high, counter.allowance)
			}
		}
		for _, name := range []string{"read", "pread64", "open", "openat", "newfstatat", "fstat", "getdents64", "fsync", "fdatasync"} {
			if large.Syscalls[name] > small.Syscalls[name]+64 {
				t.Errorf("MANAGED_AUTHORITY_COST_REGRESSION: mode=%s %s grew:32=%d10000=%d", mode, name, small.Syscalls[name], large.Syscalls[name])
			}
		}
		if time.Duration(large.Elapsed) > writeStallLimit {
			t.Errorf("MANAGED_AUTHORITY_COST_REGRESSION: mode=%s stalled for %s", mode, time.Duration(large.Elapsed))
		}
	}
}

const writeStallLimit = 10 * time.Second

func managedCostFixture(t *testing.T, count int) (string, model.Task) {
	t.Helper()
	dir := gittest.InitRepo(t)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, commonDir := gittest.Dirs(t, dir)
	repo, err := gitobj.Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_000, 0)}
	packed := make([]string, 0, count)
	var target model.Task
	for i := range count {
		labels := []string{"unrelated"}
		if i == 0 {
			labels = []string{"selected"}
		}
		pack := model.Pack{Lamport: 1, Ops: []model.Op{model.CreateTask{
			Nonce: model.NewNonce(), Title: fmt.Sprintf("managed cost fixture %05d", i),
			Type: model.TypeTask, Priority: 2, Labels: labels,
		}}}
		sha, err := repo.WriteOpsCommit(t.Context(), nil, sig, "managed cost fixture", pack)
		if err != nil {
			t.Fatal(err)
		}
		packed = append(packed, string(sha)+" "+refs.For(model.KindTask, model.EntityID(sha)))
		if i == 0 {
			snapshot, err := fold.Strict([]model.PackCommit{{SHA: sha, Author: testActor, AuthorTime: sig.When.Unix(), Pack: pack}})
			if err != nil {
				t.Fatal(err)
			}
			target = snapshot.(model.Task)
		}
	}
	slices.Sort(packed)
	if err := os.WriteFile(filepath.Join(commonDir, "packed-refs"), []byte("# pack-refs with: peeled fully-peeled sorted\n"+strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "repack", "-adq")
	if os.Getenv("CCN_MANAGED_COST_REINDEX") == "1" {
		reindex, ok := any(s).(interface{ Reindex(context.Context) error })
		if !ok {
			t.Fatal("explicit managed bootstrap requested but Store.Reindex is absent")
		}
		if err := reindex.Reindex(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	values, hit, err := s.IndexedTasks(t.Context(), []string{"selected"})
	if err != nil || !hit || !reflect.DeepEqual(values, []model.Task{target}) {
		t.Fatalf("fixture bootstrap selected=%+v hit=%t: %v", values, hit, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, target
}

func managedCostVerify(t *testing.T, s *Store, input managedCostInput, snapshots []model.Task) {
	t.Helper()
	if input.Mode == "open" {
		if len(snapshots) != 0 {
			t.Fatal("open returned snapshots")
		}
		return
	}
	if len(snapshots) != 1 {
		t.Fatalf("%s returned %d snapshots, want 1", input.Mode, len(snapshots))
	}
	actual := snapshots[0]
	chain, err := s.Repo.ReadChain(t.Context(), actual.Head)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fold.Strict(chain)
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatalf("%s differs from full authoritative fold: %v", input.Mode, err)
	}
	switch input.Mode {
	case "create":
		if actual.Title != "managed cost created task" || actual.ID == input.Expected.ID || !slices.Equal(actual.Labels, []string{"created-in-probe"}) {
			t.Fatalf("created unexpected task: %+v", actual)
		}
	case "append":
		if actual.ID != input.Expected.ID || actual.Head == input.Expected.Head || actual.Title != "managed cost updated selection" || !slices.Contains(actual.Labels, "after-managed-write") {
			t.Fatalf("appended unexpected task: %+v", actual)
		}
	default:
		if !reflect.DeepEqual(actual, input.Expected) {
			t.Fatalf("%s differs from expected complete snapshot: got=%+v want=%+v", input.Mode, actual, input.Expected)
		}
	}
}

func managedCostRusage(t *testing.T) managedCostUsage {
	t.Helper()
	var usage managedCostUsage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage.self); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Getrusage(syscall.RUSAGE_CHILDREN, &usage.children); err != nil {
		t.Fatal(err)
	}
	return usage
}

func managedCostCPU(a, b syscall.Timeval) int64 {
	return a.Nano() + b.Nano()
}

func managedCostRun(t *testing.T, input managedCostInput, count int) managedCostResult {
	t.Helper()
	dir := t.TempDir()
	if base := os.Getenv("CCN_MANAGED_COST_ARTIFACT_DIR"); base != "" {
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
	cmd := exec.CommandContext(t.Context(), "strace", "--seccomp-bpf", "-ff", "-q", "-ttt", "-s", "256", "-e", "trace=read,pread64,readv,preadv,preadv2,write,pwrite64,writev,pwritev,pwritev2,open,openat,openat2,fstat,newfstatat,statx,getdents64,fsync,fdatasync,close,mmap,mremap,munmap,%process", "-o", trace, os.Args[0], "-test.run=^TestManagedFreshProcessCostHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CCN_MANAGED_COST_INPUT="+path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fresh %s: %v stdout=%q stderr=%q", input.Mode, err, stdout.String(), stderr.String())
	}
	if stderr.String() != managedCostBegin+managedCostEnd {
		t.Fatalf("fresh %s stderr=%q", input.Mode, stderr.String())
	}
	var result managedCostResult
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
	if err := managedCostAccountTrace(&result, calls); err != nil {
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

func managedCostParseTrace(data []byte) ([]managedCostCall, error) {
	completed := regexp.MustCompile(`^([0-9]+\.[0-9]+) ([a-z0-9_]+)\((.*)\)\s+= (-?(?:0x[0-9a-f]+|[0-9]+)|\?)(?:\s.*)?$`)
	unfinished := regexp.MustCompile(`^([0-9]+\.[0-9]+) ([a-z0-9_]+)\((.*)<unfinished \.\.\.>$`)
	resumed := regexp.MustCompile(`^[0-9]+\.[0-9]+ <\.\.\. ([a-z0-9_]+) resumed>(.*)$`)
	// strace prints this for a thread that exit_group took down at syscall entry: the
	// syscall never ran, so it is no evidence as long as the thread does nothing after it.
	exitingThread := regexp.MustCompile(`^[0-9]+\.[0-9]+ \?\?\?\( <unfinished \.\.\.>$`)
	var calls []managedCostCall
	var pending []string
	exiting := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "--- SIG") || strings.Contains(line, "+++ ") {
			continue
		}
		if exiting {
			return nil, fmt.Errorf("syscall after thread exit: %s", line)
		}
		if exitingThread.MatchString(line) {
			exiting = true
			continue
		}
		if match := unfinished.FindStringSubmatch(line); match != nil {
			if pending != nil {
				return nil, fmt.Errorf("nested unfinished syscall: %s", line)
			}
			pending = match
			continue
		}
		if match := resumed.FindStringSubmatch(line); match != nil {
			if pending == nil || pending[2] != match[1] {
				return nil, fmt.Errorf("unmatched resumed syscall: %s", line)
			}
			line = pending[1] + " " + pending[2] + "(" + pending[3] + match[2]
			pending = nil
		}
		match := completed.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("unaccounted syscall trace line: %s", line)
		}
		stamp, err := time.ParseDuration(match[1] + "s")
		if err != nil {
			return nil, err
		}
		result := int64(-1)
		if match[4] != "?" {
			result, err = strconv.ParseInt(match[4], 0, 64)
			if err != nil {
				return nil, err
			}
		}
		calls = append(calls, managedCostCall{stamp: stamp, name: match[2], args: match[3], result: result})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if pending != nil {
		return nil, fmt.Errorf("unfinished syscall at end of trace: %s", pending[0])
	}
	return calls, nil
}

func managedCostAccountTrace(result *managedCostResult, calls []managedCostCall) error {
	var begin, end time.Duration
	for _, call := range calls {
		if call.name != "write" {
			continue
		}
		if strings.HasPrefix(call.args, "2, "+strconv.Quote(managedCostBegin)+",") {
			if begin != 0 || call.result != int64(len(managedCostBegin)) {
				return fmt.Errorf("invalid begin marker")
			}
			begin = call.stamp
		}
		if strings.HasPrefix(call.args, "2, "+strconv.Quote(managedCostEnd)+",") {
			if end != 0 || call.result != int64(len(managedCostEnd)) {
				return fmt.Errorf("invalid end marker")
			}
			end = call.stamp
		}
	}
	if begin == 0 || end <= begin {
		return fmt.Errorf("missing or reversed operation markers")
	}
	result.Syscalls = make(map[string]int64)
	for _, call := range calls {
		if call.stamp <= begin || call.stamp >= end {
			continue
		}
		result.Syscalls[call.name]++
		if call.result < 0 {
			continue
		}
		switch call.name {
		case "read", "pread64", "readv", "preadv", "preadv2":
			result.ReadBytes += call.result
		case "write", "pwrite64", "writev", "pwritev", "pwritev2":
			result.WriteBytes += call.result
		case "execve", "execveat":
			args := call.args
			if call.name == "execveat" {
				_, args, _ = strings.Cut(args, ", ")
			}
			path, _, ok := strings.Cut(args, ", ")
			if !ok {
				return fmt.Errorf("invalid exec arguments: %s", call.args)
			}
			name, err := strconv.Unquote(path)
			if err != nil {
				return err
			}
			if filepath.Base(name) == "git" {
				result.GitProcesses++
			}
		case "mmap":
			fields := strings.Split(call.args, ", ")
			if len(fields) != 6 {
				return fmt.Errorf("invalid mmap arguments: %s", call.args)
			}
			if fields[4] != "-1" {
				size, err := strconv.ParseInt(fields[1], 10, 64)
				if err != nil {
					return err
				}
				result.FileMapBytes += size
			}
		}
	}
	if result.Syscalls["read"] == 0 || result.GitProcesses == 0 {
		return fmt.Errorf("operation lacks actual read and Git process evidence: %+v", result)
	}
	return nil
}
