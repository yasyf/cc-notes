package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func corpusAnswer(t *testing.T, s *Store, title, body string, tags []string, anchors []model.Anchor) model.Answer {
	t.Helper()
	value, err := s.CreateExact(t.Context(), []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: title, Body: body, Tags: tags, Anchors: anchors}})
	if err != nil {
		t.Fatal(err)
	}
	return value.(model.Answer)
}

func openCorpus(t *testing.T, s *Store, cache string) *AnswerCorpusCut {
	t.Helper()
	cut, err := s.OpenAnswerCorpus(t.Context(), "test", "main", cache)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cut.Close(); err != nil {
			t.Error(err)
		}
	})
	return cut
}

func TestAnswerCorpusColdCaptureThenWriteKeepsSelectedTips(t *testing.T) {
	s := initStore(t)
	a := corpusAnswer(t, s, "before", "original\n", []string{"scope:durable", "program:test"}, nil)
	selected, err := s.selectAnswerCorpus(t.Context(), "test", "main")
	if err != nil {
		t.Fatal(err)
	}
	appendOps(t, s, answerRef(a.ID), model.SetTitle{Title: "after"}, model.SetBody{Body: "replacement"})
	cache := t.TempDir()
	cut, err := s.openAnswerCorpusSelection(t.Context(), selected, "test", "main", cache)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cut.Close() }()
	if got, err := cut.Text(t.Context(), a.ID.Short()); err != nil || got != "# before\n\noriginal\n\n" {
		t.Fatalf("captured text = %q, %v", got, err)
	}
	if cut.Records[0].Tip != a.Head || cut.Generation != selected.generation {
		t.Fatalf("captured metadata = %+v, generation %s", cut.Records, cut.Generation)
	}
	next := openCorpus(t, s, cache)
	if next.Generation == cut.Generation || next.Records[0].Tip == cut.Records[0].Tip {
		t.Fatal("next capture retained a changed tip")
	}
	if got, err := next.Text(t.Context(), a.ID.Short()); err != nil || got != "# after\n\nreplacement\n" {
		t.Fatalf("next text = %q, %v", got, err)
	}
	if got, err := cut.Pick(t.Context(), []string{a.ID.Short()}); err != nil || !reflect.DeepEqual(got, []model.Answer{a}) {
		t.Fatalf("old pick = %+v, %v; want %+v", got, err, a)
	}
}

func TestAnswerCorpusPreopenedWriterAndDirectRefLifecycle(t *testing.T) {
	s := initStore(t)
	writer, err := Open(s.Git.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	a := corpusAnswer(t, writer, "answer", "old", []string{"scope:durable", "program:test"}, nil)
	cache := t.TempDir()
	first := openCorpus(t, s, cache)
	appendOps(t, writer, answerRef(a.ID), model.SetBody{Body: "new"})
	changed := openCorpus(t, s, cache)
	if changed.Generation == first.Generation || changed.work.metadataLoads.Load() != 1 || changed.work.canonicalRefreshes.Load() != 1 || changed.work.payloadWrites.Load() != 1 {
		t.Fatalf("changed work = metadata %d, canonical %d, payload %d", changed.work.metadataLoads.Load(), changed.work.canonicalRefreshes.Load(), changed.work.payloadWrites.Load())
	}
	appendOps(t, writer, answerRef(a.ID), model.RemoveTag{Tag: "program:test"})
	if got := openCorpus(t, s, cache); len(got.Records) != 0 {
		t.Fatalf("removed program label still selected: %+v", got.Records)
	}
	appendOps(t, writer, answerRef(a.ID), model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}})
	anchored := openCorpus(t, s, cache)
	if len(anchored.Records) != 1 {
		t.Fatalf("branch anchor not selected: %+v", anchored.Records)
	}
	appendOps(t, writer, answerRef(a.ID), model.AddSupersededBy{ID: model.EntityID(strings.Repeat("f", 40))})
	if got := openCorpus(t, s, cache); len(got.Records) != 0 {
		t.Fatal("superseded answer selected")
	}
	appendOps(t, writer, answerRef(a.ID), model.RemoveSupersededBy{ID: model.EntityID(strings.Repeat("f", 40))})
	restored := openCorpus(t, s, cache)
	tip := restored.Records[0].Tip
	if err := writer.RecordsGit.UpdateRef(t.Context(), answerRef(a.ID), "", tip); err != nil {
		t.Fatal(err)
	}
	if got := openCorpus(t, s, cache); len(got.Records) != 0 {
		t.Fatal("deleted ref selected")
	}
	if err := writer.RecordsGit.UpdateRef(t.Context(), answerRef(a.ID), tip, ""); err != nil {
		t.Fatal(err)
	}
	if got := openCorpus(t, s, cache); got.Generation != restored.Generation || len(got.Records) != 1 {
		t.Fatal("direct ref restore did not restore exact selection")
	}
	appendOps(t, writer, answerRef(a.ID), model.DeleteNote{})
	if got := openCorpus(t, s, cache); len(got.Records) != 0 {
		t.Fatal("tombstoned answer selected")
	}
	if got, err := first.Text(t.Context(), a.ID.Short()); err != nil || got != "# answer\n\nold\n" {
		t.Fatalf("held original = %q, %v", got, err)
	}
}

