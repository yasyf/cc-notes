package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type taskIOBytes struct {
	Restarted  map[string]int64
	Calls      map[string]int64
	Bytes      map[string]int64
	QueryBytes map[string]int64
	PathBytes  map[string]map[string]int64
}

func TestTaskDedupeColdCLIBytes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	for _, candidate := range []struct{ name, binary string }{
		{"candidate", os.Getenv("CCN_PRODUCTIONLIKE_CLI")},
		{"legacy", os.Getenv("CCN_LEGACY_BASELINE_CLI")},
	} {
		if candidate.binary == "" {
			t.Fatalf("%s binary is required", candidate.name)
		}
		t.Run(candidate.name, func(t *testing.T) {
			for _, count := range []int{32, 10000} {
				t.Run(strconv.Itoa(count), func(t *testing.T) {
					dir := taskDedupeFixture(t, count)
					t.Logf("byte_phase=cold cli=%q unrelated=%d", candidate.binary, count)
					tracedTaskCreateBytes(t, dir, candidate.binary, count, 0)
				})
			}
		})
	}
}

func tracedTaskCreateBytes(t *testing.T, dir, binary string, unrelated, ordinal int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
	defer cancel()
	report := filepath.Join(t.TempDir(), "byte-syscalls.txt")
	title := fmt.Sprintf("byte probe %d", ordinal)
	var hitRoots map[string]bool
	if ordinal == 0 && binary == os.Getenv("CCN_PRODUCTIONLIKE_CLI") {
		hitRoots = taskCacheFixtureRoots(t, dir, unrelated+1)
	}
	args := taskByteTraceArgs(report, binary, "task", "add", title, "--backlog", "--label", "target", "--no-validation-criteria")
	cmd := exec.CommandContext(ctx, "strace", args...)
	cmd.Dir = dir
	cmd.Env = binEnv(actorA)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("byte create: %v; stdout %q; stderr %q", err, stdout.String(), stderr.String())
	}
	elapsed := time.Since(started)
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "\topen\tP2\t-\t"+title+"\n") {
		t.Fatalf("byte create output: stdout %q; stderr %q", stdout.String(), stderr.String())
	}
	var allocated map[string]int64
	if ordinal == 0 && unrelated == 32 && binary == os.Getenv("CCN_PRODUCTIONLIKE_CLI") {
		allocated = taskCacheColdAllocation(t, dir)
	}
	if hitRoots != nil {
		t.Run("cache_hit_lower_bound", func(t *testing.T) {
			tracedTaskCacheLookups(t, dir, binary, hitRoots, title)
		})
	}
	f, err := os.Open(report)
	if err != nil {
		t.Fatal(err)
	}
	work, err := parseTaskIOBytes(f)
	if err != nil {
		logTaskByteTraceFailure(t, f)
	}
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if work.Calls["read"] == 0 || work.Bytes["read"] == 0 {
		t.Fatalf("missing returned-byte accounting: %+v", work)
	}
	for namespace, size := range allocated {
		prefix := "fold_cache"
		if namespace == "relevant-v6" {
			prefix = "relevance_cache"
		}
		if writes := work.PathBytes[prefix+"_payload"]; writes["write"] != 0 || writes["pwrite64"] != 0 {
			t.Fatalf("cold allocation fixture has external %s payload writes: %v", namespace, writes)
		}
		if size > 1<<20 {
			t.Errorf("cold %s allocated %d bytes, exceeding 1MiB absolute bound", namespace, size)
		}
		t.Logf("cold_namespace=%s allocated_endpoint_bytes=%d additional_bytes_upper_bound=%d limit_bytes=%d", namespace, size, size, 1<<20)
	}
	t.Logf("unrelated=%d byte_create=%d elapsed_ms=%.3f successful_return_calls=%v restarted_zero_bytes=%v returned_bytes=%v query_db_returned_bytes=%v path_class_returned_bytes=%v user_cpu=%v system_cpu=%v raw_traced_wrapper_rusage=%+v", unrelated, ordinal, float64(elapsed)/float64(time.Millisecond), work.Calls, work.Restarted, work.Bytes, work.QueryBytes, work.PathBytes, cmd.ProcessState.UserTime(), cmd.ProcessState.SystemTime(), cmd.ProcessState.SysUsage())
}

func taskByteTraceArgs(report string, command ...string) []string {
	prefix := [...]string{"-f", "--seccomp-bpf", "-qq", "-yy", "-s", "1", "-o", report, "-e", "trace=read,pread64,write,pwrite64", "--status=successful"}
	args := make([]string, 0, len(prefix)+len(command))
	args = append(args, prefix[:]...)
	return append(args, command...)
}

