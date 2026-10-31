package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestFoldEntryByteFormat(t *testing.T) {
	note := model.Note{ID: "noteid", Title: "t", Head: "abc123"}
	body, err := json.Marshal(note)
	if err != nil {
		t.Fatalf("marshal note: %v", err)
	}
	want := append([]byte{byte('0' + foldCacheVersion), ' '}, foldCacheGeneration+" note 7 5\n"...)
	want = append(want, body...)

	got, ok := encodeFoldEntry(note, 7, 5)
	if !ok {
		t.Fatal("encodeFoldEntry: ok=false")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeFoldEntry bytes =\n%q\nwant\n%q", got, want)
	}

	back, top, floor, ok := decodeFoldEntry(want)
	if !ok {
		t.Fatal("decodeFoldEntry of hand-crafted entry: ok=false")
	}
	if !reflect.DeepEqual(back, note) {
		t.Fatalf("decodeFoldEntry = %#v, want %#v", back, note)
	}
	if top != 7 {
		t.Fatalf("decodeFoldEntry top = %d, want 7", top)
	}
	if floor != 5 {
		t.Fatalf("decodeFoldEntry floor = %d, want 5", floor)
	}
}

// TestFoldGenerationTracksOpVocabulary proves the generation is derived, not
// declared: a binary whose op registry differs by even one kind derives a
// different tag, so a release that adds an op cannot forget to bump it.
func TestFoldGenerationTracksOpVocabulary(t *testing.T) {
	ours := model.OpKinds()
	if len(ours) < 2 {
		t.Fatalf("model.OpKinds() = %v, want a populated registry", ours)
	}
	want := foldGeneration(ours)
	if got := foldGeneration(model.OpKinds()); got != want {
		t.Fatalf("foldGeneration is not a function of the vocabulary alone: %q then %q", want, got)
	}
	cases := []struct {
		name  string
		kinds []string
	}{
		{"an op a newer binary adds", append(slices.Clone(ours), "seal_entity")},
		{"an op this binary would not have", ours[1:]},
		{"an op renamed on the wire", append(slices.Clone(ours[1:]), ours[0]+"_v2")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foldGeneration(tc.kinds); got == want {
				t.Fatalf("foldGeneration over %d kinds = %q, want a tag unequal to this binary's", len(tc.kinds), got)
			}
		})
	}
}

// TestFoldCacheMissesForeignGeneration is the cross-binary hazard the
// SkippedOps put guard cannot see: a newer cc-notes folds a chain completely
// and caches it under the shared git common dir, and this binary — which cannot
// apply every op in that chain — must re-fold rather than read the entry back
// reporting zero skips. The foreign entry comes out of this binary's own
// encoder under a swapped generation, so what fails here is the discrimination
// itself, not a hand-spelled header the decoder would reject anyway.
func TestFoldCacheMissesForeignGeneration(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)
	tip := model.SHA("abababababababababababababababababababab")
	note := model.Note{ID: "noteid", Title: "folded by a newer binary", Head: tip}

	newerGen := foldGeneration(append(slices.Clone(model.OpKinds()), "seal_entity"))
	c.write(string(tip), encodeAsGeneration(t, newerGen, note))

	if snap, ok := c.get(tip); ok {
		t.Fatalf("read a newer binary's entry: %#v", snap)
	}

	ours := model.Note{ID: "noteid", Title: "re-folded here", Head: tip}
	c.put(tip, ours, 1, 0)
	got, ok := c.get(tip)
	if !ok {
		t.Fatal("this binary's own entry missed after overwriting the foreign one")
	}
	if !reflect.DeepEqual(got, ours) {
		t.Fatalf("cache round-trip = %#v, want %#v", got, ours)
	}
}

// encodeAsGeneration encodes snap the way a binary whose op vocabulary digests
// to gen writes it: through this binary's encoder, with only the generation
// swapped.
func encodeAsGeneration(t *testing.T, gen string, snap model.Snapshot) []byte {
	t.Helper()
	ours := foldCacheGeneration
	foldCacheGeneration = gen
	defer func() { foldCacheGeneration = ours }()
	entry, ok := encodeFoldEntry(snap, 1, 0)
	if !ok {
		t.Fatalf("encodeFoldEntry %T: ok=false", snap)
	}
	return entry
}

