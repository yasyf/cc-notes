package store

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type answerPriorWriterCommand struct {
	Kind   model.Kind
	ID     model.EntityID
	Title  string
	Body   string
	Delete bool
}

type answerPriorWriterReply struct {
	Ready bool
	Error string
}

func TestAnswerPriorWriterProcess(t *testing.T) {
	repo := os.Getenv("CCN_ANSWER_PRIOR_WRITER_REPO")
	if repo == "" {
		t.Skip("prior answer writer helper")
	}
	s, err := OpenContext(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(answerPriorWriterReply{Ready: true}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(os.Stdin)
	for {
		var command answerPriorWriterCommand
		if err := decoder.Decode(&command); err == io.EOF {
			return
		} else if err != nil {
			t.Fatal(err)
		}
		ops := []model.Op{model.SetTitle{Title: command.Title}, model.SetBody{Body: command.Body}}
		if command.Delete {
			ops = []model.Op{model.DeleteNote{}}
		}
		kind := command.Kind
		if kind == "" {
			kind = model.KindAnswer
		}
		_, err := s.Append(t.Context(), refs.For(kind, command.ID), ops)
		var reply answerPriorWriterReply
		if err != nil {
			reply.Error = err.Error()
		}
		if err := encoder.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
}
