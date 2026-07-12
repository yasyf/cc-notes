//go:build fuse

// Fidelity regressions for the ContentModeTree cutover: they drive cc-notes'
// REAL contentd (contentSource over a real store in a real git repo) through the
// REAL content.BridgeServer/BridgeClient — the exact wire the shared holder's
// tree view uses — with NO kernel mount. They prove the three load-bearing
// renderer semantics survive the bridge hop (commit-on-Flush with a Release
// backstop, per-version nanosecond cache defeat, fresh-after-external-change)
// and that the holder's version-keyed monotonic mtime bump preserves the cache
// defeat rather than clamping it away (the spike-3 clamp gap, now fixed).
package fusefs

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/fusekit"
	"github.com/yasyf/fusekit/content"
)

var contentdSockSeq atomic.Int64

// shortSock returns a unix socket path short enough for macOS's ~104-char
// sun_path limit (t.TempDir blows past it), under a per-run /tmp dir the test
// cleans up.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccd")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "c"+string(rune('a'+contentdSockSeq.Add(1)%26))+".sock")
}

// serveContentd stands up cc-notes' real content server over a real bridge and
// returns the bridge client the holder would use, the domain (repo root), and a
// store handle for seeding entities and reading ref tips out of band. The bridge
// server runs a fresh contentSource, so it opens its OWN store on the same repo —
// exactly the cross-process split of the shipped topology.
func serveContentd(t *testing.T) (*content.BridgeClient, string, *store.Store) {
	t.Helper()
	repo := gittest.InitRepo(t)
	s, err := store.Open(repo)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sock := shortSock(t)
	srv := &content.BridgeServer{Socket: sock, Source: newContentSource(), Version: "contentd-test"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("bridge Run returned before serving: %v", err)
		default:
		}
		if conn, err := net.Dial("unix", sock); err == nil {
			_ = conn.Close()
			return content.NewBridgeClient(sock), repo, s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("bridge socket never came up")
	return nil, "", nil
}

func notePath(n model.Note) string { return "/notes/" + Filename(n) }

func noteRef(n model.Note) string { return refs.For(model.KindNote, n.ID) }

func readAll(t *testing.T, h *content.Handle) []byte {
	t.Helper()
	b, err := h.ReadAt(context.Background(), 0, 1<<20)
	if err != nil {
		t.Fatalf("handle ReadAt: %v", err)
	}
	return b
}

// TestContentdCommitOnFlush: a write through an open handle buffers consumer-side
// and reaches the git store ONLY on Flush — over the real wire. Before the flush
// the ref tip is unchanged and a fresh open still snapshots the pre-edit bytes;
// after it the tip advances and a fresh open sees the edit.
func TestContentdCommitOnFlush(t *testing.T) {
	client, domain, s := serveContentd(t)
	ctx := context.Background()
	note := createNote(t, s, "Fidelity", "original body line")
	p := notePath(note)
	ref := noteRef(note)
	tip0 := mustTip(t, s, ref)

	h, err := client.OpenHandle(ctx, domain, p)
	if err != nil {
		t.Fatalf("OpenHandle: %v", err)
	}
	snap := readAll(t, h)
	edited := append(append([]byte(nil), snap...), []byte("\nappended fidelity line\n")...)
	if err := h.Truncate(ctx, 0); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if err := h.WriteAt(ctx, 0, edited); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}

	// The store MUST NOT have advanced: commit-on-flush, not commit-on-write.
	if tip := mustTip(t, s, ref); tip != tip0 {
		t.Fatalf("ref tip advanced before flush: %s -> %s", tip0, tip)
	}
	// A concurrent fresh open snapshots the committed (pre-edit) bytes.
	h2, err := client.OpenHandle(ctx, domain, p)
	if err != nil {
		t.Fatalf("OpenHandle h2: %v", err)
	}
	if got := string(readAll(t, h2)); got != string(snap) {
		t.Fatalf("uncommitted edit leaked to a fresh open before flush")
	}
	_ = h2.Release(ctx)

	if err := h.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if tip := mustTip(t, s, ref); tip == tip0 {
		t.Fatalf("ref tip did not advance after flush (edit not committed)")
	}
	h3, err := client.OpenHandle(ctx, domain, p)
	if err != nil {
		t.Fatalf("OpenHandle h3: %v", err)
	}
	if got := string(readAll(t, h3)); !strings.Contains(got, "appended fidelity line") {
		t.Fatalf("committed edit not visible on a fresh open: %q", got)
	}
	_ = h3.Release(ctx)
	_ = h.Release(ctx)
}

// TestContentdReleaseBackstop: a dirty handle never Flushed still commits on
// Release (the FS's dirty-and-unflushed backstop), over the wire.
func TestContentdReleaseBackstop(t *testing.T) {
	client, domain, s := serveContentd(t)
	ctx := context.Background()
	note := createNote(t, s, "Backstop", "backstop body")
	p := notePath(note)
	ref := noteRef(note)
	tip0 := mustTip(t, s, ref)

	h, err := client.OpenHandle(ctx, domain, p)
	if err != nil {
		t.Fatalf("OpenHandle: %v", err)
	}
	snap := readAll(t, h)
	edited := append(append([]byte(nil), snap...), []byte("\nbackstop appended line\n")...)
	if err := h.Truncate(ctx, 0); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if err := h.WriteAt(ctx, 0, edited); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if tip := mustTip(t, s, ref); tip != tip0 {
		t.Fatalf("ref tip advanced before release: %s -> %s", tip0, tip)
	}
	if err := h.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if tip := mustTip(t, s, ref); tip == tip0 {
		t.Fatalf("release backstop did not commit the dirty buffer")
	}
}

