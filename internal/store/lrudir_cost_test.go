package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/homeguard"
)

func TestLRUDirCostHelper(t *testing.T) {
	mode := os.Getenv("CCN_CACHE_COST_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("CCN_CACHE_COST_DIR")
	count, err := strconv.Atoi(os.Getenv("CCN_CACHE_COST_COUNT"))
	if err != nil {
		t.Fatal(err)
	}
	l := &lruDir{capacity: count, dir: dir}
	started := time.Now()
	switch mode {
	case "baseline":
	case "populate":
		for i := range count {
			l.write(fmt.Sprintf("%064d", i), []byte("cached payload"))
		}
		if got := len(l.names()); got != count {
			t.Fatalf("populated %d, want %d", got, count)
		}
	case "write":
		l.write("new", []byte("new payload"))
		if data, ok := l.read("new"); !ok || string(data) != "new payload" {
			t.Fatalf("new payload = %q, %v", data, ok)
		}
	case "owned", "per-access":
		for i := range count {
			name := fmt.Sprintf("%064d", i)
			if data, ok := l.read(name); !ok || string(data) != "cached payload" {
				t.Fatalf("payload %s = %q, %v", name, data, ok)
			}
			l.touch(name)
			if mode == "per-access" {
				if err := l.close(); err != nil {
					t.Fatal(err)
				}
				l = &lruDir{capacity: count, dir: dir}
			}
		}
	default:
		t.Fatalf("unknown cost mode %q", mode)
	}
	walBefore := cacheCostFileSize(t, dir, "index.sqlite-wal")
	dbBefore := cacheCostFileSize(t, dir, "index.sqlite")
	workElapsed := time.Since(started).Nanoseconds()
	closeStarted := time.Now()
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	closeElapsed := time.Since(closeStarted).Nanoseconds()
	walAfter := cacheCostFileSize(t, dir, "index.sqlite-wal")
	dbAfter := cacheCostFileSize(t, dir, "index.sqlite")
	result := map[string]int64{"elapsed_ns": time.Since(started).Nanoseconds(), "work_elapsed_ns": workElapsed, "close_elapsed_ns": closeElapsed, "wal_bytes_before_close": walBefore, "wal_bytes_after_close": walAfter, "db_bytes_before_close": dbBefore, "db_bytes_after_close": dbAfter}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	homeguard.ChildExit(0)
}

func TestLRUDirFreshProcessCost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	var writes []map[string]int
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := t.TempDir()
			baseline := cacheCostProcess(t, "baseline", dir, count, true)
			cacheCostProcess(t, "populate", dir, count, true)
			owned := cacheCostProcess(t, "owned", dir, count, true)
			perAccess := cacheCostProcess(t, "per-access", dir, count, true)
			if count == 10000 && owned["elapsed_ns"] >= perAccess["elapsed_ns"] {
				t.Fatalf("owned handle did not improve listing latency: owned=%v per-access=%v", owned, perAccess)
			}
			ownedOpens := owned["open"] + owned["openat"]
			perAccessOpens := perAccess["open"] + perAccess["openat"]
			if ownedOpens > count+64 || ownedOpens*2 >= perAccessOpens {
				t.Fatalf("owned handle reopened per hit: owned=%v per-access=%v", owned, perAccess)
			}
			if owned["write"] > 8 || owned["pwrite64"] > 8 || owned["fsync"]+owned["fdatasync"] != 0 {
				t.Fatalf("read-only listing wrote cache metadata: %v", owned)
			}
			write := cacheCostProcess(t, "write", dir, count, true)
			writes = append(writes, write)
			for _, name := range []string{"read", "pread64", "open", "openat", "fstat", "newfstatat", "getdents64", "write", "pwrite64", "fsync", "fdatasync"} {
				if write[name] > 512 {
					t.Errorf("new-process write %s exceeded bound: %v", name, write)
				}
			}
			if write["getdents64"] > baseline["getdents64"] {
				t.Fatalf("new-process write enumerated cache directory: %v", write)
			}
		})
	}
	if len(writes) == 2 {
		for _, name := range []string{"read", "pread64", "open", "openat", "fstat", "newfstatat", "getdents64", "write", "pwrite64", "fsync", "fdatasync"} {
			if writes[1][name] > writes[0][name]+64 {
				t.Errorf("fresh write %s grew with cache population: %d to %d", name, writes[0][name], writes[1][name])
			}
		}
	}
}

func cacheCostProcess(t *testing.T, mode, dir string, count int, trace bool) map[string]int {
	t.Helper()
	args := []string{"-test.run=^TestLRUDirCostHelper$", "-test.count=1"}
	command := os.Args[0]
	report := filepath.Join(t.TempDir(), "syscalls.txt")
	if trace {
		args = append([]string{"-f", "-qq", "-c", "-o", report, "-e", "trace=read,pread64,open,openat,fstat,newfstatat,getdents64,write,pwrite64,fsync,fdatasync,mmap", command}, args...)
		command = "strace"
	}
	cmd := exec.CommandContext(t.Context(), command, args...)
	cmd.Env = append(os.Environ(), "CCN_CACHE_COST_MODE="+mode, "CCN_CACHE_COST_DIR="+dir, "CCN_CACHE_COST_COUNT="+strconv.Itoa(count))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cache cost %s: %v: %s", mode, err, out)
	}
	var result map[string]int64
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("cache cost result: %v: %q", err, out)
	}
	counts := map[string]int{"elapsed_ns": int(result["elapsed_ns"])}
	if trace {
		data, err := os.ReadFile(report)
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 || fields[len(fields)-1] == "total" {
				continue
			}
			calls, err := strconv.Atoi(fields[3])
			if err == nil {
				counts[fields[len(fields)-1]] = calls
			}
		}
	}
	t.Logf("cache-cost mode=%s entries=%d elapsed_ns=%d work_ns=%d close_ns=%d wal_before=%d wal_after=%d db_before=%d db_after=%d syscalls=%v", mode, count, result["elapsed_ns"], result["work_elapsed_ns"], result["close_elapsed_ns"], result["wal_bytes_before_close"], result["wal_bytes_after_close"], result["db_bytes_before_close"], result["db_bytes_after_close"], counts)
	return counts
}

func cacheCostFileSize(t *testing.T, dir, name string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