func TestAnswerCorpusLazyPayloadSurvivesSourcePruneAndReplacement(t *testing.T) {
	s := initStore(t)
	a := corpusAnswer(t, s, "雪🙂", "café\n", []string{"scope:durable", "program:test"}, nil)
	cache := t.TempDir()
	cut := openCorpus(t, s, cache)
	if cut.work.payloadReads.Load() != 0 {
		t.Fatal("open hydrated a payload")
	}
	if err := s.RecordsGit.UpdateRef(t.Context(), answerRef(a.ID), "", a.Head); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, s.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, s.Git.Dir, "gc", "--prune=now")
	if err := os.RemoveAll(s.cache.dir); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(s.Git.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	if _, err := fresh.LoadAt(t.Context(), a.Head); err == nil {
		t.Fatal("pruned source tip remained readable")
	}
	if got, err := cut.Text(t.Context(), a.ID.Short()); err != nil || got != "# 雪🙂\n\ncafé\n\n" {
		t.Fatalf("lazy text after prune = %q, %v", got, err)
	}
	if got, err := cut.Pick(t.Context(), []string{a.ID.Short(), a.ID.Short()}); err != nil || !reflect.DeepEqual(got, []model.Answer{a, a}) {
		t.Fatalf("lazy pick after prune = %+v, %v", got, err)
	}
	if got := openCorpus(t, fresh, cache); len(got.Records) != 0 || got.Generation == cut.Generation {
		t.Fatal("next cut ignored deleted source")
	}
	replaceRepo(t, s.Git.Dir)
	replacement, err := Open(s.Git.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	if got := openCorpus(t, replacement, cache); got.Binding == cut.Binding {
		t.Fatal("replacement source reused original binding")
	}
	if got, err := cut.Pick(t.Context(), []string{a.ID.Short()}); err != nil || got[0].Body != a.Body {
		t.Fatalf("held pick after source replacement = %+v, %v", got, err)
	}
}

func TestAnswerCorpusMetadataTimeCodecAndUnionOrder(t *testing.T) {
	a := model.EntityID("abcdef0" + strings.Repeat("0", 33))
	b := model.EntityID("abcdef0" + strings.Repeat("1", 33))
	c := model.EntityID(strings.Repeat("0", 40))
	timestamp := time.Date(9999, 12, 31, 23, 59, 58, 100, time.UTC)
	rows := []queryMetadata{
		{ID: a, Tip: "a", UpdatedTime: timestamp, Labels: []string{"program:test"}, Anchors: []model.Anchor{{Kind: model.AnchorBranch, Value: "main"}}},
		{ID: b, Tip: "b", UpdatedTime: timestamp.Add(800 * time.Nanosecond), Labels: []string{"program:test"}},
		{ID: c, Tip: "c", UpdatedTime: timestamp.Add(900 * time.Nanosecond), Anchors: []model.Anchor{{Kind: model.AnchorBranch, Value: "main"}}},
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var restored []queryMetadata
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	selection, err := makeAnswerCorpusSelection("source", "metadata", "test", "main", restored)
	if err != nil {
		t.Fatal(err)
	}
	var selected []model.EntityID
	for _, row := range selection.selected {
		selected = append(selected, row.ID)
	}
	if !slices.Equal(selected, []model.EntityID{b, a, c}) || len(selection.records) != 2 || selection.records[0].ID != c || selection.records[1].ID != a || !selection.records[1].UpdatedAt.Equal(timestamp) {
		t.Fatalf("codec/union/prefix order = %v, %+v", selected, selection.records)
	}
	canonical := model.Answer{UpdatedAt: 1700000000}.Meta().UpdatedAt
	if canonical.Nanosecond() != 0 || !canonical.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("canonical seconds contract changed: %s", canonical)
	}
}

func TestAnswerCorpusEqualMarkdownPreservesCanonicalFieldBoundaries(t *testing.T) {
	s := initStore(t)
	a := corpusAnswer(t, s, "a\n\nb", "c", []string{"scope:durable", "program:test"}, nil)
	b := corpusAnswer(t, s, "a", "b\n\nc", []string{"scope:durable", "program:test"}, nil)
	cut := openCorpus(t, s, t.TempDir())
	if len(cut.Records) != 2 || cut.Records[0].TextDigest != cut.Records[1].TextDigest || cut.work.payloadWrites.Load() != 2 {
		t.Fatalf("equal Markdown payload identities = %+v, writes %d", cut.Records, cut.work.payloadWrites.Load())
	}
	for _, id := range []string{a.ID.Short(), b.ID.Short()} {
		if got, err := cut.Text(t.Context(), id); err != nil || got != "# a\n\nb\n\nc\n" {
			t.Fatalf("shared Markdown = %q, %v", got, err)
		}
	}
	if got, err := cut.Pick(t.Context(), []string{b.ID.Short(), a.ID.Short()}); err != nil || !reflect.DeepEqual(got, []model.Answer{b, a}) {
		t.Fatalf("canonical field boundaries = %+v, %v", got, err)
	}
}

func corpusScaleFixture(t *testing.T, count int) (*Store, model.EntityID) {
	t.Helper()
	s := initStore(t)
	signature := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
	updates := make([]gitcmd.RefUpdate, 0, count)
	var first model.EntityID
	for i := range count {
		ops := []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: fmt.Sprintf("answer %05d", i), Body: "canonical 🙂 payload", Tags: []string{"scope:durable", "program:test"}}}
		tip, err := s.Repo.WriteOpsCommit(t.Context(), nil, signature, "answer corpus fixture", model.Pack{Lamport: 1, Ops: ops})
		if err != nil {
			t.Fatal(err)
		}
		id := model.EntityID(tip)
		if i == 0 {
			first = id
		}
		updates = append(updates, gitcmd.RefUpdate{Ref: answerRef(id), New: tip})
	}
	if err := s.PublishRefs(t.Context(), updates); err != nil {
		t.Fatal(err)
	}
	return s, first
}