// TestFoldCacheRefusesSkippedOps proves a partial fold never reaches disk.
// SkippedOps does not marshal, so a stored entry would come back reporting
// zero — a warm cache silently hiding history this binary cannot fold.
func TestFoldCacheRefusesSkippedOps(t *testing.T) {
	cache := newFoldCache(t.TempDir(), foldCacheCap)
	partial := model.Note{ID: "noteid", Title: "t", Head: "abc123", SkippedOps: 1}

	cache.put("abc123", partial, 1, 0)
	if snap, ok := cache.get("abc123"); ok {
		t.Fatalf("cached a partial fold: %#v", snap)
	}

	whole := partial
	whole.SkippedOps = 0
	cache.put("abc123", whole, 1, 0)
	snap, ok := cache.get("abc123")
	if !ok {
		t.Fatal("complete fold missed the cache")
	}
	if !reflect.DeepEqual(snap, whole) {
		t.Fatalf("cache round-trip = %#v, want %#v", snap, whole)
	}
}

func TestFoldCacheHitMiss(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("real")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	if _, err := s.Load(ctx, ref); err != nil {
		t.Fatalf("Load (populate): %v", err)
	}
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}

	sentinel := note
	sentinel.Title = "sentinel"
	s.cache.put(tip, sentinel, 1, 0)

	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load (cached): %v", err)
	}
	if got := loaded.(model.Note).Title; got != "sentinel" {
		t.Fatalf("Load did not consult cache: title = %q, want %q", got, "sentinel")
	}

	if _, ok := s.cache.get(model.SHA("0000000000000000000000000000000000000000")); ok {
		t.Fatal("get of unknown tip: want miss")
	}
}

func TestFoldCacheRebuildAfterDelete(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("rebuild")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	if _, err := s.Load(ctx, ref); err != nil {
		t.Fatalf("Load (populate): %v", err)
	}
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	if _, statErr := os.Stat(cacheEntryPath(t, &s.cache.lruDir, string(tip))); statErr != nil {
		t.Fatalf("entry not written after Load: %v", statErr)
	}

	wipeCache(t, s)

	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if got := loaded.(model.Note).Title; got != "rebuild" {
		t.Fatalf("Load after delete: title = %q, want %q", got, "rebuild")
	}
	if _, statErr := os.Stat(cacheEntryPath(t, &s.cache.lruDir, string(tip))); statErr != nil {
		t.Fatalf("entry not repopulated after re-fold: %v", statErr)
	}
}

func TestFoldCacheRejectsForeignHeaders(t *testing.T) {
	tip := model.SHA("aaaa000000000000000000000000000000000000")
	body, err := json.Marshal(model.Note{ID: "x", Head: tip})
	if err != nil {
		t.Fatalf("marshal note: %v", err)
	}
	entry := func(header string) []byte { return append([]byte(header+"\n"), body...) }
	read := func(t *testing.T, data []byte) (model.Snapshot, bool) {
		t.Helper()
		dir := t.TempDir()
		cache := newFoldCache(dir, foldCacheCap)
		defer func() { _ = cache.close() }()
		cache.write(string(tip), data)
		return cache.get(tip)
	}
	current := strconv.Itoa(foldCacheVersion) + " " + foldCacheGeneration + " note"
	if _, ok := read(t, entry(current+" 1 0")); !ok {
		t.Fatalf("well-formed entry %q missed", current+" 1 0")
	}

	cases := []struct {
		name   string
		header string
	}{
		{"a different cache epoch", "9 " + foldCacheGeneration + " note 1 0"},
		{"a v1 entry written before the generation tag", "1 note"},
		{"a v1 entry carrying the generation tag", "1 " + foldCacheGeneration + " note"},
		{"a current header missing both lamports", current},
		{"a v2 header from before the extend floor", current + " 1"},
		{"an empty top lamport", current + "  0"},
		{"a non-numeric top lamport", current + " seven 0"},
		{"a negative top lamport", current + " -1 0"},
		{"a top lamport past uint64", current + " 18446744073709551616 0"},
		{"an empty extend floor", current + " 1 "},
		{"a non-numeric extend floor", current + " 1 seven"},
		{"an extend floor past uint64", current + " 1 18446744073709551616"},
		{"a trailing header field", current + " 1 0 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if snap, ok := read(t, entry(tc.header)); ok {
				t.Fatalf("read an entry this binary cannot trust: %#v", snap)
			}
		})
	}
}

