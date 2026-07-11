package fold_test

import (
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// alienCreate is a CreateOp whose kind has no registered folder. Only a
// hand-built PackCommit can carry it: the pack codec's kind gate rejects
// unregistered ops before they ever decode.
type alienCreate struct{}

func (alienCreate) OpKind() string         { return "create_alien" }
func (alienCreate) CreateKind() model.Kind { return model.Kind("alien") }

// TestDispatchUnregisteredKind pins the dispatch nil-folder guard: a CreateOp
// whose kind is not in the folders table must yield ErrNoCreate, never a panic
// from calling a nil fold func.
func TestDispatchUnregisteredKind(t *testing.T) {
	chain := []model.PackCommit{mk("aaa", nil, "alice", 100, 1, alienCreate{})}
	if _, err := fold.Fold(chain); !errors.Is(err, fold.ErrNoCreate) {
		t.Fatalf("Fold error = %v, want ErrNoCreate", err)
	}
	if _, err := fold.History(chain); !errors.Is(err, fold.ErrNoCreate) {
		t.Fatalf("History error = %v, want ErrNoCreate", err)
	}
}

// TestDispatchPointerCreateOp pins a deliberate error-class change from the
// pre-refactor code. A *CreateTask first op satisfies the CreateOp interface —
// value-receiver methods promote to the pointer — so dispatch routes it to the
// task folder, whose create asserts the exact value type and fails with
// ErrKindMismatch. The old type-switch matched neither the value create cases
// nor the sibling cases and returned ErrNoCreate. This class change is accepted
// for this internal package (real packs never carry pointer ops); the pin keeps
// it visible.
func TestDispatchPointerCreateOp(t *testing.T) {
	chain := []model.PackCommit{mk("aaa", nil, "alice", 100, 1, &model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"})}
	if _, err := fold.Fold(chain); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("Fold error = %v, want ErrKindMismatch", err)
	}
	if _, err := fold.History(chain); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("History error = %v, want ErrKindMismatch", err)
	}
}