func TestAnswerCorpusWarmAndChangedPayloadWork(t *testing.T) {
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, _ := corpusScaleFixture(t, count)
			cache := t.TempDir()
			cold := openCorpus(t, s, cache)
			if got := cold.work.canonicalRefreshes.Load(); got != int64(count) || cold.work.payloadWrites.Load() != int64(count) || cold.work.payloadReads.Load() != 0 || cold.work.answerDecodes.Load() != 0 {
				t.Fatalf("cold work = canonical %d writes %d reads %d DTO %d", got, cold.work.payloadWrites.Load(), cold.work.payloadReads.Load(), cold.work.answerDecodes.Load())
			}
			warm := openCorpus(t, s, cache)
			if warm.Generation != cold.Generation || warm.work.metadataLoads.Load() != 0 || warm.work.canonicalRefreshes.Load() != 0 || warm.work.payloadWrites.Load() != 0 || warm.work.payloadReads.Load() != 0 || warm.work.answerDecodes.Load() != 0 {
				t.Fatal("unchanged cut visited canonical payloads")
			}
			id := warm.Records[0].ID
			appendOps(t, s, answerRef(id), model.SetBody{Body: "one changed answer"})
			changed := openCorpus(t, s, cache)
			if changed.Generation == warm.Generation || changed.work.metadataLoads.Load() != 1 || changed.work.canonicalRefreshes.Load() != 1 || changed.work.payloadWrites.Load() != 1 || changed.work.payloadReads.Load() != 0 {
				t.Fatalf("one change work = metadata %d canonical %d writes %d reads %d", changed.work.metadataLoads.Load(), changed.work.canonicalRefreshes.Load(), changed.work.payloadWrites.Load(), changed.work.payloadReads.Load())
			}
			appendOps(t, s, answerRef(id), model.AddTag{Tag: "unrelated-metadata"})
			metadata := openCorpus(t, s, cache)
			if metadata.Generation == changed.Generation || metadata.work.metadataLoads.Load() != 1 || metadata.work.canonicalRefreshes.Load() != 1 || metadata.work.payloadWrites.Load() != 0 || metadata.work.payloadReads.Load() != 0 {
				t.Fatal("metadata-only change rewrote or read payloads")
			}
			if got, err := metadata.Pick(t.Context(), []string{id.Short()}); err != nil || len(got) != 1 || got[0].Body != "one changed answer" || !slices.Contains(got[0].Tags, "unrelated-metadata") {
				t.Fatalf("bounded pick = %+v, %v", got, err)
			}
			if metadata.work.payloadReads.Load() != 1 || metadata.work.answerDecodes.Load() != 1 {
				t.Fatal("pick exceeded one payload/DTO")
			}
			corpusAnswer(t, s, "unselected", "outside", nil, nil)
			if got := openCorpus(t, s, cache); got.Generation != metadata.Generation || got.work.canonicalRefreshes.Load() != 0 || got.work.payloadWrites.Load() != 0 {
				t.Fatal("unselected answer invalidated selected cut")
			}
			t.Logf("N=%d cold canonical=%d; warm metadata=0 canonical=0 payload reads=0 DTO=0; body change canonical=1 writes=1; metadata change canonical=1 writes=0; pick payload=1 DTO=1", count, cold.work.canonicalRefreshes.Load())
		})
	}
}

