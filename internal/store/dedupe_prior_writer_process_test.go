package store

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

const duplicatePriorWriterRequestEnv = "CCN_DUPLICATE_PRIOR_WRITER_REQUEST"

type duplicatePriorWriterRequest struct {
	Repo   string
	Title  string
	Output string
}

type duplicatePriorWriterResult struct {
	ID       model.EntityID
	Metadata queryExecutableResult
}

func TestDuplicatePriorWriterProcess(t *testing.T) {
	raw := os.Getenv(duplicatePriorWriterRequestEnv)
	if raw == "" {
		t.Skip("prior writer helper")
	}
	var request duplicatePriorWriterRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	s, err := OpenContext(t.Context(), request.Repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	value, err := s.CreateExact(t.Context(), docOps(request.Title))
	if err != nil {
		t.Fatal(err)
	}
	result := duplicatePriorWriterResult{ID: value.EntityID(), Metadata: readQueryExecutableResult(t, s)}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Output, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