func logTaskByteTraceFailure(t *testing.T, f *os.File) {
	t.Helper()
	offset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Logf("byte trace diagnostic position: %v", err)
		return
	}
	start := max(int64(0), offset-(64<<10))
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		t.Logf("byte trace diagnostic seek: %v", err)
		return
	}
	window, err := io.ReadAll(io.LimitReader(f, 128<<10))
	t.Logf("byte_trace_failure_window_start=%d scanner_file_offset=%d raw=%q diagnostic_read_error=%v", start, offset, window, err)
}

func taskIOPathClass(fd string) string {
	if strings.Contains(fd, "/cc-notes-query/") {
		return "query_metadata"
	}
	base := filepath.Base(strings.TrimSuffix(strings.TrimSuffix(fd, ">"), " (deleted)"))
	for _, cache := range []struct{ prefix, class string }{
		{"/cc-notes/folds-", "fold_cache"},
		{"/cc-notes/relevant-", "relevance_cache"},
	} {
		if strings.Contains(fd, cache.prefix) {
			switch base {
			case "index.sqlite", "index.sqlite-wal", "index.sqlite-shm":
				return cache.class + "_sqlite"
			case "index.bin":
				return cache.class + "_hint_metadata"
			case "values.bin":
				return cache.class + "_inline_payload"
			}
			if base == ".pending" || strings.HasPrefix(base, "slot-") {
				return cache.class + "_payload"
			}
			return cache.class + "_other"
		}
	}
	if base == "packed-refs" {
		return "packed_refs"
	}
	if strings.Contains(fd, "/.git/objects/") {
		return "git_objects"
	}
	return "other"
}

