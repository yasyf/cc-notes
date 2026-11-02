package store

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/model"
)

func TestManagedProofValidatorPreservesStateAndReceiptInvariants(t *testing.T) {
	id := strings.Repeat("a", 64)
	valid := sourceindex.Operation{Previous: model.SHA(strings.Repeat("1", 40)), Token: model.SHA(strings.Repeat("2", 40)), Result: "valid", RequestDigest: sha256.Sum256([]byte("request")), State: gitobj.SourceOperationApplied, RetentionRoot: model.SHA(strings.Repeat("3", 40))}
	tests := []struct {
		name   string
		change func(*sourceindex.Operation)
	}{
		{"zero request digest", func(op *sourceindex.Operation) { op.RequestDigest = [32]byte{} }},
		{"NUL result", func(op *sourceindex.Operation) { op.Result = "bad\x00result" }},
		{"newline result", func(op *sourceindex.Operation) { op.Result = "bad\nresult" }},
		{"CR result", func(op *sourceindex.Operation) { op.Result = "bad\rresult" }},
		{"unknown state", func(op *sourceindex.Operation) { op.State = 0 }},
		{"bad previous", func(op *sourceindex.Operation) { op.Previous = "not a SHA" }},
		{"bad committed", func(op *sourceindex.Operation) { op.Token = "not a SHA" }},
		{"Applied receipt", func(op *sourceindex.Operation) { op.ReceiptDigest = sha256.Sum256([]byte("receipt")) }},
		{"bad retention", func(op *sourceindex.Operation) { op.RetentionRoot = "not a SHA" }},
		{"Acknowledged no receipt", func(op *sourceindex.Operation) { op.State = gitobj.SourceOperationAcknowledged; op.RetentionRoot = "" }},
		{"Acknowledged retained payload", func(op *sourceindex.Operation) {
			op.State = gitobj.SourceOperationAcknowledged
			op.ReceiptDigest = sha256.Sum256([]byte("receipt"))
		}},
		{"Forgotten retained revisions", func(op *sourceindex.Operation) {
			op.State = gitobj.SourceOperationForgotten
			op.ReceiptDigest = sha256.Sum256([]byte("receipt"))
		}},
	}
	if err := validateManagedSourceOperation(id, valid); err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := valid
			test.change(&operation)
			if err := validateManagedSourceOperation(id, operation); err == nil {
				t.Fatal("invalid proof accepted")
			}
			s := &Store{}
			if _, err := s.writeManagedSourceProof(t.Context(), id, operation); err == nil {
				t.Fatal("invalid proof reached object writer")
			}
		})
	}
	acknowledged := valid
	acknowledged.State = gitobj.SourceOperationAcknowledged
	acknowledged.RetentionRoot = ""
	acknowledged.ReceiptDigest = sha256.Sum256([]byte("receipt"))
	if err := validateManagedSourceOperation(id, acknowledged); err != nil {
		t.Fatal(err)
	}
	forgotten := acknowledged
	forgotten.State = gitobj.SourceOperationForgotten
	forgotten.Previous = ""
	forgotten.Token = ""
	forgotten.Result = ""
	if err := validateManagedSourceOperation(id, forgotten); err != nil {
		t.Fatal(err)
	}
}

func TestManagedProofImportRejectsInvalidReceiptAndNoncanonicalBody(t *testing.T) {
	s := initStore(t)
	id := strings.Repeat("b", 64)
	valid := sourceindex.Operation{Previous: model.SHA(strings.Repeat("1", 40)), Token: model.SHA(strings.Repeat("2", 40)), Result: "valid", RequestDigest: sha256.Sum256([]byte("request")), State: gitobj.SourceOperationApplied}
	for _, test := range []struct {
		name   string
		change func(*sourceindex.Operation)
		suffix string
	}{{name: "zero digest", change: func(op *sourceindex.Operation) { op.RequestDigest = [32]byte{} }}, {name: "newline result", change: func(op *sourceindex.Operation) { op.Result = "bad\nresult" }}, {name: "unknown state", change: func(op *sourceindex.Operation) { op.State = 9 }}, {name: "noncanonical framing", suffix: "\n"}} {
		t.Run(test.name, func(t *testing.T) {
			operation := valid
			if test.change != nil {
				test.change(&operation)
			}
			body := struct {
				Format      string
				OperationID string
				Operation   sourceindex.Operation
			}{"cc-notes-source-operation-v2", id, operation}
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte(test.suffix)...)
			var sha model.SHA
			if err := s.Repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
				blob, err := objects.WriteBlob(data)
				if err != nil {
					return err
				}
				tree, err := objects.WriteTree([]object.TreeEntry{{Name: "source-operation-v2", Mode: filemode.Regular, Hash: plumbing.NewHash(string(blob))}})
				if err != nil {
					return err
				}
				identity := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(0, 0)}
				sha, err = objects.WriteCommit(gitobj.SourceCommit{Tree: tree, Parents: []model.SHA{valid.Token}, Author: identity, Committer: identity, Message: "cc-notes: source operation v2"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, modern, err := s.readManagedSourceProof(t.Context(), sha, id); err == nil || !modern {
				t.Fatalf("invalid imported proof modern=%v err=%v", modern, err)
			}
		})
	}
}
