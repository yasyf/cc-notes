package cli_test

import (
	"bytes"
	"context"
	"errors"
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

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func taskOpenPath(name, args string) (string, error) {
	if name != "open" {
		var ok bool
		_, args, ok = strings.Cut(args, ",")
		if !ok {
			return "", errors.New("missing open directory argument")
		}
	}
	args = strings.TrimSpace(args)
	if !strings.HasPrefix(args, "\"") {
		return "", errors.New("unavailable open pathname")
	}
	escaped, end := false, 0
	for i := 1; i < len(args); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch args[i] {
		case '\\':
			escaped = true
		case '"':
			end = i + 1
		}
		if end != 0 {
			break
		}
	}
	if end == 0 || !strings.HasPrefix(strings.TrimSpace(args[end:]), ",") {
		return "", errors.New("truncated open pathname")
	}
	return strconv.Unquote(args[:end])
}

func parseTaskCacheLookups(r io.Reader, roots map[string]bool) (map[string]bool, error) {
	lookedUp := make(map[string]bool)
	observed := 0
	err := scanTaskSyscalls(r, func(name, args, result string) error {
		if name != "open" && name != "openat" && name != "openat2" {
			return fmt.Errorf("unexpected open trace syscall %q", name)
		}
		fields := strings.Fields(result)
		if len(fields) == 0 {
			return errors.New("empty open result")
		}
		if !(len(fields) > 1 && fields[0] == "?" && fields[1] == "ERESTARTSYS") {
			if _, err := strconv.ParseInt(fields[0], 10, 64); err != nil {
				return fmt.Errorf("unknown open result: %w", err)
			}
		}
		path, err := taskOpenPath(name, args)
		if err != nil {
			return err
		}
		observed++
		path = filepath.Clean(path)
		parent, base := filepath.Base(filepath.Dir(path)), filepath.Base(path)
		if len(parent) == 2 && len(base) == 38 && roots[parent+base] {
			lookedUp[parent+base] = true
		}
		return nil
	})
	if err == nil && observed == 0 {
		err = errors.New("empty open-attempt trace")
	}
	return lookedUp, err
}

func taskCacheFixtureRoots(t *testing.T, dir string, count int) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "packed-refs"))
	if err != nil {
		t.Fatal(err)
	}
	roots := make(map[string]bool, count)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 40 || !plumbing.IsHash(fields[0]) || fields[1] != refs.For(model.KindTask, model.EntityID(fields[0])) {
			t.Fatalf("non-root fixture reference %q", line)
		}
		roots[fields[0]] = true
	}
	if len(roots) != count {
		t.Fatalf("fixture roots=%d, want %d", len(roots), count)
	}
	return roots
}

func validateTaskCacheList(t *testing.T, output string, roots map[string]bool, extraTitle string) {
	t.Helper()
	short := make(map[string]int, len(roots))
	for root := range roots {
		short[model.EntityID(root).Short()]++
	}
	titles := make([]bool, len(roots))
	extras := 0
	rows := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	for _, line := range rows {
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) != 5 || fields[1] != "open" || fields[2] != "P2" || fields[3] != "-" {
			t.Fatalf("unexpected fixture row %q", line)
		}
		if extraTitle != "" && fields[4] == extraTitle {
			extras++
			continue
		}
		ordinal, err := strconv.Atoi(strings.TrimPrefix(fields[4], "fixture "))
		if err != nil || fields[4] != fmt.Sprintf("fixture %d", ordinal) || ordinal < 0 || ordinal >= len(titles) || titles[ordinal] || short[fields[0]] == 0 {
			t.Fatalf("unexpected fixture identity/title %q", line)
		}
		titles[ordinal] = true
		short[fields[0]]--
	}
	wantExtras := 0
	if extraTitle != "" {
		wantExtras = 1
	}
	if extras != wantExtras || len(rows) != len(roots)+wantExtras {
		t.Fatalf("fixture output rows=%d extras=%d, want %d/%d", len(rows), extras, len(roots)+wantExtras, wantExtras)
	}
	for _, count := range short {
		if count != 0 {
			t.Fatal("fixture short-ID multiplicities differ")
		}
	}
	for _, seen := range titles {
		if !seen {
			t.Fatal("fixture title omitted")
		}
	}
}