// TestContentdVersionCacheDefeat: the consumer's Entry.Version is the chain-tip
// SHA and Entry.Mtime folds VersionNsec(version) into the second, byte-exact
// over the wire; an external same-entity commit mints a new tip, so the wire
// Version and Mtime both change — the freshness signal the holder keys on.
func TestContentdVersionCacheDefeat(t *testing.T) {
	client, domain, s := serveContentd(t)
	ctx := context.Background()
	note := createNote(t, s, "Cache defeat", "v0 body")
	p := notePath(note)
	ref := noteRef(note)

	e0, err := client.Stat(ctx, domain, p)
	if err != nil {
		t.Fatalf("Stat v0: %v", err)
	}
	if e0.Version != string(mustTip(t, s, ref)) {
		t.Fatalf("Entry.Version = %q, want the chain-tip SHA %q", e0.Version, mustTip(t, s, ref))
	}
	if e0.Mtime%1_000_000_000 != fusekit.VersionNsec(e0.Version) {
		t.Fatalf("Mtime nsec = %d, want VersionNsec(version) = %d (cache-defeat scheme not on the wire)", e0.Mtime%1_000_000_000, fusekit.VersionNsec(e0.Version))
	}

	// External CLI commit on the same entity → new tip.
	if _, err := s.Append(ctx, ref, []model.Op{model.SetBody{Body: "v1 body extended"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	e1, err := client.Stat(ctx, domain, p)
	if err != nil {
		t.Fatalf("Stat v1: %v", err)
	}
	if e1.Version == e0.Version {
		t.Fatalf("Entry.Version unchanged across an external commit: %q", e1.Version)
	}
	if e1.Version != string(mustTip(t, s, ref)) {
		t.Fatalf("Entry.Version = %q, want the new chain-tip SHA %q", e1.Version, mustTip(t, s, ref))
	}
	if e1.Mtime%1_000_000_000 != fusekit.VersionNsec(e1.Version) {
		t.Fatalf("Mtime nsec v1 = %d, want VersionNsec(version) = %d", e1.Mtime%1_000_000_000, fusekit.VersionNsec(e1.Version))
	}
	if e1.Mtime == e0.Mtime {
		t.Fatalf("wire mtime unchanged across a version change (%d) — cache defeat impossible", e0.Mtime)
	}
}

// TestContentdFreshAfterExternalChange: an already-open handle keeps its
// open-time snapshot across an external commit, while a fresh open sees the new
// content — over the wire.
func TestContentdFreshAfterExternalChange(t *testing.T) {
	client, domain, s := serveContentd(t)
	ctx := context.Background()
	note := createNote(t, s, "Fresh", "v1 marker body")
	p := notePath(note)
	ref := noteRef(note)

	h1, err := client.OpenHandle(ctx, domain, p)
	if err != nil {
		t.Fatalf("OpenHandle h1: %v", err)
	}
	snap1 := string(readAll(t, h1))
	if !strings.Contains(snap1, "v1 marker body") {
		t.Fatalf("h1 snapshot missing the v1 body: %q", snap1)
	}

	if _, err := s.Append(ctx, ref, []model.Op{model.SetBody{Body: "v2 replacement body marker"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// h1 keeps its open-time snapshot, immune to the concurrent commit.
	if got := string(readAll(t, h1)); got != snap1 {
		t.Fatalf("h1 snapshot tore across an external commit")
	}
	// A fresh open sees the new content.
	h2, err := client.OpenHandle(ctx, domain, p)
	if err != nil {
		t.Fatalf("OpenHandle h2: %v", err)
	}
	if got := string(readAll(t, h2)); !strings.Contains(got, "v2 replacement body marker") {
		t.Fatalf("fresh open did not see the external change: %q", got)
	}
	_ = h1.Release(ctx)
	_ = h2.Release(ctx)
}

// --- the holder's version-keyed monotonic bump, reproduced faithfully ---
//
// The next two tests — TestContentdVersionChangeDefeatsClamp and
// TestContentdWriteBumpNeverSwallowsCanonical — are SEMANTIC MIRRORS of
// fusekit's own authoritative pinned tests for treeNode.bumpVersionLocked (in
// holderfs/treeview_test.go). That method is unexported and its file is
// //go:build fuse&&cgo&&darwin (kernel mount), so it cannot be imported here;
// the ~8 decisive lines are copied verbatim below and fed REAL wire-delivered
// Entry.Version/Entry.Mtime values (with real VersionNsec). These mirrors prove
// the cc-notes cache-defeat scheme survives that bump end-to-end — they are NOT
// the drift guard. fusekit's own gate on the real bumpVersionLocked is; if the
// two ever diverge, fusekit's tests fail there, not here.

type treeNode struct{ mtimeHWM time.Time }

func (n *treeNode) bumpVersionLocked(prevVersion string, hadStat bool, e content.Entry) {
	if mt := time.Unix(0, e.Mtime); e.Mtime != 0 && mt.After(n.mtimeHWM) {
		n.mtimeHWM = mt
		return
	}
	if hadStat && e.Version != prevVersion {
		n.mtimeHWM = n.mtimeHWM.Add(time.Nanosecond)
	}
}

// TestContentdVersionChangeDefeatsClamp: a same-second version change whose
// VersionNsec is LOWER than the served high-water mark must still advance the
// served mtime — the spike-3 clamp gap, now closed by version keying. The two
// Version strings and their nsecs come from the REAL bridge.
func TestContentdVersionChangeDefeatsClamp(t *testing.T) {
	client, domain, s := serveContentd(t)
	ctx := context.Background()

	// Collect several real versioned entries and pick a high/low VersionNsec pair.
	var hi, lo content.Entry
	haveHi, haveLo := false, false
	for i := 0; i < 12; i++ {
		note := createNote(t, s, "Clamp", "distinct body "+string(rune('a'+i)))
		e, err := client.Stat(ctx, domain, notePath(note))
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		nsec := e.Mtime % 1_000_000_000
		if !haveHi || nsec > hi.Mtime%1_000_000_000 {
			hi, haveHi = e, true
		}
		if !haveLo || nsec < lo.Mtime%1_000_000_000 {
			lo, haveLo = e, true
		}
	}
	if hi.Version == lo.Version || hi.Mtime%1_000_000_000 == lo.Mtime%1_000_000_000 {
		t.Fatal("could not find a distinct high/low VersionNsec pair (unexpected)")
	}

	// Normalize both to the SAME second: the node serves hi, then a version change
	// to lo whose reported mtime is strictly lower (the case the old clamp swallowed).
	const sec = int64(1_700_000_000)
	hiE := content.Entry{Version: hi.Version, Mtime: sec*1_000_000_000 + hi.Mtime%1_000_000_000}
	loE := content.Entry{Version: lo.Version, Mtime: sec*1_000_000_000 + lo.Mtime%1_000_000_000}
	if loE.Mtime >= hiE.Mtime {
		t.Fatalf("expected loE < hiE; got hi=%d lo=%d", hiE.Mtime, loE.Mtime)
	}

	n := &treeNode{mtimeHWM: time.Unix(0, hiE.Mtime)}
	n.bumpVersionLocked(hiE.Version, true, loE)
	served := n.mtimeHWM.UnixNano()
	if served <= hiE.Mtime {
		t.Fatalf("same-second lower-nsec version change did not advance the served mtime (%d -> %d) — the clamp gap is back", hiE.Mtime, served)
	}
	if served != hiE.Mtime+1 {
		t.Fatalf("served mtime = %d, want a +1ns bump above %d", served, hiE.Mtime)
	}
}

// TestContentdWriteBumpNeverSwallowsCanonical: after a write-through the holder
// bumps the served mtime to a wall-clock instant anywhere in the save second;
// the post-close canonical render (a NEW version, same second) must never be
// suppressed by losing a coin flip to that wall clock. Version keying makes the
// suppression rate exactly 0. Modeled exactly as spike-3, with real chain-tip
// SHAs.
func TestContentdWriteBumpNeverSwallowsCanonical(t *testing.T) {
	client, domain, s := serveContentd(t)
	ctx := context.Background()
	note := createNote(t, s, "Write bump", "pre-save body")
	ref := noteRef(note)
	pre, err := client.Stat(ctx, domain, notePath(note))
	if err != nil {
		t.Fatalf("Stat pre: %v", err)
	}
	if _, err := s.Append(ctx, ref, []model.Op{model.SetBody{Body: "post-save canonical body"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	post, err := client.Stat(ctx, domain, notePath(note))
	if err != nil {
		t.Fatalf("Stat post: %v", err)
	}
	if pre.Version == post.Version {
		t.Fatal("pre/post versions equal; the append did not mint a new tip")
	}

	nowSec := time.Now().Unix()
	rng := rand.New(rand.NewSource(1))
	const n = 4096
	suppressed := 0
	for i := 0; i < n; i++ {
		writeBump := time.Unix(nowSec, rng.Int63n(1_000_000_000))
		canonical := content.Entry{Version: post.Version, Mtime: nowSec*1_000_000_000 + post.Mtime%1_000_000_000}
		node := &treeNode{mtimeHWM: writeBump}
		node.bumpVersionLocked(pre.Version, true, canonical)
		served := node.mtimeHWM.UnixNano()
		if served <= writeBump.UnixNano() && served != canonical.Mtime {
			suppressed++
		}
		if served < writeBump.UnixNano() {
			t.Fatalf("iteration %d: served mtime regressed below the write bump", i)
		}
	}
	if suppressed != 0 {
		t.Fatalf("canonical render suppressed on %d/%d saves — version keying should make it 0", suppressed, n)
	}
}

// --- direct contentSource drives (no bridge): the source IS the unit here ---

// directSource returns a fresh contentSource, the repo root (domain), and a
// second store handle for seeding entities and reading tips out of band — the
// source opens its OWN cold store at the domain, exactly the cross-process split
// contentd runs in.
func directSource(t *testing.T) (*contentSource, string, *store.Store) {
	t.Helper()
	repo := gittest.InitRepo(t)
	s, err := store.Open(repo)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return newContentSource(), repo, s
}

// classOf extracts a bridge error class off a treeErr (direct calls return it
// un-roundtripped).
func classOf(err error) string {
	var c interface{ Class() string }
	if errors.As(err, &c) {
		return c.Class()
	}
	return ""
}

// TestContentdUnknownTokenIsLoudIOError pins that a stale handle token — the
// signature of a contentd restart that lost its in-memory table — surfaces a
// LOUD transient IO error on every handle op, NEVER ClassNotFound. ENOENT would
// make an editor's save read as file-not-found on a file that exists.
func TestContentdUnknownTokenIsLoudIOError(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Restart", "body")
	p := notePath(note)

	const stale = "999"
	for _, tc := range []struct {
		op   string
		call func() error
	}{
		{"Flush", func() error { return cs.FlushHandle(domain, p, stale) }},
		{"Write", func() error { return cs.WriteAtHandle(domain, p, stale, 0, []byte("x")) }},
		{"Release", func() error { return cs.ReleaseHandle(domain, p, stale) }},
	} {
		err := tc.call()
		if err == nil {
			t.Errorf("%sHandle with a stale token succeeded, want a loud error", tc.op)
			continue
		}
		if cls := classOf(err); cls != content.ClassTransient {
			t.Errorf("%sHandle stale-token class = %q, want %q (never ClassNotFound → ENOENT)", tc.op, cls, content.ClassTransient)
		}
	}
}

// TestContentdReleaseAllDiscardsDirty pins that a holder-generation-change
// release (ReleaseAllHandles) DISCARDS a dirty buffer rather than committing it:
// a mid-rewrite generation change would otherwise make a torn partial write
// canonical. The edit here is a VALID commit-able one, so the old
// commit-backstop path WOULD have committed it.
func TestContentdReleaseAllDiscardsDirty(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Discard", "original body")
	p := notePath(note)
	ref := noteRef(note)
	tip0 := mustTip(t, s, ref)

	tok, _, err := cs.OpenHandle(domain, p)
	if err != nil {
		t.Fatalf("OpenHandle: %v", err)
	}
	snap, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
	if err != nil {
		t.Fatalf("ReadAtHandle: %v", err)
	}
	edited := append(append([]byte(nil), snap...), []byte("\ndiscarded valid line\n")...)
	if err := cs.TruncateHandle(domain, p, tok, 0); err != nil {
		t.Fatalf("TruncateHandle: %v", err)
	}
	if err := cs.WriteAtHandle(domain, p, tok, 0, edited); err != nil {
		t.Fatalf("WriteAtHandle: %v", err)
	}

	if err := cs.ReleaseAllHandles(domain); err != nil {
		t.Fatalf("ReleaseAllHandles: %v", err)
	}
	if tip := mustTip(t, s, ref); tip != tip0 {
		t.Fatalf("ReleaseAllHandles committed a dirty buffer (tip %s -> %s); it must discard", tip0, tip)
	}
	fresh, err := cs.ReadSynth(domain, p)
	if err != nil {
		t.Fatalf("ReadSynth: %v", err)
	}
	if strings.Contains(string(fresh), "discarded valid line") {
		t.Fatalf("discarded edit leaked into the committed content: %q", fresh)
	}
}

// TestContentdReleaseTransientFailureKeepsBuffer pins that a transient store
// failure on an unflushed Release surfaces a LOUD error AND keeps the handle and
// its buffer (the only copy of the edit) — never a silent success that discards
// it. A retry after the store recovers commits the retained buffer.
func TestContentdReleaseTransientFailureKeepsBuffer(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Transient", "original body")
	p := notePath(note)
	ref := noteRef(note)
	tip0 := mustTip(t, s, ref)

	tok, _, err := cs.OpenHandle(domain, p)
	if err != nil {
		t.Fatalf("OpenHandle: %v", err)
	}
	snap, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
	if err != nil {
		t.Fatalf("ReadAtHandle: %v", err)
	}
	edited := append(append([]byte(nil), snap...), []byte("\nunflushed edit line\n")...)
	if err := cs.TruncateHandle(domain, p, tok, 0); err != nil {
		t.Fatalf("TruncateHandle: %v", err)
	}
	if err := cs.WriteAtHandle(domain, p, tok, 0, edited); err != nil {
		t.Fatalf("WriteAtHandle: %v", err)
	}

	// Make the store fail: an unwritable objects dir fails the git write with a
	// non-parse, non-refnotfound error → EIO (the transient class).
	objs := filepath.Join(domain, ".git", "objects")
	if err := os.Chmod(objs, 0o000); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			_ = os.Chmod(objs, 0o700)
		}
	}()

	relErr := cs.ReleaseHandle(domain, p, tok)
	if relErr == nil {
		t.Fatal("ReleaseHandle succeeded despite a store failure on the only copy of an unflushed edit")
	}
	if cls := classOf(relErr); cls != content.ClassTransient {
		t.Fatalf("failed-release class = %q, want %q (EIO)", cls, content.ClassTransient)
	}
	if tip := mustTip(t, s, ref); tip != tip0 {
		t.Fatalf("tip advanced despite the store failure: %s -> %s", tip0, tip)
	}

	// Restore the store; the RETAINED buffer commits on retry with the same token.
	if err := os.Chmod(objs, 0o700); err != nil {
		t.Fatal(err)
	}
	restored = true
	if err := cs.ReleaseHandle(domain, p, tok); err != nil {
		t.Fatalf("retry ReleaseHandle: %v (the buffer was not retained)", err)
	}
	if tip := mustTip(t, s, ref); tip == tip0 {
		t.Fatal("retry did not commit the retained buffer — the edit was lost")
	}
	fresh, err := cs.ReadSynth(domain, p)
	if err != nil {
		t.Fatalf("ReadSynth: %v", err)
	}
	if !strings.Contains(string(fresh), "unflushed edit line") {
		t.Fatalf("retry commit missing the edit: %q", fresh)
	}
}

// TestContentdListLoudOnStoreError pins that List fails LOUDLY on a non-ENOENT
// child-stat failure instead of silently skipping — a transient store EIO must
// not delete a live note from the holder's tree. The first note's render is
// warmed so its stat survives the unreadable store; the second (cold) note's
// stat hits the store and fails, and List must surface that.
func TestContentdListLoudOnStoreError(t *testing.T) {
	cs, domain, s := directSource(t)
	createNote(t, s, "Warm", "warm body")
	// Warm the source's render cache for the first note (so its later stat needs
	// no object read), and prove List works before the fault.
	if entries, err := cs.List(domain, "/notes"); err != nil || len(entries) != 1 {
		t.Fatalf("baseline List = (%d entries, %v), want 1 entry and no error", len(entries), err)
	}
	// A second note the source has never rendered — its stat is cold.
	createNote(t, s, "Cold", "cold body")

	objs := filepath.Join(domain, ".git", "objects")
	if err := os.Chmod(objs, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(objs, 0o700) }()

	_, err := cs.List(domain, "/notes")
	if err == nil {
		t.Fatal("List succeeded with an unreadable store, want a loud error (a transient stat failure must not silently drop a live note)")
	}
	if cls := classOf(err); cls != content.ClassTransient {
		t.Errorf("List store-error class = %q, want %q (never a silent skip)", cls, content.ClassTransient)
	}
}

// TestContentdConcurrentWriteNotLostAcrossFlush pins the dirty-generation guard:
// a WriteAtHandle that lands during a FlushHandle's unlocked store append must
// NOT be silently cleared away. Each iteration first writes a real change (base),
// so the flush actually appends to the store (a real, long unlock window), then
// races a marker write against that flush and Releases WITHOUT a final flush — so
// a marker cleared away by the pre-fix unconditional dirty-clear is lost forever.
// With the generation guard every marker survives.
func TestContentdConcurrentWriteNotLostAcrossFlush(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Race", "seed body")
	p := notePath(note)

	const n = 80
	markers := make([]string, n)
	for i := range markers {
		markers[i] = fmt.Sprintf("<m%03d>", i)
	}

	for i := 0; i < n; i++ {
		tok, _, err := cs.OpenHandle(domain, p)
		if err != nil {
			t.Fatalf("iter %d OpenHandle: %v", i, err)
		}
		cur, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
		if err != nil {
			t.Fatalf("iter %d ReadAtHandle: %v", i, err)
		}
		// A real committed change so the racing flush does store I/O (a genuine
		// unlock window), not a zero-op no-op.
		base := []byte(fmt.Sprintf("\nbase%03d\n", i))
		if err := cs.WriteAtHandle(domain, p, tok, int64(len(cur)), base); err != nil {
			t.Fatalf("iter %d base write: %v", i, err)
		}
		markOff := int64(len(cur) + len(base))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = cs.FlushHandle(domain, p, tok)
		}()
		go func() {
			defer wg.Done()
			_ = cs.WriteAtHandle(domain, p, tok, markOff, []byte(markers[i]+"\n"))
		}()
		wg.Wait()
		if err := cs.ReleaseHandle(domain, p, tok); err != nil {
			t.Fatalf("iter %d ReleaseHandle: %v", i, err)
		}
	}

	final, err := cs.ReadSynth(domain, p)
	if err != nil {
		t.Fatalf("final ReadSynth: %v", err)
	}
	var missing []string
	for _, m := range markers {
		if !strings.Contains(string(final), m) {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d/%d marker writes lost to the write-during-flush race: %v", len(missing), n, missing)
	}
}

// TestContentdStatSizeVersionAtomic pins that Stat pairs size and version from
// ONE snapshot: a committer oscillates the body length while a reader stats in a
// loop, and every observed (Version, Size) must match ground truth built from
// the same renderer the FS uses. Reading size and version across two separate
// lock acquisitions would pair a stale size with the new version SHA.
func TestContentdStatSizeVersionAtomic(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Atomic", "seed")
	p := notePath(note)
	ref := noteRef(note)

	var mu sync.Mutex
	sizeOf := map[string]int64{}
	record := func(snap model.Snapshot) {
		mu.Lock()
		sizeOf[string(snap.Meta().Head)] = int64(len(renderDocument(snap)))
		mu.Unlock()
	}
	record(note)

	stop := make(chan struct{})
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		bodies := []string{"x", strings.Repeat("much longer body ", 8)}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			snap, err := s.Append(context.Background(), ref, []model.Op{model.SetBody{Body: bodies[i%2] + fmt.Sprint(i)}})
			if err != nil {
				select {
				case errc <- err:
				default:
				}
				return
			}
			record(snap)
		}
	}()

	checks := 0
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		e, err := cs.Stat(domain, p)
		if err != nil {
			continue // a stat racing a mid-write may be transient; only consistency matters
		}
		mu.Lock()
		want, ok := sizeOf[e.Version]
		mu.Unlock()
		if ok && want != e.Size {
			close(stop)
			wg.Wait()
			t.Fatalf("Stat paired version %s (true size %d) with size %d — non-atomic size/version snapshot", e.Version, want, e.Size)
		}
		checks++
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errc:
		t.Fatalf("committer append failed: %v", err)
	default:
	}
	if checks == 0 {
		t.Fatal("no successful stats observed")
	}
}

// TestContentdConcurrentWriteNotLostAcrossRelease pins O3: a WriteAtHandle that
// lands during the ReleaseHandle backstop-commit's unlocked store append must NOT
// be dropped. Each iteration writes a base change then races a marker write
// against ReleaseHandle (no FlushHandle first, so Release itself commits). A write
// that SUCCEEDS hit a still-live handle inside the commit window, so the drain
// loop must re-commit it; a write that raced after close (error) is legitimately
// lost. Every accepted marker survives.
func TestContentdConcurrentWriteNotLostAcrossRelease(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "ReleaseRace", "seed body")
	p := notePath(note)

	const n = 80
	markers := make([]string, n)
	for i := range markers {
		markers[i] = fmt.Sprintf("<r%03d>", i)
	}
	accepted := make([]bool, n)

	for i := 0; i < n; i++ {
		tok, _, err := cs.OpenHandle(domain, p)
		if err != nil {
			t.Fatalf("iter %d OpenHandle: %v", i, err)
		}
		cur, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
		if err != nil {
			t.Fatalf("iter %d ReadAtHandle: %v", i, err)
		}
		// A real committed change so the racing Release does store I/O (a genuine
		// unlock window), not a zero-op no-op.
		base := []byte(fmt.Sprintf("\nbase%03d\n", i))
		if err := cs.WriteAtHandle(domain, p, tok, int64(len(cur)), base); err != nil {
			t.Fatalf("iter %d base write: %v", i, err)
		}
		markOff := int64(len(cur) + len(base))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = cs.ReleaseHandle(domain, p, tok)
		}()
		var writeErr error
		go func() {
			defer wg.Done()
			writeErr = cs.WriteAtHandle(domain, p, tok, markOff, []byte(markers[i]+"\n"))
		}()
		wg.Wait()
		accepted[i] = writeErr == nil
	}

	final, err := cs.ReadSynth(domain, p)
	if err != nil {
		t.Fatalf("final ReadSynth: %v", err)
	}
	var missing []string
	acceptedCount := 0
	for i, m := range markers {
		if !accepted[i] {
			continue // raced after close — legitimately lost, not the O3 bug
		}
		acceptedCount++
		if !strings.Contains(string(final), m) {
			missing = append(missing, m)
		}
	}
	if acceptedCount == 0 {
		t.Fatal("no marker write ever landed during Release's commit window; the race was not exercised")
	}
	if len(missing) > 0 {
		t.Fatalf("%d accepted marker writes lost to the write-during-Release race: %v", len(missing), missing)
	}
}

// TestContentdReleaseParseFailureSurfacesErrno pins O4: a NON-EIO deterministic
// backstop failure (a parse rejection) on an unflushed Release surfaces its errno
// (ClassInvalid) instead of silently reporting success and dropping the dirty
// document, and KEEPS the handle+token so a re-close resolves the same fh.
func TestContentdReleaseParseFailureSurfacesErrno(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Parse", "original body")
	p := notePath(note)
	ref := noteRef(note)
	tip0 := mustTip(t, s, ref)

	tok, _, err := cs.OpenHandle(domain, p)
	if err != nil {
		t.Fatalf("OpenHandle: %v", err)
	}
	// Overwrite the whole document with content the note codec cannot parse (no
	// frontmatter), and DON'T flush — so the Release backstop is what commits and
	// rejects it. The non-EIO rejection must not be swallowed.
	if err := cs.TruncateHandle(domain, p, tok, 0); err != nil {
		t.Fatalf("TruncateHandle: %v", err)
	}
	if err := cs.WriteAtHandle(domain, p, tok, 0, []byte("# Just markdown\n\nbody with no frontmatter\n")); err != nil {
		t.Fatalf("WriteAtHandle: %v", err)
	}

	relErr := cs.ReleaseHandle(domain, p, tok)
	if relErr == nil {
		t.Fatal("ReleaseHandle reported success on a parse-rejected unflushed edit — the dirty document was silently dropped (O4)")
	}
	if cls := classOf(relErr); cls != content.ClassInvalid {
		t.Fatalf("release parse-failure class = %q, want %q (surface the real errno, never EIO-only)", cls, content.ClassInvalid)
	}
	if tip := mustTip(t, s, ref); tip != tip0 {
		t.Fatalf("tip advanced on a rejected save: %s -> %s", tip0, tip)
	}
	// The handle+token were KEPT (not dropped on a non-EIO failure): the token
	// still resolves, and the buffer reverted to the last good render so the
	// broken bytes never shadow the entity for path readers.
	got, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
	if err != nil {
		t.Fatalf("token dropped after a non-EIO backstop failure (want kept for retry): %v", err)
	}
	if strings.Contains(string(got), "no frontmatter") {
		t.Errorf("rejected garbage lingered in the kept buffer for path readers: %q", got)
	}
}

// TestContentdFlushRacesReleaseAll pins O5's commit gate: a FlushHandle racing a
// ReleaseAllHandles (holder generation change) yields one of two clean outcomes —
// the commit drained before the discard (full edit committed) or it was refused
// before its append (edit discarded) — never a corrupt or partial state. The gate
// (commit RLock vs releaseAll write Lock) serializes them.
func TestContentdFlushRacesReleaseAll(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "GenRace", "seed body\n")
	p := notePath(note)
	ref := noteRef(note)

	const n = 60
	for i := 0; i < n; i++ {
		tip0 := mustTip(t, s, ref)
		orig, err := cs.ReadSynth(domain, p)
		if err != nil {
			t.Fatalf("iter %d ReadSynth: %v", i, err)
		}

		tok, _, err := cs.OpenHandle(domain, p)
		if err != nil {
			t.Fatalf("iter %d OpenHandle: %v", i, err)
		}
		cur, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
		if err != nil {
			t.Fatalf("iter %d ReadAtHandle: %v", i, err)
		}
		edit := fmt.Sprintf("edit-%03d-line\n", i)
		if err := cs.WriteAtHandle(domain, p, tok, int64(len(cur)), []byte(edit)); err != nil {
			t.Fatalf("iter %d write: %v", i, err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = cs.FlushHandle(domain, p, tok)
		}()
		go func() {
			defer wg.Done()
			_ = cs.ReleaseAllHandles(domain)
		}()
		wg.Wait()
		_ = cs.ReleaseHandle(domain, p, tok) // clean up any surviving handle

		final, err := cs.ReadSynth(domain, p)
		if err != nil {
			t.Fatalf("iter %d final ReadSynth: %v", i, err)
		}
		committed := mustTip(t, s, ref) != tip0
		switch {
		case committed && !strings.Contains(string(final), edit):
			t.Fatalf("iter %d: tip advanced but the full edit line is absent — a partial commit: %q", i, final)
		case committed && !strings.Contains(string(final), "seed body"):
			t.Fatalf("iter %d: commit dropped the original body: %q", i, final)
		case !committed && string(final) != string(orig):
			t.Fatalf("iter %d: no commit but content changed from %q to %q", i, orig, final)
		}
	}
}

// TestContentdSustainedWriterDrainsUntilRelease pins Q1's drain-until-stable: a
// writer HAMMERS WriteAtHandle in a tight loop while a single ReleaseHandle races
// it, so the backstop must re-commit until the buffer stops changing (no fixed
// bound). Every write that RETURNED nil landed in a live handle inside a commit
// window and MUST survive in the committed content; a write that failed raced past
// close and is legitimately lost. A bounded drain would drop the tail of a
// sustained racing writer and leave the handle dirty (EIO).
func TestContentdSustainedWriterDrainsUntilRelease(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Sustained", "seed body\n")
	p := notePath(note)
	ref := noteRef(note)
	tip0 := mustTip(t, s, ref)

	tok, _, err := cs.OpenHandle(domain, p)
	if err != nil {
		t.Fatalf("OpenHandle: %v", err)
	}
	cur, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
	if err != nil {
		t.Fatalf("ReadAtHandle: %v", err)
	}

	const maxWrites = 1500
	var accepted atomic.Int64
	off := int64(len(cur))
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < maxWrites; i++ {
			m := fmt.Sprintf("<s%04d>\n", i)
			if err := cs.WriteAtHandle(domain, p, tok, off, []byte(m)); err != nil {
				return // Release won: the handle is gone, this and later writes are lost
			}
			off += int64(len(m))
			accepted.Add(1)
		}
	}()

	// Let a batch of writes land, then race Release against the still-hammering writer.
	for accepted.Load() < 5 {
		time.Sleep(time.Millisecond)
	}
	relErr := cs.ReleaseHandle(domain, p, tok)
	<-writerDone

	if relErr != nil {
		t.Fatalf("ReleaseHandle drain returned %v, want a clean commit of every accepted write", relErr)
	}
	n := int(accepted.Load())
	if n == 0 {
		t.Fatal("no writes were accepted; the sustained race never engaged")
	}
	final, err := cs.ReadSynth(domain, p)
	if err != nil {
		t.Fatalf("final ReadSynth: %v", err)
	}
	var missing []string
	for i := 0; i < n; i++ {
		if m := fmt.Sprintf("<s%04d>", i); !strings.Contains(string(final), m) {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d/%d accepted writes lost despite drain-until-stable: %v", len(missing), n, missing)
	}
	if mustTip(t, s, ref) == tip0 {
		t.Fatal("tip never advanced — nothing was committed")
	}
}

// TestContentdOpenAgainstClosingGenerationRefused pins Q2's generation-closed
// check: an OpenHandle that snapshots the generation, then finds it advanced by a
// releaseAll before it takes the commit gate straddled a teardown and MUST refuse
// transient — never mint a handle for the closing generation that a later gated
// Flush could commit past the discard.
func TestContentdOpenAgainstClosingGenerationRefused(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "Closing", "seed body")
	p := notePath(note)
	rv, err := cs.viewFor(domain)
	if err != nil {
		t.Fatalf("viewFor: %v", err)
	}

	// Stand in for a releaseAll in progress: hold the gate's write side so a
	// concurrent OpenHandle snapshots the generation and blocks on the read side.
	rv.commitGate.Lock()
	type res struct {
		tok string
		err error
	}
	done := make(chan res, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		tok, _, err := cs.OpenHandle(domain, p)
		done <- res{tok, err}
	}()
	<-started
	time.Sleep(50 * time.Millisecond) // let OpenHandle snapshot gen and block on RLock
	rv.gen.Add(1)                     // the teardown advances the generation
	rv.commitGate.Unlock()

	r := <-done
	if r.err == nil {
		_ = cs.ReleaseHandle(domain, p, r.tok)
		t.Fatal("OpenHandle straddling a generation teardown succeeded, want a transient refusal")
	}
	if cls := classOf(r.err); cls != content.ClassTransient {
		t.Fatalf("straddled-open class = %q, want %q (never mint a handle for the closing generation)", cls, content.ClassTransient)
	}

	// A non-straddled open (no concurrent teardown) still succeeds — the check
	// never over-refuses a clean open.
	tok, _, err := cs.OpenHandle(domain, p)
	if err != nil {
		t.Fatalf("clean OpenHandle refused after the generation settled: %v", err)
	}
	_ = cs.ReleaseHandle(domain, p, tok)
}

// TestContentdOpenRacesReleaseAll stress-races OpenHandle+write+Flush against
// ReleaseAllHandles: the commit-gate open path must never tear content, never
// leave a partial commit, and never leak an FS handle (the O5 symptom of a handle
// minted outside the gate).
func TestContentdOpenRacesReleaseAll(t *testing.T) {
	cs, domain, s := directSource(t)
	note := createNote(t, s, "OpenGenRace", "seed body\n")
	p := notePath(note)
	ref := noteRef(note)

	const n = 200
	for i := 0; i < n; i++ {
		tip0 := mustTip(t, s, ref)
		orig, err := cs.ReadSynth(domain, p)
		if err != nil {
			t.Fatalf("iter %d ReadSynth: %v", i, err)
		}
		edit := fmt.Sprintf("edit-%03d-line\n", i)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			tok, _, err := cs.OpenHandle(domain, p)
			if err != nil {
				return // straddled the teardown (transient) — a clean outcome
			}
			cur, err := cs.ReadAtHandle(domain, p, tok, 0, 1<<20)
			if err != nil {
				_ = cs.ReleaseHandle(domain, p, tok)
				return
			}
			if e := cs.WriteAtHandle(domain, p, tok, int64(len(cur)), []byte(edit)); e != nil {
				_ = cs.ReleaseHandle(domain, p, tok)
				return
			}
			_ = cs.FlushHandle(domain, p, tok)
			_ = cs.ReleaseHandle(domain, p, tok)
		}()
		go func() {
			defer wg.Done()
			_ = cs.ReleaseAllHandles(domain)
		}()
		wg.Wait()

		final, err := cs.ReadSynth(domain, p)
		if err != nil {
			t.Fatalf("iter %d final ReadSynth: %v", i, err)
		}
		committed := mustTip(t, s, ref) != tip0
		switch {
		case committed && !strings.Contains(string(final), edit):
			t.Fatalf("iter %d: tip advanced but the edit line is absent — partial commit: %q", i, final)
		case committed && !strings.Contains(string(final), "seed body"):
			t.Fatalf("iter %d: commit dropped the original body: %q", i, final)
		case !committed && string(final) != string(orig):
			t.Fatalf("iter %d: no commit but content changed from %q to %q", i, orig, final)
		}
	}

	rv := cs.existingView(domain)
	rv.fs.mu.Lock()
	leaked := len(rv.fs.handles)
	rv.fs.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("%d FS handles leaked after the open/releaseAll storm", leaked)
	}
}

