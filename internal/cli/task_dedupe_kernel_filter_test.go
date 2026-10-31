package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTaskIOBytesKernelFilter(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	filters := ""
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Seccomp_filters:" {
			filters = fields[1]
		}
	}
	if filters != "0" {
		t.Fatalf("calibration requires no inherited seccomp filters, got %q", filters)
	}
	dir := t.TempDir()
	queryDir := filepath.Join(dir, "cc-notes-query")
	if err := os.Mkdir(queryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const program = `import os
import sys
import threading

with open("/proc/self/status", encoding="ascii") as status:
    filters = dict(line.split(":", 1) for line in status if ":" in line)["Seccomp_filters"].strip()
assert filters == "1", filters
fd = os.open(sys.argv[1], os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
data = b"x" * 4096
for _ in range(16):
    assert os.write(fd, data) == len(data)
os.lseek(fd, 0, os.SEEK_SET)
for _ in range(16):
    assert os.read(fd, len(data)) == data
for offset in range(0, 65536, len(data)):
    assert os.pread(fd, len(data), offset) == data
    assert os.pwrite(fd, data, offset) == len(data)
os.close(fd)
started = threading.Event()
condition = threading.Condition()
def blocked():
    with condition:
        started.set()
        condition.wait()
threading.Thread(target=blocked, daemon=True).start()
started.wait()
with condition:
    print("Seccomp_filters=" + filters, flush=True)
    os._exit(0)
`
	ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
	defer cancel()
	report := filepath.Join(dir, "byte-syscalls.txt")
	cmd := exec.CommandContext(ctx, "strace", taskByteTraceArgs(report, "python3", "-I", "-S", "-c", program, filepath.Join(queryDir, "payload"))...)
	cmd.Dir = dir
	cmd.Env = binEnv(actorA)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("kernel filter calibration: %v; stdout %q; stderr %q", err, stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "Seccomp_filters=1" || stderr.Len() != 0 {
		t.Fatalf("kernel filter calibration output: stdout %q; stderr %q", stdout.String(), stderr.String())
	}
	f, err := os.Open(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 16<<20 {
		t.Fatalf("calibration trace exceeds 16 MiB: %d", info.Size())
	}
	work, err := parseTaskIOBytes(f)
	if err != nil {
		logTaskByteTraceFailure(t, f)
		t.Fatal(err)
	}
	for _, name := range []string{"read", "pread64", "write", "pwrite64"} {
		if work.QueryBytes[name] != 65536 {
			t.Fatalf("kernel filter returned-byte calibration %s: got %d, want 65536; work %+v", name, work.QueryBytes[name], work)
		}
	}
	t.Logf("parent_filters=%s child_filters=1 calibration_query_bytes=%v", filters, work.QueryBytes)
}
