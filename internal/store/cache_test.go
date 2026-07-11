package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

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
	s.cache.put(tip, sentinel)

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
	dir, err := s.cache.resolveDir()
	if err != nil {
		t.Fatalf("resolveDir: %v", err)
	}
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, string(tip))); statErr != nil {
		t.Fatalf("entry not written after Load: %v", statErr)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove cache dir: %v", err)
	}

	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if got := loaded.(model.Note).Title; got != "rebuild" {
		t.Fatalf("Load after delete: title = %q, want %q", got, "rebuild")
	}
	if _, statErr := os.Stat(filepath.Join(dir, string(tip))); statErr != nil {
		t.Fatalf("entry not repopulated after re-fold: %v", statErr)
	}
}

func TestFoldCacheVersionBump(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)
	tip := model.SHA("aaaa000000000000000000000000000000000000")

	stale := append([]byte{byte('0' + foldCacheVersion - 1), ' '}, "note\n{\"id\":\"x\"}"...)
	if err := os.WriteFile(filepath.Join(dir, string(tip)), stale, 0o600); err != nil {
		t.Fatalf("write stale entry: %v", err)
	}

	if _, ok := c.get(tip); ok {
		t.Fatal("get of version-mismatched entry: want miss")
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
		c.put(tip, model.Note{ID: model.EntityID(tip), Head: tip})
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
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
	c.put(noteTip, note)
	got, ok := c.get(noteTip)
	if !ok {
		t.Fatal("note round-trip: get miss")
	}
	if !reflect.DeepEqual(got, note) {
		t.Fatalf("note round-trip: got %#v, want %#v", got, note)
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
	c.put(taskTip, task)
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
	c.put(tip, doc)

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
	c.put(tip, log)

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
	c.put(tip, task)

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

func TestFoldCachePreP3EntryInvalidated(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)
	tip := model.SHA("dddd444444444444444444444444444444444444")

	// A valid task entry written by a pre-P3 binary under cache version 2: the
	// JSON predates the heartbeat_at/heartbeat_lamport/commits fields. The body
	// parses cleanly, so the version byte is the only thing that makes get miss;
	// reverting foldCacheVersion to 2 turns this into a hit and fails the test.
	preP3 := append([]byte{byte('0' + 2), ' '}, "task\n{\"id\":\"taskid\",\"branch\":\"main\",\"title\":\"old\",\"head\":\""+string(tip)+"\"}"...)
	if err := os.WriteFile(filepath.Join(dir, string(tip)), preP3, 0o600); err != nil {
		t.Fatalf("write pre-P3 entry: %v", err)
	}

	if _, ok := c.get(tip); ok {
		t.Fatal("get of pre-P3 (version 2) task entry: want miss after bump to 3")
	}
}

func TestFoldCachePreAttachmentEntryInvalidated(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)
	tip := model.SHA("eeee555555555555555555555555555555555555")

	// A valid note entry written by a pre-attachment binary under cache
	// version 4: the JSON predates the attachments field. The body parses
	// cleanly, so the version byte is the only thing that makes get miss;
	// reverting foldCacheVersion to 4 turns this into a hit and fails the test.
	preLFS := append([]byte{byte('0' + 4), ' '}, "note\n{\"id\":\"noteid\",\"title\":\"old\",\"head\":\""+string(tip)+"\"}"...)
	if err := os.WriteFile(filepath.Join(dir, string(tip)), preLFS, 0o600); err != nil {
		t.Fatalf("write pre-attachment entry: %v", err)
	}

	if _, ok := c.get(tip); ok {
		t.Fatal("get of pre-attachment (version 4) note entry: want miss after bump to 5")
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
	dir, err := s.cache.resolveDir()
	if err != nil {
		t.Fatalf("resolveDir: %v", err)
	}
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, string(tip)), []byte("not a cache entry"), 0o600); err != nil {
		t.Fatalf("corrupt entry: %v", err)
	}

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
