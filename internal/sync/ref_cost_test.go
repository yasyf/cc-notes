package sync_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

type gitCost struct{ gitProcesses, packReads int }

func TestPrefixedPushReadsTheSameObjectsAtOneAndAThousandRefs(t *testing.T) {
	small, large := prefixedPushCost(t, 1), prefixedPushCost(t, 1000)
	t.Logf("prefixed push at 1 ref = %+v, at 1000 refs = %+v", small, large)
	if small.packReads == 0 {
		t.Fatal("prefixed push read no packed object, so the count measures nothing")
	}
	if small != large {
		t.Fatalf("prefixed push at 1 ref = %+v, at 1000 refs = %+v; want the same git processes and packed-object reads", small, large)
	}
}

func TestPrefixedFetchReadsTheSameObjectsAtOneAndAThousandRefs(t *testing.T) {
	small, large := prefixedFetchCost(t, 1), prefixedFetchCost(t, 1000)
	t.Logf("prefixed fetch at 1 ref = %+v, at 1000 refs = %+v", small, large)
	if small.packReads == 0 {
		t.Fatal("prefixed fetch read no packed object, so the count measures nothing")
	}
	if small != large {
		t.Fatalf("prefixed fetch at 1 ref = %+v, at 1000 refs = %+v; want the same git processes and packed-object reads", small, large)
	}
}

func prefixedPushCost(t *testing.T, others int) gitCost {
	t.Helper()
	bare, s := shallowCloneWithRefs(t, others)
	dir := s.RecordsGit.Dir
	ref := refs.For(model.KindNote, createNote(t, s, "pushed alone").ID)
	if _, err := ccsync.Prefixed(t.Context(), s, "origin", false, ref); err != nil {
		t.Fatalf("Prefixed(%s): %v", ref, err)
	}
	appendOps(t, s, ref, model.AddTag{Tag: "moved"})
	gittest.Git(t, dir, "repack", "-adq")

	cost := traceGit(t)
	report, err := ccsync.Prefixed(t.Context(), s, "origin", false, ref)
	if err != nil || report.Pushed != 1 {
		t.Fatalf("Prefixed(%s) at %d other refs = %+v, %v; want it pushed", ref, others, report, err)
	}
	if got, want := gittest.Git(t, bare, "rev-parse", ref), gittest.Git(t, dir, "rev-parse", ref); got != want {
		t.Fatalf("remote %s = %s, want %s", ref, got, want)
	}
	if got, want := gittest.Git(t, dir, "rev-parse", trackingOf(ref)), gittest.Git(t, dir, "rev-parse", ref); got != want {
		t.Fatalf("tracking ref of %s = %s, want the pushed %s", ref, got, want)
	}
	return cost()
}

func prefixedFetchCost(t *testing.T, others int) gitCost {
	t.Helper()
	bare, s := shallowCloneWithRefs(t, others)
	dir := s.RecordsGit.Dir
	ref := refs.For(model.KindNote, createNote(t, s, "moved elsewhere").ID)
	if _, err := ccsync.Prefixed(t.Context(), s, "origin", false, ref); err != nil {
		t.Fatalf("Prefixed(%s): %v", ref, err)
	}
	other := clone(t, bare, "Bob", "bob@example.com")
	if _, err := ccsync.Prefixed(t.Context(), other, "origin", false, ref); err != nil {
		t.Fatalf("Prefixed(%s) in the other clone: %v", ref, err)
	}
	appendOps(t, other, ref, model.AddTag{Tag: "moved"})
	if report, err := ccsync.Prefixed(t.Context(), other, "origin", false, ref); err != nil || report.Pushed != 1 {
		t.Fatalf("Prefixed(%s) in the other clone = %+v, %v; want the move pushed", ref, report, err)
	}
	gittest.Git(t, dir, "repack", "-adq")

	cost := traceGit(t)
	report, err := ccsync.Prefixed(t.Context(), s, "origin", false, ref)
	if err != nil || report.FastForwarded != 1 {
		t.Fatalf("Prefixed(%s) at %d other refs = %+v, %v; want it fast-forwarded", ref, others, report, err)
	}
	moved := gittest.Git(t, bare, "rev-parse", ref)
	if got := gittest.Git(t, dir, "rev-parse", ref); got != moved {
		t.Fatalf("local %s = %s, want the remote's %s", ref, got, moved)
	}
	if got := gittest.Git(t, dir, "rev-parse", trackingOf(ref)); got != moved {
		t.Fatalf("tracking ref of %s = %s, want the remote's %s", ref, got, moved)
	}
	return cost()
}

func shallowCloneWithRefs(t *testing.T, others int) (string, *store.Store) {
	t.Helper()
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	dir := s.RecordsGit.Dir
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "boundary")
	gittest.Shallow(t, dir, gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "config", "--add", "remote.origin.fetch", ccFetchRefspec)
	for key, command := range map[string]string{"receivepack": "receive-pack", "uploadpack": "upload-pack"} {
		gittest.Git(t, dir, "config", "remote.origin."+key, "env -u GIT_TRACE_PACK_ACCESS -u GIT_TRACE2_EVENT git "+command)
	}
	var stream strings.Builder
	for i := range others {
		fmt.Fprintf(&stream, "commit refs/cc-notes/notes/%040x\ncommitter A <a@example.com> 1700000000 +0000\ndata 1\nn\nM 644 inline ops.json\ndata %d\n%d\n\n", i, len(fmt.Sprint(i)), i)
	}
	importer := exec.Command("git", "-C", dir, "fast-import", "--quiet")
	importer.Stdin = strings.NewReader(stream.String())
	if out, err := importer.CombinedOutput(); err != nil {
		t.Fatalf("fast-import %d refs: %v: %s", others, err, out)
	}
	gittest.Git(t, dir, "push", "-q", "origin", "refs/cc-notes/*:refs/cc-notes/*")
	gittest.Git(t, dir, "fetch", "-q", "origin")
	return bare, s
}

func traceGit(t *testing.T) func() gitCost {
	t.Helper()
	traces := t.TempDir()
	packs, events := filepath.Join(traces, "packs"), filepath.Join(traces, "events")
	t.Setenv("GIT_TRACE_PACK_ACCESS", packs)
	t.Setenv("GIT_TRACE2_EVENT", events)
	return func() gitCost {
		return gitCost{gitProcesses: strings.Count(readTrace(t, events), `"event":"start"`), packReads: strings.Count(readTrace(t, packs), "\n")}
	}
}

func trackingOf(ref string) string {
	return "refs/cc-notes-sync/origin/" + strings.TrimPrefix(ref, refs.Namespace)
}

func readTrace(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