func tracedTaskCacheLookups(t *testing.T, dir, binary string, roots map[string]bool, extraTitle string) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), binTimeout)
	defer cancel()
	report := filepath.Join(t.TempDir(), "cache-open-attempts.txt")
	args := []string{"-f", "-qq", "-s", "4096", "-o", report, "-e", "trace=open,openat,openat2", binary, "task", "list", "--all", "--all-branches"}
	cmd := exec.CommandContext(ctx, "strace", args...)
	cmd.Dir = dir
	cmd.Env = binEnv(actorA)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("cache-hit list: %v; stderr %q", err, stderr.String())
	}
	elapsed := time.Since(started)
	if stderr.Len() != 0 {
		t.Fatalf("cache-hit list stderr %q", stderr.String())
	}
	validateTaskCacheList(t, stdout.String(), roots, extraTitle)
	f, err := os.Open(report)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil || info.Size() > 64<<20 {
		closeErr := f.Close()
		t.Fatalf("open trace is unreadable or exceeds64MiB: info=%v stat=%v close=%v", info, err, closeErr)
	}
	lookedUp, err := parseTaskCacheLookups(f, roots)
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
	minimum := len(roots) - len(lookedUp)
	t.Logf("known_initial_roots=%d distinct_source_roots_looked_up=%d minimum_cache_hits=%d minimum_hit_fraction=%.6f trace_file_bytes=%d separate_list_ms=%.3f raw_traced_wrapper_rusage=%+v", len(roots), len(lookedUp), minimum, float64(minimum)/float64(len(roots)), info.Size(), float64(elapsed)/float64(time.Millisecond), cmd.ProcessState.SysUsage())
	return lookedUp
}

func TestTaskCacheHitTraceAccounting(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	roots := map[string]bool{a: true, b: true, c: true}
	trace := fmt.Sprintf("101 openat(AT_FDCWD, %q, O_RDONLY) = 3\n", "/tmp/objects/aa/"+a[2:]) +
		fmt.Sprintf("101 openat(4, %q, O_RDONLY) = -1 ENOENT (No such file)\n", "bb/"+b[2:]) +
		"102 open(\"/tmp/unrelated\", O_RDONLY <unfinished ...>\n" +
		fmt.Sprintf("101 openat2(4, %q, {flags=O_RDONLY}, 24) = 5\n", "/other/objects/aa/"+a[2:]) +
		"102 <... open resumed>) = -1 ENOENT (No such file)\n" +
		fmt.Sprintf("101 openat(AT_FDCWD, %q, O_RDONLY) = ? ERESTARTSYS (To be restarted)\n", "/tmp/objects/cc/"+c[2:])
	got, err := parseTaskCacheLookups(strings.NewReader(trace), roots)
	if err != nil || !reflect.DeepEqual(got, roots) {
		t.Fatalf("source attempts=%v, %v; want %v", got, err, roots)
	}
	for _, bad := range []string{
		"",
		"101 ???(\n",
		"101 openat(AT_FDCWD, 0x1, O_RDONLY) = 3\n",
		"101 openat(AT_FDCWD, \"/tmp/x\"..., O_RDONLY) = 3\n",
		"101 openat(AT_FDCWD, \"/tmp/x\", O_RDONLY) = ?\n",
		"101 openat(AT_FDCWD, \"/tmp/x\", <unfinished ...>\n",
		"101 <... openat resumed>) = 3\n",
		"101 read(3, \"x\", 1) = 1\n",
	} {
		if _, err := parseTaskCacheLookups(strings.NewReader(bad), roots); err == nil {
			t.Fatalf("accepted incomplete lookup accounting %q", bad)
		}
	}
}

func TestTaskCacheHitLowerBoundCalibration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cache hit observation requires Linux strace")
	}
	binary := os.Getenv("CCN_PRODUCTIONLIKE_CLI")
	if binary == "" {
		t.Fatal("production-like candidate binary is required")
	}
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed=%t", packed), func(t *testing.T) {
			dir := taskDedupeFixture(t, 31)
			roots := taskCacheFixtureRoots(t, dir, 32)
			if packed {
				gittest.Git(t, dir, "-c", "pack.threads=2", "repack", "-ad")
				gittest.Git(t, dir, "prune-packed")
			}
			for root := range roots {
				info, err := os.Lstat(filepath.Join(dir, ".git", "objects", root[:2], root[2:]))
				if packed && !errors.Is(err, os.ErrNotExist) || !packed && (err != nil || !info.Mode().IsRegular()) {
					t.Fatalf("fixture storage packed=%t: %v", packed, err)
				}
			}
			if got := len(tracedTaskCacheLookups(t, dir, binary, roots, "")); got != len(roots) {
				t.Fatalf("cold negative control observed %d/%d source roots", got, len(roots))
			}
			if got := len(tracedTaskCacheLookups(t, dir, binary, roots, "")); got != 0 {
				t.Fatalf("warm positive control observed %d source roots", got)
			}
		})
	}
}
