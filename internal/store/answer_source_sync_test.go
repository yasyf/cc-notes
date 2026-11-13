package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/yasyf/cc-notes/internal/answermatch"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestIndexedAnswerMatchesPriorV8Writer(t *testing.T) {
	executable := os.Getenv("CCN_ANSWER_V8_WRITER")
	if executable == "" {
		t.Skip("requires the actual v8 writer executable from f7965dac1e02bbec6b5f33d71c437220b67e9c3a")
	}
	s := initStore(t)
	var ids []model.EntityID
	for i := range 24 {
		snapshot, err := s.Create(t.Context(), []model.Op{model.CreateAnswer{
			Nonce: model.NewNonce(), Title: fmt.Sprintf("original %d", i), Body: "original body",
			Tags: []string{"mixedwriter"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, snapshot.EntityID())
	}
	note := create(t, s, noteOps("prior note")).(model.Note)
	cmd := exec.Command(executable, "-test.run=^TestAnswerPriorWriterProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CCN_ANSWER_PRIOR_WRITER_REPO="+s.Git.Dir)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stdin.Close(); err != nil {
			t.Error(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	})
	decoder := json.NewDecoder(stdout)
	var ready answerPriorWriterReply
	if err := decoder.Decode(&ready); err != nil || !ready.Ready {
		t.Fatalf("open protected v8 writer: %+v, %v", ready, err)
	}
	if err := s.Reindex(t.Context()); err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(stdin)
	write := func(command answerPriorWriterCommand) {
		t.Helper()
		if err := encoder.Encode(command); err != nil {
			t.Fatal(err)
		}
		var reply answerPriorWriterReply
		if err := decoder.Decode(&reply); err != nil || reply.Error != "" {
			t.Fatalf("v8 write: %s, %v", reply.Error, err)
		}
	}
	check := func(term string, wantSeen, wantRows int, wantLoads int64, labels []string) {
		t.Helper()
		var loads atomic.Int64
		ctx := context.WithValue(t.Context(), answerCorpusMetadataWorkKey{}, &loads)
		terms := slices.Sorted(maps.Keys(answermatch.Terms(term)))
		got, err := s.IndexedAnswerMatches(ctx, labels, terms)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Answers) != wantRows || got.Seen[terms[0]] != wantSeen || loads.Load() != wantLoads {
			t.Fatalf("term %q: rows=%d seen=%d metadata_loads=%d; want %d/%d/%d", term, len(got.Answers), got.Seen[terms[0]], loads.Load(), wantRows, wantSeen, wantLoads)
		}
	}
	write(answerPriorWriterCommand{Kind: model.KindNote, ID: note.ID, Title: "changed note", Body: "updated note"})
	status, hit, err := s.StatusMetadata(t.Context(), model.KindNote)
	if err != nil || !hit || len(status.Records) != 1 {
		t.Fatalf("unchanged note schema after v8 write: hit=%v records=%d error=%v", hit, len(status.Records), err)
	}
	snapshot, err := status.Captured.LoadAt(t.Context(), status.Records[0].Tip)
	if err != nil || snapshot.(model.Note).Title != "changed note" {
		t.Fatalf("v8 note publication was not preserved: %v", err)
	}
	check("original", 24, 24, 0, nil)
	write(answerPriorWriterCommand{ID: ids[0], Title: "aurora", Body: "quasar"})
	check("aurora", 1, 24, 1, nil)
	check("quasar", 1, 24, 0, []string{"mixedwriter"})
	check("original", 23, 24, 0, nil)
	write(answerPriorWriterCommand{ID: ids[0], Title: "zenith", Body: "pulsar"})
	if _, err := s.Append(t.Context(), refs.For(model.KindAnswer, ids[1]), []model.Op{model.SetBody{Body: "native"}}); err != nil {
		t.Fatal(err)
	}
	check("zenith", 1, 24, 1, []string{"mixedwriter"})
	check("aurora", 0, 24, 0, nil)
	write(answerPriorWriterCommand{ID: ids[0], Delete: true})
	check("zenith", 0, 23, 1, nil)
	check("pulsar", 0, 23, 0, []string{"mixedwriter"})
	write(answerPriorWriterCommand{ID: ids[1], Title: "statusfresh", Body: "statusbody"})
	status, hit, err = s.StatusMetadata(t.Context(), model.KindAnswer)
	if err != nil || !hit || len(status.Records) != 24 {
		t.Fatalf("answer status after v8 write: hit=%v records=%d error=%v", hit, len(status.Records), err)
	}
	check("statusfresh", 1, 23, 0, nil)
}
