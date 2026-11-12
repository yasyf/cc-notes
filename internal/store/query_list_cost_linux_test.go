package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type indexedListCost struct {
	Digest    string
	Count     int
	ReadBytes uint64
	Written   uint64
	ElapsedNS int64
	CPUNS     int64
}

func indexedListDigest(t *testing.T, answers []model.Answer) string {
	t.Helper()
	slices.SortFunc(answers, func(a, b model.Answer) int { return strings.Compare(string(a.ID), string(b.ID)) })
	data, err := json.Marshal(answers)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func indexedListIO(t *testing.T) map[string]uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]uint64)
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(fields[0], ":")] = value
	}
	return out
}

func indexedListCPU(t *testing.T) int64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return usage.Utime.Nano() + usage.Stime.Nano()
}

func TestIndexedListCostHelper(t *testing.T) {
	dir := os.Getenv("CCN_INDEXED_LIST_COST_REPO")
	if dir == "" {
		return
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	before := indexedListIO(t)
	cpu := indexedListCPU(t)
	start := time.Now()
	answers, hit, err := s.IndexedAnswers(t.Context(), []string{"target"}, ListOpts{})
	if err != nil || !hit {
		t.Fatalf("indexed answers: hit=%v err=%v", hit, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	elapsed, cpu := time.Since(start).Nanoseconds(), indexedListCPU(t)-cpu
	after := indexedListIO(t)
	result := indexedListCost{
		Digest: indexedListDigest(t, answers), Count: len(answers),
		ReadBytes: after["rchar"] - before["rchar"], Written: after["wchar"] - before["wchar"],
		ElapsedNS: elapsed, CPUNS: cpu,
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	homeguard.ChildExit(0)
}

func TestIndexedListFreshProcessWriteCost(t *testing.T) {
	s := initStore(t)
	var edited model.EntityID
	const count = 64
	body := strings.Repeat("unchanged answer body\n", 4096)
	for i := range count {
		edited = create(t, s, []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: fmt.Sprintf("answer %d", i), Body: body, Tags: []string{"target"}}}).EntityID()
	}
	prime := func() string {
		t.Helper()
		answers, err := s.ListAnswers(t.Context(), false, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, hit, err := s.StatusMetadata(t.Context(), model.KindAnswer); err != nil || !hit {
			t.Fatalf("prime index: hit=%v err=%v", hit, err)
		}
		return indexedListDigest(t, answers)
	}
	want := prime()
	read := func(phase string, maxWrites uint64) indexedListCost {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestIndexedListCostHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), "CCN_INDEXED_LIST_COST_REPO="+s.Git.Dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", phase, err, out)
		}
		var got indexedListCost
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%s: %v: %s", phase, err, out)
		}
		if got.Count != count || got.Digest != want {
			t.Fatalf("%s changed complete answer snapshots: %+v want %s", phase, got, want)
		}
		t.Logf("phase=%s full_snapshot=PASS selected=%d logical_read_bytes=%d logical_write_bytes=%d cpu_ns=%d elapsed_ns=%d", phase, got.Count, got.ReadBytes, got.Written, got.CPUNS, got.ElapsedNS)
		if got.Written > maxWrites {
			t.Errorf("INDEXED_LIST_WRITE_AMPLIFICATION: %s wrote %d bytes, want <=%d", phase, got.Written, maxWrites)
		}
		if got.ReadBytes > uint64(count*len(body)*2+(1<<20)) {
			t.Errorf("%s reread too much data: %+v", phase, got)
		}
		return got
	}
	read("first-selection", 64<<10)
	for i := range 3 {
		read(fmt.Sprintf("warm-%d", i), 0)
	}
	appendOps(t, s, refs.For(model.KindAnswer, edited), model.SetBody{Body: "edited answer"})
	want = prime()
	tip, err := s.Repo.Tip(t.Context(), refs.For(model.KindAnswer, edited))
	if err != nil {
		t.Fatal(err)
	}
	s.cache.delete(tip)
	read("changed-tip", 128<<10)
	read("warm-after-change", 0)
}