func scanTaskSyscalls(r io.Reader, visit func(name, args, result string) error) error {
	pending := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		pid, event, ok := strings.Cut(line, " ")
		if !ok {
			return fmt.Errorf("missing trace PID: %q", line)
		}
		event = strings.TrimSpace(event)
		if strings.HasPrefix(event, "--- ") || strings.HasPrefix(event, "+++ ") {
			continue
		}
		if prefix, ok := strings.CutSuffix(event, "<unfinished ...>"); ok {
			if pending[pid] != "" {
				return fmt.Errorf("overlapping unfinished syscall: %q", line)
			}
			pending[pid] = prefix
			continue
		}
		if strings.HasPrefix(event, "<... ") {
			name, suffix, ok := strings.Cut(strings.TrimPrefix(event, "<... "), " resumed>")
			if !ok || !strings.HasPrefix(pending[pid], name+"(") {
				return fmt.Errorf("unpaired resumed syscall: %q", line)
			}
			event = pending[pid] + suffix
			delete(pending, pid)
		}
		name, _, ok := strings.Cut(event, "(")
		result := strings.LastIndex(event, " = ")
		if !ok || result < len(name)+1 {
			return fmt.Errorf("missing syscall frame: %q", line)
		}
		if err := visit(name, event[len(name)+1:result], event[result+3:]); err != nil {
			return fmt.Errorf("trace %q: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(pending) != 0 {
		return fmt.Errorf("unreturned syscalls: %v", pending)
	}
	return nil
}

func parseTaskIOBytes(r io.Reader) (taskIOBytes, error) {
	work := taskIOBytes{Restarted: make(map[string]int64), Calls: make(map[string]int64), Bytes: make(map[string]int64), QueryBytes: make(map[string]int64), PathBytes: make(map[string]map[string]int64)}
	err := scanTaskSyscalls(r, func(name, args, result string) error {
		if name != "read" && name != "pread64" && name != "write" && name != "pwrite64" {
			return fmt.Errorf("unexpected traced syscall: %q", name)
		}
		fields := strings.Fields(result)
		if len(fields) == 0 {
			return fmt.Errorf("empty syscall result")
		}
		if len(fields) > 1 && fields[0] == "?" && fields[1] == "ERESTARTSYS" {
			work.Calls[name]++
			work.Restarted[name]++
			return nil
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return fmt.Errorf("parse syscall result: %w", err)
		}
		work.Calls[name]++
		if n > 0 {
			work.Bytes[name] += n
			fd, _, _ := strings.Cut(args, ",")
			if strings.Contains(fd, "/cc-notes-query/") {
				work.QueryBytes[name] += n
			}
			class := taskIOPathClass(fd)
			if work.PathBytes[class] == nil {
				work.PathBytes[class] = make(map[string]int64)
			}
			work.PathBytes[class][name] += n
		}
		return nil
	})
	return work, err
}

func TestTaskIOBytesTraceAccounting(t *testing.T) {
	trace := "101 pread64(3</tmp/repo/.git/cc-notes-query/tasks.db>, \"x\"..., 4096, 0) = 4096\n" +
		"102 read(4</tmp/repo/.git/objects/ab/cd>,  <unfinished ...>\n" +
		"101 pwrite64(3</tmp/repo/.git/cc-notes-query/tasks.db>, \"y\"..., 8192, 0) = 8192\n" +
		"102 <... read resumed>\"z\"..., 16384) = 10000\n" +
		"101 write(1<pipe:[1]>, \"o\"..., 12) = 12\n" +
		"101 read(9</tmp/empty>, \"\", 16) = 0\n" +
		"101 read(5<pipe:[2]>, 0x1, 8192) = ? ERESTARTSYS (To be restarted if SA_RESTART is set)\n" +
		"101 read(5<pipe:[2]>, \"p\"..., 8192) = 4\n" +
		"101 pread64(3</tmp/repo/.git/cc-notes-query/tasks.db>, 0x1, 4096, 0) = -1 EINTR (Interrupted system call)\n"
	got, err := parseTaskIOBytes(strings.NewReader(trace))
	if err != nil {
		t.Fatal(err)
	}
	want := taskIOBytes{Restarted: map[string]int64{"read": 1}, Calls: map[string]int64{"pread64": 2, "read": 4, "pwrite64": 1, "write": 1}, Bytes: map[string]int64{"pread64": 4096, "read": 10004, "pwrite64": 8192, "write": 12}, QueryBytes: map[string]int64{"pread64": 4096, "pwrite64": 8192}, PathBytes: map[string]map[string]int64{"query_metadata": {"pread64": 4096, "pwrite64": 8192}, "git_objects": {"read": 10000}, "other": {"read": 4, "write": 12}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for _, bad := range []string{
		"4388  ???( <detached ...>\n",
		"101 <... read resumed>\"x\", 1) = 1\n",
		"101 read(3, <unfinished ...>\n",
		"101 read(3, \"x\", 1) = ?\n",
	} {
		if _, err := parseTaskIOBytes(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted incomplete accounting %q", bad)
		}
	}
}

func TestTaskIOBytesPathPartition(t *testing.T) {
	trace := `101 pwrite64(3</tmp/repo/.git/cc-notes/folds-v3/index.sqlite-wal>, "x", 8, 0) = 8
101 write(4</tmp/repo/.git/cc-notes/folds-v3/.pending>, "p", 7) = 7
101 read(5</tmp/repo/.git/cc-notes/relevant-v4/slot-00000000>, "y", 5) = 5
101 pwrite64(6</tmp/repo/.git/cc-notes/relevant-v4/index.sqlite-shm>, "z", 9, 0) = 9
101 pread64(7</tmp/repo/.git/packed-refs>, "r", 16, 0) = 16
101 read(8</tmp/repo/.git/cc-notes/folds-v2/abc>, "o", 3) = 3
101 read(9</tmp/repo/.git/cc-notes-query/hash.db>, "q", 2) = 2
101 write(1<pipe:[1]>, "m", 4) = 4
101 pwrite64(10</tmp/repo/.git/cc-notes/folds-v5/index.bin>, "h", 56, 0) = 56
101 pwrite64(11</tmp/repo/.git/cc-notes/folds-v5/values.bin>, "i", 512, 1024) = 512
101 pread64(12</tmp/repo/.git/cc-notes/relevant-v6/index.bin>, "h", 56, 0) = 56
101 pread64(13</tmp/repo/.git/cc-notes/relevant-v6/values.bin>, "i", 88, 2048) = 88
`
	work, err := parseTaskIOBytes(strings.NewReader(trace))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]int64{
		"fold_cache_sqlite":              {"pwrite64": 8},
		"fold_cache_payload":             {"write": 7},
		"relevance_cache_payload":        {"read": 5},
		"relevance_cache_sqlite":         {"pwrite64": 9},
		"packed_refs":                    {"pread64": 16},
		"fold_cache_other":               {"read": 3},
		"query_metadata":                 {"read": 2},
		"other":                          {"write": 4},
		"fold_cache_hint_metadata":       {"pwrite64": 56},
		"fold_cache_inline_payload":      {"pwrite64": 512},
		"relevance_cache_hint_metadata":  {"pread64": 56},
		"relevance_cache_inline_payload": {"pread64": 88},
	}
	if !reflect.DeepEqual(work.PathBytes, want) {
		t.Fatalf("path partition = %v, want %v", work.PathBytes, want)
	}
	totals := make(map[string]int64)
	for _, family := range work.PathBytes {
		for name, count := range family {
			totals[name] += count
		}
	}
	if !reflect.DeepEqual(totals, work.Bytes) || !reflect.DeepEqual(work.PathBytes["query_metadata"], work.QueryBytes) {
		t.Fatalf("partition differs from totals: partition=%v all=%v query=%v", totals, work.Bytes, work.QueryBytes)
	}
}

func taskCacheColdAllocation(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	allocated := make(map[string]int64, 2)
	for _, namespace := range []string{"folds-v5", "relevant-v6"} {
		var total int64
		root := filepath.Join(dir, ".git", "cc-notes", namespace)
		for _, name := range []string{root, filepath.Join(root, "index.bin"), filepath.Join(root, "values.bin")} {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			total += info.Sys().(*syscall.Stat_t).Blocks * 512
		}
		allocated[namespace] = total
	}
	return allocated
}
