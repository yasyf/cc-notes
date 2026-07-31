package fusefs

import (
	"errors"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/model"
)

// TestCodecsExhaustive asserts every model.Kind has a codec keyed by itself and
// that codecs holds no extra entries: a new kind without a codec fails here.
func TestCodecsExhaustive(t *testing.T) {
	kinds := model.Kinds()
	if len(codecs) != len(kinds) {
		t.Fatalf("codecs has %d entries, want %d kinds", len(codecs), len(kinds))
	}
	for _, kind := range kinds {
		if got := codecOf(kind).Kind(); got != kind {
			t.Errorf("codecOf(%s).Kind() = %s, want %s", kind, got, kind)
		}
	}
}

// readOnlyKinds names the kinds projected immutable, stated here rather than
// read back off the codec table so a flipped readOnly flag fails the contract
// test instead of moving its expectations with it.
var readOnlyKinds = map[model.Kind]bool{
	model.KindRunbook:       true,
	model.KindInvestigation: true,
	model.KindPlan:          true,
}

// TestCodecReadOnlyGovernsProjectedModeAndWriteRejection pins both things the
// readOnly flag decides, for every kind at once: the mode the projection gives
// the kind's files, and whether the driver admits their locators on the create
// and the revise/delete path. A read-only codec wires no parse func, so a write
// that reached codec.Diff or codec.New would call a nil func inside the signed
// helper daemon — the flag is the only thing standing between the two.
func TestCodecReadOnlyGovernsProjectedModeAndWriteRejection(t *testing.T) {
	source := authorityStore(t)
	ids := make(map[model.Kind]model.EntityID, len(model.Kinds()))
	for _, kind := range model.Kinds() {
		ids[kind] = createSnapshot(t, source, createOpFor(kind)).EntityID()
	}
	snapshot, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatalf("BuildAuthoritySnapshot: %v", err)
	}
	objects := indexAuthorityObjects(snapshot.objects)
	tenant := testTenant(t)

	for _, kind := range model.Kinds() {
		t.Run(string(kind), func(t *testing.T) {
			readOnly := readOnlyKinds[kind]
			if got := codecOf(kind).ReadOnly(); got != readOnly {
				t.Errorf("codecOf(%s).ReadOnly() = %v, want %v", kind, got, readOnly)
			}
			key := sourceKeyForEntity(t, source, kind, ids[kind])

			wantMode := uint32(0o644)
			if readOnly {
				wantMode = 0o444
			}
			if got := objects[key].mode; got != wantMode {
				t.Errorf("%s file mode = %#o, want %#o", kind, got, wantMode)
			}

			entity := sourceLocator(tenant, key, 1)
			gotKind, _, _, err := sourceEntityLocator(&entity)
			switch {
			case readOnly && !errors.Is(err, sourcedriver.ErrInvalidValue):
				t.Errorf("revise/delete locator for read-only %s: err = %v, want ErrInvalidValue", kind, err)
			case !readOnly && (err != nil || gotKind != kind):
				t.Errorf("revise/delete locator for %s = (%s, %v), want (%s, nil)", kind, gotKind, err, kind)
			}

			parent := sourceLocator(tenant, "kind:"+string(kind), 1)
			create := catalog.SourceMutationContext{
				Operation: writableFileOperation(catalog.MutationCreate, "new"+layouts[kind].ext, true),
				Parent:    &parent,
			}
			gotKind, err = driverMutationKind(create)
			switch {
			case readOnly && !errors.Is(err, sourcedriver.ErrInvalidValue):
				t.Errorf("create under read-only %s: err = %v, want ErrInvalidValue", kind, err)
			case !readOnly && (err != nil || gotKind != kind):
				t.Errorf("create under %s = (%s, %v), want (%s, nil)", kind, gotKind, err, kind)
			}
		})
	}
}

// createOpFor returns a minimal valid create op for kind, panicking on a kind
// it does not know so a newly registered kind fails loudly here.
func createOpFor(kind model.Kind) model.Op {
	nonce := model.NewNonce()
	switch kind {
	case model.KindNote:
		return model.CreateNote{Nonce: nonce, Title: "note", Body: "body"}
	case model.KindDoc:
		return model.CreateDoc{Nonce: nonce, Title: "doc", Body: "body", When: "always"}
	case model.KindLog:
		return model.CreateLog{Nonce: nonce, Title: "log"}
	case model.KindTask:
		return model.CreateTask{Nonce: nonce, Title: "task", Type: model.TypeTask, Branch: "main"}
	case model.KindSprint:
		return model.CreateSprint{Nonce: nonce, Title: "sprint"}
	case model.KindProject:
		return model.CreateProject{Nonce: nonce, Title: "project"}
	case model.KindRunbook:
		return model.CreateRunbook{Nonce: nonce, Title: "runbook", Description: "description"}
	case model.KindInvestigation:
		return model.CreateInvestigation{Nonce: nonce, Title: "investigation", Premise: "premise"}
	case model.KindPlan:
		return model.CreatePlan{Nonce: nonce, Title: "plan", Body: "body", Status: model.PlanDraft}
	}
	panic("fusefs: no create op for kind " + string(kind))
}