func TestAnswerCorpusConcurrentColdReadersAndSourceWriter(t *testing.T) {
	s := initStore(t)
	a := corpusAnswer(t, s, "concurrent", "before", []string{"scope:durable", "program:test"}, nil)
	cache := t.TempDir()
	start := make(chan struct{})
	type result struct {
		cut *AnswerCorpusCut
		err error
	}
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			cut, err := s.OpenAnswerCorpus(t.Context(), "test", "main", cache)
			results <- result{cut, err}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var cuts []*AnswerCorpusCut
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		cuts = append(cuts, result.cut)
		defer func() { _ = result.cut.Close() }()
	}
	if cuts[0].Generation != cuts[1].Generation || cuts[0].work.payloadWrites.Load()+cuts[1].work.payloadWrites.Load() != 1 {
		t.Fatal("cold readers did not publish one complete payload")
	}
	appendOps(t, s, answerRef(a.ID), model.SetBody{Body: "after"})
	next := openCorpus(t, s, cache)
	if got, err := next.Text(t.Context(), a.ID.Short()); err != nil || got != "# concurrent\n\nafter\n" {
		t.Fatalf("concurrent next = %q, %v", got, err)
	}
	for _, cut := range cuts {
		if got, err := cut.Text(t.Context(), a.ID.Short()); err != nil || got != "# concurrent\n\nbefore\n" {
			t.Fatalf("held concurrent cut = %q, %v", got, err)
		}
	}
}

func TestAnswerCorpusErrorsCancellationAndClose(t *testing.T) {
	s := initStore(t)
	a := corpusAnswer(t, s, "empty", "", []string{"scope:durable", "program:test"}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if cut, err := s.OpenAnswerCorpus(ctx, "test", "main", t.TempDir()); cut != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open = %v, %v", cut, err)
	}
	cache := t.TempDir()
	cut := openCorpus(t, s, cache)
	if got, err := cut.Text(t.Context(), a.ID.Short()); err != nil || got != "# empty\n\n\n" {
		t.Fatalf("empty body text = %q, %v", got, err)
	}
	if got, err := cut.Pick(t.Context(), []string{a.ID.Short(), "unknown"}); got != nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial invalid pick = %+v, %v", got, err)
	}
	if _, err := cut.Text(ctx, a.ID.Short()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled text = %v", err)
	}
	cut.Records[0].ID = model.EntityID(strings.Repeat("f", 40))
	if got, err := cut.Pick(t.Context(), []string{a.ID.Short()}); err != nil || got[0].ID != a.ID {
		t.Fatalf("public metadata mutation relabeled words: %+v, %v", got, err)
	}
	if err := cut.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cut.Close(); err != nil || cut.db.Stats().OpenConnections != 0 {
		t.Fatalf("closed handles = %+v, %v", cut.db.Stats(), err)
	}
	if _, err := cut.Text(t.Context(), a.ID.Short()); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed text = %v", err)
	}
	if _, err := cut.Pick(t.Context(), nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed pick = %v", err)
	}
	broken := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(broken, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.OpenAnswerCorpus(t.Context(), "test", "main", broken); got != nil || err == nil {
		t.Fatalf("unavailable owned cache = %v, %v", got, err)
	}
}

func TestAnswerCorpusCacheCorruptionRefusesAndHeldViewStaysExact(t *testing.T) {
	s := initStore(t)
	a := corpusAnswer(t, s, "canonical", "owner words", []string{"scope:durable", "program:test"}, nil)
	cache := t.TempDir()
	cut := openCorpus(t, s, cache)
	files, err := filepath.Glob(filepath.Join(cache, "*.sqlite"))
	if err != nil || len(files) != 1 {
		t.Fatalf("cache files = %v, %v", files, err)
	}
	db, err := sql.Open("sqlite", files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), "UPDATE payload SET body=?", []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	if got, err := cut.Text(t.Context(), a.ID.Short()); err != nil || got != "# canonical\n\nowner words\n" {
		t.Fatalf("held view followed cache writer = %q, %v", got, err)
	}
	corrupt := openCorpus(t, s, cache)
	if got, err := corrupt.Text(t.Context(), a.ID.Short()); got != "" || err == nil {
		t.Fatalf("corrupt words accepted = %q, %v", got, err)
	}
	if _, err := db.ExecContext(t.Context(), "UPDATE member SET record=?", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.OpenAnswerCorpus(t.Context(), "test", "main", cache); got != nil || err == nil {
		t.Fatalf("inconsistent membership accepted = %v, %v", got, err)
	}
}
