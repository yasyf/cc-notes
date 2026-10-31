package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	capacity := count * 4
	if mode == "populate-default" || mode == "default-rate" {
		capacity = foldCacheCap
	}
	l := &lruDir{capacity: capacity, dir: dir}
	var metadataOpens, payloadOpens, validated, rankSum int64
	var hits, misses int64
	started := time.Now()
	switch mode {
	case "baseline":
	case "missing":
		for i := range count {
			if _, ok := l.entry(fmt.Sprintf("%064d", i)); ok {
				t.Fatal("missing entry became a hit")
			}
		}
		if !l.lookupMissing || l.index != nil {
			t.Fatalf("missing owner state: absent=%v index=%v", l.lookupMissing, l.index)
		}
	case "populate":
		for i := range count {
			l.write(fmt.Sprintf("%064d", i), []byte("cached payload"))
		}
	case "populate-batch", "populate-default":
		cache := newFoldCache(dir, capacity)
		l = &cache.lruDir
		batch := newFoldPublicationBatch(t.Context(), cache)
		for i := range count {
			batch.add(&cachePublication{name: fmt.Sprintf("%064d", i), data: []byte("cached payload")})
		}
		batch.flush()
	case "write":
		l.write("new", []byte("new payload"))
		if data, ok := l.read("new"); !ok || string(data) != "new payload" {
			t.Fatalf("new payload = %q, %v", data, ok)
		}
	case "requested":
		names := []string{fmt.Sprintf("%064d", 0), fmt.Sprintf("%064d", count/2), fmt.Sprintf("%064d", count-1)}
		entries := l.entries(t.Context(), names)
		if len(entries) != len(names) {
			t.Fatalf("requested %d entries, got %d", len(names), len(entries))
		}
		for _, name := range names {
			if data, ok := l.readEntry(entries[name]); !ok || string(data) != "cached payload" {
				t.Fatalf("requested payload %s = %q, %v", name, data, ok)
			}
		}
	case "owned", "per-access":
		var previousIndex *os.File
		for i := range count {
			name := fmt.Sprintf("%064d", i)
			entry, ok := l.entry(name)
			if !ok {
				t.Fatalf("hint %s missing", name)
			}
			if l.index != previousIndex {
				metadataOpens++
				previousIndex = l.index
			}
			reader, ok := l.openEntry(entry)
			if !ok {
				t.Fatalf("payload %s missing", name)
			}
			payloadOpens++
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || string(data) != "cached payload" {
				t.Fatalf("payload %s = %q, read=%v close=%v", name, data, readErr, closeErr)
			}
			l.rememberHit(entry)
			if mode == "per-access" {
				if err := l.close(); err != nil {
					t.Fatal(err)
				}
				if _, err := previousIndex.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("closed metadata handle = %v", err)
				}
				l = &lruDir{capacity: capacity, dir: dir}
			}
		}
	case "default-rate":
		for i := range count {
			name := fmt.Sprintf("%064d", i)
			entry, ok := l.entry(name)
			if !ok {
				misses++
				continue
			}
			reader, ok := l.openEntry(entry)
			if !ok {
				misses++
				continue
			}
			payloadOpens++
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || string(data) != "cached payload" {
				t.Fatalf("default-capacity payload %s = %q, read=%v close=%v", name, data, readErr, closeErr)
			}
			hits++
		}
		if l.index != nil {
			metadataOpens = 1
		}
	default:
		t.Fatalf("unknown cost mode %q", mode)
	}
	if mode == "populate" || mode == "populate-batch" {
		names := l.names()
		if len(names) != count {
			t.Fatalf("population entries=%d want=%d", len(names), count)
		}
		for _, name := range names {
			entry, ok := l.entry(name)
			if !ok || entry.rank == 0 || entry.rank > uint64(count) {
				t.Fatalf("population hint %s = %+v, %v", name, entry, ok)
			}
			if data, ok := l.readEntry(entry); !ok || string(data) != "cached payload" {
				t.Fatalf("population payload %s = %q, %v", name, data, ok)
			}
			validated++
			rankSum += int64(entry.rank)
		}
	}
	indexBefore := cacheCostFileSize(t, dir, cacheHintFile)
	workElapsed := time.Since(started).Nanoseconds()
	closeStarted := time.Now()
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	closeElapsed := time.Since(closeStarted).Nanoseconds()
	indexAfter := cacheCostFileSize(t, dir, cacheHintFile)
	result := map[string]int64{
		"elapsed_ns": time.Since(started).Nanoseconds(), "work_elapsed_ns": workElapsed,
		"close_elapsed_ns": closeElapsed, "index_bytes_before_close": indexBefore, "index_bytes_after_close": indexAfter,
		"capacity": int64(capacity), "owner_fd_acquisitions": metadataOpens, "payload_fd_acquisitions": payloadOpens,
		"validated_population_entries": validated, "hint_rank_sum": rankSum,
		"cache_hits": hits, "cache_misses": misses,
	}
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
	var requests []map[string]int
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := t.TempDir()
			baseline := cacheCostProcess(t, "baseline", dir, count, true)
			populated := cacheCostProcess(t, "populate", dir, count, true)
			batch := cacheCostProcess(t, "populate-batch", t.TempDir(), count, true)
			chunks := (count + cachePublicationRows - 1) / cachePublicationRows
			if populated["pwrite64"]-batch["pwrite64"] != count-chunks {
				t.Errorf("rank header amortization: point=%v batch=%v want_delta=%d", populated, batch, count-chunks)
			}
			for _, result := range []map[string]int{populated, batch} {
				if result["validated_population_entries"] != count || result["hint_rank_sum"] != count*(count+1)/2 {
					t.Fatalf("population payload/hint semantics changed: %v", result)
				}
			}
			if batch["fsync"]+batch["fdatasync"] > populated["fsync"]+populated["fdatasync"] {
				t.Errorf("bulk publication added sync calls: point=%v batch=%v", populated, batch)
			}
			requested := cacheCostProcess(t, "requested", dir, count, true)
			requests = append(requests, requested)
			for _, name := range []string{"read", "pread64", "open", "openat", "fstat", "newfstatat"} {
				if requested[name] > 512 {
					t.Errorf("requested subset %s exceeded bound: %v", name, requested)
				}
			}
			if requested["getdents64"] > baseline["getdents64"] || requested["fsync"]+requested["fdatasync"] != 0 || requested["write"] > 8 || requested["pwrite64"] > 8 {
				t.Fatalf("requested subset enumerated or wrote cache metadata: %v", requested)
			}
			owned := cacheCostProcess(t, "owned", dir, count, true)
			perAccess := cacheCostProcess(t, "per-access", dir, count, true)
			if count == 10000 && owned["elapsed_ns"] >= perAccess["elapsed_ns"] {
				t.Fatalf("owned handle did not improve listing latency: owned=%v per-access=%v", owned, perAccess)
			}
			ownedOpens := owned["open"] + owned["openat"]
			perAccessOpens := perAccess["open"] + perAccess["openat"]
			if ownedOpens > count+64 || perAccessOpens-ownedOpens != count-1 || owned["owner_fd_acquisitions"] != 1 || perAccess["owner_fd_acquisitions"] != count || owned["payload_fd_acquisitions"] != count || perAccess["payload_fd_acquisitions"] != count {
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
	if len(requests) == 2 {
		for _, name := range []string{"read", "pread64", "open", "openat", "fstat", "newfstatat", "getdents64", "write", "pwrite64", "fsync", "fdatasync"} {
			if requests[1][name] > requests[0][name]+64 {
				t.Errorf("requested subset %s grew with cache population: %d to %d", name, requests[0][name], requests[1][name])
			}
		}
	}
}