func TestFoldCacheV1NamespaceRebuildsDerivedEntries(t *testing.T) {
	common := t.TempDir()
	tip := model.SHA("abababababababababababababababababababab")
	entry, ok := encodeFoldEntry(model.Note{ID: "noteid", Title: "old derived entry", Head: tip}, 1, 0)
	if !ok {
		t.Fatal("encode old derived entry")
	}
	for _, prior := range []string{"folds", "folds-v1", "folds-v2", "folds-v3", "folds-v4", "folds-v5"} {
		oldDir := filepath.Join(common, "cc-notes", prior)
		if err := os.MkdirAll(oldDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oldDir, string(tip)), entry, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cache := newFoldCache(filepath.Join(common, foldCacheSubdir), foldCacheCap)
	if _, found := cache.get(tip); found {
		t.Fatal("prior derived-cache namespace was read")
	}
	want := model.Note{ID: "noteid", Title: "rebuilt", Head: tip}
	cache.put(tip, want, 1, 0)
	if got, found := cache.get(tip); !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt cache entry = %#v found=%v", got, found)
	}
	if _, err := os.Stat(cacheEntryPath(t, &cache.lruDir, string(tip))); err != nil {
		t.Fatalf("rebuilt cache path: %v", err)
	}
}

func TestFoldCacheLRUEviction(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, 2)
	tips := []model.SHA{
		"1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333",
	}
	for _, tip := range tips {
		c.put(tip, model.Note{ID: model.EntityID(tip), Head: tip}, 1, 0)
	}

	ents := c.names()
	if len(ents) > 2 {
		t.Fatalf("cache holds %d entries, want <= 2", len(ents))
	}
	if _, ok := c.get(tips[0]); ok {
		t.Fatalf("oldest entry %s not evicted", tips[0])
	}
	for _, tip := range tips[1:] {
		if _, ok := c.get(tip); !ok {
			t.Fatalf("entry %s missing after eviction", tip)
		}
	}
}

func TestFoldCacheRoundTripBothKinds(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)

	noteTip := model.SHA("aaaa111111111111111111111111111111111111")
	note := model.Note{
		ID:        "noteid",
		Title:     "title",
		Body:      "body",
		Tags:      []string{"a", "b"},
		Anchors:   []model.Anchor{{Kind: model.AnchorPath, Value: "x.go"}},
		Author:    testActor,
		CreatedAt: 100,
		UpdatedAt: 200,
		Head:      noteTip,
	}
	c.put(noteTip, note, 3, 0)
	got, top, _, ok := c.getTop(noteTip)
	if !ok {
		t.Fatal("note round-trip: get miss")
	}
	if !reflect.DeepEqual(got, note) {
		t.Fatalf("note round-trip: got %#v, want %#v", got, note)
	}
	if top != 3 {
		t.Fatalf("note round-trip: top = %d, want 3", top)
	}

	taskTip := model.SHA("bbbb222222222222222222222222222222222222")
	task := model.Task{
		ID:        "taskid",
		Branch:    "main",
		Title:     "ship",
		Type:      model.TypeTask,
		Status:    model.StatusInProgress,
		Priority:  1,
		Assignee:  testActor,
		Labels:    []string{"x"},
		CreatedAt: 1,
		UpdatedAt: 2,
		StartedAt: 3,
		Head:      taskTip,
	}
	c.put(taskTip, task, 1, 0)
	gotTask, ok := c.get(taskTip)
	if !ok {
		t.Fatal("task round-trip: get miss")
	}
	if !reflect.DeepEqual(gotTask, task) {
		t.Fatalf("task round-trip: got %#v, want %#v", gotTask, task)
	}
}

func TestFoldCacheRoundTripDoc(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)

	tip := model.SHA("dddd111111111111111111111111111111111111")
	doc := model.Doc{
		ID:        "docid",
		Title:     "design",
		Body:      "long body",
		When:      "before touching the fold",
		Tags:      []string{"a", "b"},
		Anchors:   []model.Anchor{{Kind: model.AnchorPath, Value: "fold.go"}},
		Author:    testActor,
		CreatedAt: 100,
		UpdatedAt: 200,
		Witness: []model.AnchorWitness{
			{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: "abc1234"}, OID: "abc1234"},
		},
		VerifiedAt:     150,
		VerifiedBy:     testActor,
		VerifiedCommit: "deadbeef",
		Head:           tip,
	}
	c.put(tip, doc, 1, 0)

	got, ok := c.get(tip)
	if !ok {
		t.Fatal("doc round-trip: get miss")
	}
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("doc round-trip: got %#v, want %#v", got, doc)
	}
	if got.(model.Doc).When != doc.When {
		t.Fatalf("When = %q, want %q", got.(model.Doc).When, doc.When)
	}
}

