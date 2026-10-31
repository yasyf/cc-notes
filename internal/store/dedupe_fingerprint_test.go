package store

import (
	"reflect"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func checkDuplicateFingerprint[S model.Snapshot](t *testing.T, zero S, fields []string, same func(S, S) bool) {
	t.Helper()
	original := duplicateFingerprint(zero)
	value := reflect.ValueOf(zero)
	for _, name := range fields {
		t.Run(name, func(t *testing.T) {
			changed := reflect.New(value.Type()).Elem()
			changed.Set(value)
			setDuplicateField(t, changed.FieldByName(name))
			other := changed.Interface().(S)
			if same(zero, other) || duplicateFingerprint(other) == original {
				t.Fatalf("content field %s was omitted from comparator or fingerprint", name)
			}
		})
	}
	for i := range value.NumField() {
		field := value.Type().Field(i)
		if slices.Contains(fields, field.Name) {
			continue
		}
		changed := reflect.New(value.Type()).Elem()
		changed.Set(value)
		setDuplicateField(t, changed.Field(i))
		other := changed.Interface().(S)
		if !same(zero, other) || duplicateFingerprint(other) != original {
			t.Fatalf("ignored field %s changed the content fingerprint", field.Name)
		}
	}
	empty := reflect.New(value.Type()).Elem()
	empty.Set(value)
	for i := range empty.NumField() {
		if empty.Field(i).Kind() == reflect.Slice {
			empty.Field(i).Set(reflect.MakeSlice(empty.Field(i).Type(), 0, 0))
		}
	}
	if !same(zero, empty.Interface().(S)) || duplicateFingerprint(empty.Interface().(S)) != original {
		t.Fatal("nil and empty collections have different content fingerprints")
	}
}

func setDuplicateField(t *testing.T, value reflect.Value) {
	t.Helper()
	switch value.Kind() {
	case reflect.String:
		value.SetString("different")
	case reflect.Int, reflect.Int64:
		value.SetInt(1)
	case reflect.Uint, reflect.Uint64:
		value.SetUint(1)
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Slice:
		value.Set(reflect.MakeSlice(value.Type(), 1, 1))
		setDuplicateField(t, value.Index(0))
	case reflect.Map:
		value.Set(reflect.MakeMap(value.Type()))
	case reflect.Struct:
		for i := range value.NumField() {
			if value.Field(i).CanSet() {
				setDuplicateField(t, value.Field(i))
			}
		}
	default:
		t.Fatalf("unsupported duplicate test field %s", value.Type())
	}
}

func TestDuplicateFingerprintAllComparedFields(t *testing.T) {
	t.Run("note", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Note{}, []string{"Title", "Body", "Tags", "Anchors", "Attachments"}, sameNoteContent)
	})
	t.Run("doc", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Doc{}, []string{"Title", "Body", "When", "Tags", "Anchors", "Attachments"}, sameDocContent)
	})
	t.Run("answer", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Answer{}, []string{"Title", "Body", "Tags", "Anchors", "Attachments"}, sameAnswerContent)
	})
	t.Run("log", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Log{}, []string{"Title", "Tags", "Anchors", "Attachments"}, sameLogContent)
	})
	t.Run("task", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Task{}, []string{"Branch", "Title", "Description", "Type", "Priority", "Parent", "Sprint", "Project", "Plan", "Labels", "BlockedBy", "Anchors", "Criteria"}, sameTaskContent)
	})
	t.Run("sprint", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Sprint{}, []string{"Project", "Title", "Description", "StartDate", "EndDate", "Labels"}, sameSprintContent)
	})
	t.Run("project", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Project{}, []string{"Title", "Description", "Labels"}, sameProjectContent)
	})
	t.Run("runbook", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Runbook{}, []string{"Title", "Description", "Labels", "Anchors", "Steps"}, sameRunbookContent)
	})
	t.Run("investigation", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Investigation{}, []string{"Title", "Premise", "Tags", "Anchors", "Attachments"}, sameInvestigationContent)
	})
	t.Run("plan", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Plan{}, []string{"Title", "Body", "Status", "Labels", "Anchors"}, samePlanContent)
	})
	t.Run("ledger", func(t *testing.T) {
		checkDuplicateFingerprint(t, model.Ledger{}, []string{"Title", "Description", "Columns", "Labels", "Anchors"}, sameLedgerContent)
	})
}

func TestDuplicateFingerprintIgnoresCriterionAndStepIdentity(t *testing.T) {
	a := model.Task{Criteria: []model.Criterion{{Text: "check", Script: "true"}}}
	b := a
	b.Criteria = slices.Clone(a.Criteria)
	b.Criteria[0].ID = model.NewNonce()
	b.Criteria[0].Status = model.CriterionStatus("changed")
	b.Criteria[0].Note = "changed"
	if !sameTaskContent(a, b) || duplicateFingerprint(a) != duplicateFingerprint(b) {
		t.Fatal("criterion identity changed the fingerprint")
	}
	x := model.Runbook{Steps: []model.RunbookStep{{Text: "check", Command: "true"}}}
	y := x
	y.Steps = slices.Clone(x.Steps)
	y.Steps[0].ID = model.NewNonce()
	y.Steps[0].Position = "changed"
	if !sameRunbookContent(x, y) || duplicateFingerprint(x) != duplicateFingerprint(y) {
		t.Fatal("step identity changed the fingerprint")
	}
}