func TestLRUDirMissingProcessCost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	var results []map[string]int
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := t.TempDir()
			baseline := cacheCostProcess(t, "baseline", dir, count, true)
			missing := cacheCostProcess(t, "missing", dir, count, true)
			results = append(results, missing)
			for _, name := range []string{"read", "pread64", "open", "openat", "fstat", "newfstatat"} {
				if missing[name] > baseline[name]+8 {
					t.Errorf("missing lookup repeated %s: baseline=%v missing=%v", name, baseline, missing)
				}
			}
			for _, name := range []string{"getdents64", "write", "pwrite64", "fsync", "fdatasync"} {
				if missing[name] > baseline[name] {
					t.Errorf("missing lookup enumerated or wrote %s: baseline=%v missing=%v", name, baseline, missing)
				}
			}
		})
	}
	for _, name := range []string{"read", "pread64", "open", "openat", "fstat", "newfstatat", "getdents64", "write", "pwrite64", "fsync", "fdatasync"} {
		if len(results) == 2 && results[1][name] > results[0][name]+64 {
			t.Errorf("missing lookup %s grew with requested misses: %d to %d", name, results[0][name], results[1][name])
		}
	}
}

func TestLRUDirDefaultCapacityHitRate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("syscall accounting requires Linux strace")
	}
	if _, err := exec.LookPath("strace"); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "cache")
			cacheCostProcess(t, "populate-default", dir, count, false)
			read := cacheCostProcess(t, "default-rate", dir, count, true)
			if read["capacity"] != foldCacheCap || read["cache_hits"]+read["cache_misses"] != count || read["payload_fd_acquisitions"] != read["cache_hits"] {
				t.Fatalf("default-capacity hit accounting = %v", read)
			}
			if count == 32 && read["cache_hits"] != count {
				t.Fatalf("small population lost entries: %v", read)
			}
			t.Logf("product_capacity=%d population=%d valid_payload_hits=%d misses=%d hit_rate=%.6f", foldCacheCap, count, read["cache_hits"], read["cache_misses"], float64(read["cache_hits"])/float64(count))
		})
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
	for _, name := range []string{"capacity", "owner_fd_acquisitions", "payload_fd_acquisitions", "validated_population_entries", "hint_rank_sum", "cache_hits", "cache_misses"} {
		counts[name] = int(result[name])
	}
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
	t.Logf("cache-cost mode=%s entries=%d capacity=%d elapsed_ns=%d work_ns=%d close_ns=%d index_before=%d index_after=%d syscalls=%v", mode, count, result["capacity"], result["elapsed_ns"], result["work_elapsed_ns"], result["close_elapsed_ns"], result["index_bytes_before_close"], result["index_bytes_after_close"], counts)
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