func TestFoldCacheRoundTripLog(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)

	tip := model.SHA("eeee111111111111111111111111111111111111")
	log := model.Log{
		ID:    "logid",
		Title: "rollout",
		Entries: []model.LogEntry{
			{Author: testActor, TS: 150, Text: "flipped to 5%"},
			{Author: testActor, TS: 250, Text: "flipped to 50%"},
		},
		Tags:      []string{"a", "b"},
		Anchors:   []model.Anchor{{Kind: model.AnchorDir, Value: "internal/auth"}},
		Author:    testActor,
		CreatedAt: 100,
		UpdatedAt: 250,
		Head:      tip,
	}
	c.put(tip, log, 1, 0)

	got, ok := c.get(tip)
	if !ok {
		t.Fatal("log round-trip: get miss")
	}
	if !reflect.DeepEqual(got, log) {
		t.Fatalf("log round-trip: got %#v, want %#v", got, log)
	}
}

func TestFoldCacheTaskP3FieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)

	tip := model.SHA("cccc333333333333333333333333333333333333")
	task := model.Task{
		ID:               "taskid",
		Branch:           "main",
		Title:            "ship",
		Type:             model.TypeTask,
		Status:           model.StatusInProgress,
		Assignee:         testActor,
		HeartbeatAt:      1717000000,
		HeartbeatLamport: 42,
		Commits: []model.SHA{
			"1111111111111111111111111111111111111111",
			"2222222222222222222222222222222222222222",
		},
		CreatedAt: 1,
		UpdatedAt: 2,
		Head:      tip,
	}
	c.put(tip, task, 1, 0)

	got, ok := c.get(tip)
	if !ok {
		t.Fatal("P3 task round-trip: get miss")
	}
	gotTask := got.(model.Task)
	if gotTask.HeartbeatAt != task.HeartbeatAt {
		t.Errorf("HeartbeatAt = %d, want %d", gotTask.HeartbeatAt, task.HeartbeatAt)
	}
	if gotTask.HeartbeatLamport != task.HeartbeatLamport {
		t.Errorf("HeartbeatLamport = %d, want %d", gotTask.HeartbeatLamport, task.HeartbeatLamport)
	}
	if !reflect.DeepEqual(gotTask.Commits, task.Commits) {
		t.Errorf("Commits = %v, want %v", gotTask.Commits, task.Commits)
	}
}

func TestFoldCacheCorruptEntryIsMiss(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("corrupt")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	if _, err := s.Load(ctx, ref); err != nil {
		t.Fatalf("Load (populate): %v", err)
	}
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	cacheEntryFrame(t, &s.cache.lruDir, string(tip)).write(t, []byte("not a cache entry"))

	if _, ok := s.cache.get(tip); ok {
		t.Fatal("get of corrupt entry: want miss")
	}
	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load over corrupt entry: %v", err)
	}
	if got := loaded.(model.Note).Title; got != "corrupt" {
		t.Fatalf("Load over corrupt entry: title = %q, want %q", got, "corrupt")
	}
}

// TestFoldCacheHoldsTheLiveRefsOfALargeStore pins the cap above the 14,063 live
// refs one repository held on Oct 31 2026. At the old 16,384 a full listing of a
// store that size evicted entries it had just written, and the next listing
// folded them again.
func TestFoldCacheHoldsTheLiveRefsOfALargeStore(t *testing.T) {
	const live = 20000
	c := newFoldCache(t.TempDir(), foldCacheCap)
	tips := make([]model.SHA, live)
	for i := range tips {
		tips[i] = model.SHA(fmt.Sprintf("%040x", i+1))
		c.put(tips[i], model.Note{ID: fmt.Sprintf("n%d", i), Title: "t", Head: tips[i]}, 1, 0)
	}
	for _, tip := range []model.SHA{tips[0], tips[live/2], tips[live-1]} {
		if _, ok := c.get(tip); !ok {
			t.Fatalf("entry %s was evicted after writing %d live refs", tip, live)
		}
	}
}
