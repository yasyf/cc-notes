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
	"testing"
	"time"
)

type taskIOBytes struct {
	Restarted  map[string]int64
	Calls      map[string]int64
	Bytes      map[string]int64
	QueryBytes map[string]int64
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
	args := []string{"-f", "-qq", "-yy", "-s", "1", "-o", report, "-e", "trace=read,pread64,write,pwrite64", "--status=successful", binary, "task", "add", title, "--backlog", "--label", "target", "--no-validation-criteria"}
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
	f, err := os.Open(report)
	if err != nil {
		t.Fatal(err)
	}
	work, err := parseTaskIOBytes(f)
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
	t.Logf("unrelated=%d byte_create=%d elapsed_ms=%.3f successful_return_calls=%v restarted_zero_bytes=%v returned_bytes=%v query_db_returned_bytes=%v user_cpu=%v system_cpu=%v raw_traced_wrapper_rusage=%+v", unrelated, ordinal, float64(elapsed)/float64(time.Millisecond), work.Calls, work.Restarted, work.Bytes, work.QueryBytes, cmd.ProcessState.UserTime(), cmd.ProcessState.SystemTime(), cmd.ProcessState.SysUsage())
}

func parseTaskIOBytes(r io.Reader) (taskIOBytes, error) {
	work := taskIOBytes{Restarted: make(map[string]int64), Calls: make(map[string]int64), Bytes: make(map[string]int64), QueryBytes: make(map[string]int64)}
	pending := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		pid, event, ok := strings.Cut(line, " ")
		if !ok {
			return work, fmt.Errorf("missing trace PID: %q", line)
		}
		event = strings.TrimSpace(event)
		if strings.HasPrefix(event, "--- ") || strings.HasPrefix(event, "+++ ") {
			continue
		}
		if prefix, ok := strings.CutSuffix(event, "<unfinished ...>"); ok {
			if pending[pid] != "" {
				return work, fmt.Errorf("overlapping unfinished syscall: %q", line)
			}
			pending[pid] = prefix
			continue
		}
		if strings.HasPrefix(event, "<... ") {
			name, suffix, ok := strings.Cut(strings.TrimPrefix(event, "<... "), " resumed>")
			if !ok || !strings.HasPrefix(pending[pid], name+"(") {
				return work, fmt.Errorf("unpaired resumed syscall: %q", line)
			}
			event = pending[pid] + suffix
			delete(pending, pid)
		}
		name, args, ok := strings.Cut(event, "(")
		if !ok || (name != "read" && name != "pread64" && name != "write" && name != "pwrite64") {
			return work, fmt.Errorf("unexpected traced syscall: %q", line)
		}
		result := strings.LastIndex(event, " = ")
		if result < 0 {
			return work, fmt.Errorf("missing syscall result: %q", line)
		}
		fields := strings.Fields(event[result+3:])
		if len(fields) == 0 {
			return work, fmt.Errorf("empty syscall result: %q", line)
		}
		if len(fields) > 1 && fields[0] == "?" && fields[1] == "ERESTARTSYS" {
			work.Calls[name]++
			work.Restarted[name]++
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return work, fmt.Errorf("parse syscall result %q: %w", line, err)
		}
		work.Calls[name]++
		if n > 0 {
			work.Bytes[name] += n
			fd, _, _ := strings.Cut(args, ",")
			if strings.Contains(fd, "/cc-notes-query/") {
				work.QueryBytes[name] += n
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return work, err
	}
	if len(pending) != 0 {
		return work, fmt.Errorf("unreturned syscalls: %v", pending)
	}
	return work, nil
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
	want := taskIOBytes{Restarted: map[string]int64{"read": 1}, Calls: map[string]int64{"pread64": 2, "read": 4, "pwrite64": 1, "write": 1}, Bytes: map[string]int64{"pread64": 4096, "read": 10004, "pwrite64": 8192, "write": 12}, QueryBytes: map[string]int64{"pread64": 4096, "pwrite64": 8192}}
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