// TestContentdCorruptAliasErrnoPreserved pins Q3: a non-ErrRefNotFound Tip failure
// on an aliased path (a symbolic-ref cycle) surfaces its real errno and KEEPS the
// alias at BOTH the read path (openEntity) and the commit path (resolveTarget) —
// never ENOENT. The commit-path leg additionally proves no DUPLICATE entity is
// minted over the transient fault.
func TestContentdCorruptAliasErrnoPreserved(t *testing.T) {
	cs, domain, s := directSource(t)
	createNote(t, s, "Seed", "seed body") // a real note so the /notes count is meaningful

	// A symbolic-ref cycle: Tip resolves to a recursion error, NOT ErrRefNotFound.
	gittest.Git(t, domain, "symbolic-ref", "refs/cc-notes/cyclea", "refs/cc-notes/cycleb")
	gittest.Git(t, domain, "symbolic-ref", "refs/cc-notes/cycleb", "refs/cc-notes/cyclea")

	rv, err := cs.viewFor(domain)
	if err != nil {
		t.Fatalf("viewFor: %v", err)
	}
	const p = "/notes/cyclic-alias.md"
	rv.fs.mu.Lock()
	rv.fs.aliases[p] = "refs/cc-notes/cyclea"
	rv.fs.mu.Unlock()

	// Read path (openEntity): the cyclic alias reads a LOUD transient error, not
	// ClassNotFound (which would read as a missing file that in fact exists).
	if _, _, err := cs.OpenHandle(domain, p); err == nil {
		t.Fatal("OpenHandle on a cyclic-ref alias succeeded, want a loud error")
	} else if cls := classOf(err); cls != content.ClassTransient {
		t.Fatalf("openEntity cyclic-alias class = %q, want %q (never ClassNotFound)", cls, content.ClassTransient)
	}

	// Commit path (resolveTarget): the same failure must not read as ENOENT and
	// fall through to create a DUPLICATE entity.
	before, err := cs.List(domain, "/notes")
	if err != nil {
		t.Fatalf("baseline List: %v", err)
	}
	rv.fs.mu.Lock()
	_, _, errc := rv.fs.commitDocument(p, []byte("---\ntitle: Dup\n---\nbody\n"))
	_, aliasKept := rv.fs.aliases[p]
	rv.fs.mu.Unlock()
	if errc == 0 || errc == -fuse.ENOENT {
		t.Fatalf("commitDocument errc = %d, want a loud non-ENOENT errno (a cyclic ref must not read as missing)", errc)
	}
	if !aliasKept {
		t.Fatal("commit path evicted the alias on a non-ErrRefNotFound Tip failure; it must keep it")
	}
	after, err := cs.List(domain, "/notes")
	if err != nil {
		t.Fatalf("post List: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("/notes count changed %d -> %d: a duplicate entity was minted over the transient fault", len(before), len(after))
	}
}
