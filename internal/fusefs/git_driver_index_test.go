//go:build darwin

package fusefs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func TestGitDriverIndexedProjectionMatchesAuthorityOracle(t *testing.T) {
	driver, tenant, source := newGitDriverTest(t)
	operations := []model.Op{
		model.CreateNote{Nonce: model.NewNonce(), Title: "Note", Body: "body\n"},
		model.CreateDoc{Nonce: model.NewNonce(), Title: "Doc", Body: "body", When: "when"},
		model.CreateLog{Nonce: model.NewNonce(), Title: "Log"},
		model.CreateTask{Nonce: model.NewNonce(), Title: "Task", Type: model.TypeTask, Branch: "main"},
		model.CreateSprint{Nonce: model.NewNonce(), Title: "Sprint"},
		model.CreateProject{Nonce: model.NewNonce(), Title: "Project"},
		model.CreateRunbook{Nonce: model.NewNonce(), Title: "Runbook"},
		model.CreateInvestigation{Nonce: model.NewNonce(), Title: "Investigation", Premise: "premise"},
		model.CreatePlan{Nonce: model.NewNonce(), Title: "Plan"},
		model.CreateAnswer{Nonce: model.NewNonce(), Title: "Answer", Body: "answer"},
		model.CreateLedger{Nonce: model.NewNonce(), Title: "Ledger", Columns: []string{"state"}},
	}
	created := make(map[model.Kind]model.Snapshot)
	for _, operation := range operations {
		snapshot := createSnapshot(t, source, operation)
		created[snapshot.Meta().Kind] = snapshot
	}
	if len(created) != len(model.Kinds()) {
		t.Fatalf("oracle fixture covers %d kinds, want %d", len(created), len(model.Kinds()))
	}
	attachmentBody := []byte("immutable attachment bytes\n")
	attachmentPath := filepath.Join(t.TempDir(), "proof.bin")
	if err := os.WriteFile(attachmentPath, attachmentBody, 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, _, err := source.AttachFile(t.Context(), attachmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Append(t.Context(), refs.For(model.KindNote, created[model.KindNote].EntityID()), []model.Op{model.AddAttachment(attachment)}); err != nil {
		t.Fatal(err)
	}
	answer := created[model.KindAnswer].(model.Answer)
	partial, err := source.Repo.WriteOpsCommit(t.Context(), []model.SHA{answer.Head}, gitobj.Signature{Name: "Oracle", Email: "oracle@example.com", When: time.Unix(1, 0)}, "partial answer", model.Pack{Lamport: 2, Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.PublishRef(t.Context(), refs.For(model.KindAnswer, answer.ID), partial, answer.Head); err != nil {
		t.Fatal(err)
	}
	loaded, err := source.LoadAt(t.Context(), partial)
	if err != nil || loaded.Meta().SkippedOps != 1 {
		t.Fatalf("partial fixture=%+v err=%v", loaded, err)
	}
	beforeToken, before := indexedAuthorityOracle(t, driver, tenant, source)
	task := created[model.KindTask].(model.Task)
	sprint := created[model.KindSprint].(model.Sprint)
	project := created[model.KindProject].(model.Project)
	for _, change := range []struct {
		kind model.Kind
		id   model.EntityID
		ops  []model.Op
	}{
		{model.KindTask, task.ID, []model.Op{model.SetSprint{Sprint: sprint.ID}, model.SetProject{Project: project.ID}}},
		{model.KindSprint, sprint.ID, []model.Op{model.SetProject{Project: project.ID}}},
		{model.KindTask, task.ID, []model.Op{model.SetProject{}}},
		{model.KindTask, task.ID, []model.Op{model.SetTitle{Title: "Renamed"}}},
	} {
		if _, err := source.Append(t.Context(), refs.For(change.kind, change.id), change.ops); err != nil {
			t.Fatal(err)
		}
		afterToken, after := indexedAuthorityOracle(t, driver, tenant, source)
		indexedDeltaOracle(t, driver, tenant, beforeToken, afterToken, before, after)
		beforeToken, before = afterToken, after
	}
	ref := refs.For(model.KindProject, project.ID)
	tip, err := source.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.PublishRef(t.Context(), ref, "", tip); err != nil {
		t.Fatal(err)
	}
	indexedAuthorityOracle(t, driver, tenant, source)
	if err := source.PublishRef(t.Context(), ref, tip, ""); err != nil {
		t.Fatal(err)
	}
	indexedAuthorityOracle(t, driver, tenant, source)
}

func indexedAuthorityOracle(t *testing.T, driver *GitDriver, tenant Tenant, source *store.Store) (sourcedriver.RevisionToken, AuthoritySnapshot) {
	t.Helper()
	token := refreshDriver(t, driver, tenant.Authority).Revision
	manifest, err := (sourceindex.Index{Backend: source}).Snapshot(t.Context(), model.SHA(token))
	if err != nil {
		t.Fatal(err)
	}
	want, err := BuildAuthoritySnapshotAt(t.Context(), source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	ordered := slices.Clone(want.objects)
	slices.SortFunc(ordered, func(a, b authorityObject) int { return strings.Compare(a.key, b.key) })
	for start := 0; start < len(ordered); start += 3 {
		values, total, err := source.SourceObjectRange(t.Context(), model.SHA(token), uint64(start), 3)
		if err != nil || total != uint64(len(ordered)) || len(values) != min(3, len(ordered)-start) {
			t.Fatalf("range %d: count=%d values=%+v err=%v", start, total, values, err)
		}
		for offset, descriptor := range values {
			actual, err := indexedAuthorityObject(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			expected := ordered[start+offset]
			if expected.kind == catalog.KindFile {
				_, reader, err := source.OpenSourceContent(t.Context(), model.SHA(token), expected.key)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(reader)
				if err := errors.Join(readErr, reader.Close()); err != nil {
					t.Fatal(err)
				}
				if int64(len(body)) != expected.size || catalog.ContentHash(sha256.Sum256(body)) != expected.hash {
					t.Fatalf("content descriptor differs for %s", expected.key)
				}
				if expected.attachment == "" {
					actual.content = body
				}
			}
			if !sameAuthorityObject(actual, expected) {
				t.Fatalf("descriptor/body mismatch: got=%+v want=%+v", actual, expected)
			}
		}
	}
	for _, limit := range []int{1, 3, sourcedriver.MaxPageItems} {
		request := testDriverSnapshotRequest(token, nil, limit)
		start := 0
		for {
			actual, err := driver.Snapshot(t.Context(), tenant.Authority, request)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := snapshotPage(request, snapshotIndexForTest(want), []Tenant{tenant}, start)
			if err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("snapshot output differs at %d limit %d: got=%+v want=%+v err=%v", start, limit, actual, expected, err)
			}
			start += len(actual.Objects)
			if actual.Next == nil {
				break
			}
			request.Cursor = actual.Next
		}
	}
	return token, want
}

func indexedDeltaOracle(t *testing.T, driver *GitDriver, tenant Tenant, from, to sourcedriver.RevisionToken, before, after AuthoritySnapshot) {
	t.Helper()
	delta := changesForTest(DiffAuthoritySnapshots(before, after))
	request := testDriverChangesRequest(from, to, nil, 2)
	start := 0
	for {
		actual, err := driver.ChangesSince(t.Context(), tenant.Authority, request)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := changePage(request, delta, []Tenant{tenant}, start)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("change output differs at %d: got=%+v want=%+v err=%v", start, actual, expected, err)
		}
		start += len(actual.Changes)
		if actual.Next == nil {
			return
		}
		request.Cursor = actual.Next
	}
}

func TestIndexedAuthorityShortCollisionPartialAndErrors(t *testing.T) {
	firstID := model.EntityID("abcdef0" + strings.Repeat("1", 33))
	secondID := model.EntityID("abcdef0" + strings.Repeat("2", 33))
	oid := strings.Repeat("a", 64)
	for _, count := range []int{1, 2} {
		first := model.Note{ID: firstID, Title: "First", Body: "partial", SkippedOps: 3, Attachments: []model.Attachment{{Name: "proof.bin", OID: oid, Size: 8}}}
		second := model.Doc{ID: secondID, Title: "Second", Attachments: []model.Attachment{{Name: "other.bin", OID: oid, Size: 8}}}
		fixture := authorityEmissionFixture{entities: make(map[string]sourceview.Entity), members: make(map[string][]sourceview.Entity)}
		builder := projectionBuilder{snaps: map[model.Kind][]model.Snapshot{model.KindNote: {first}}}
		snapshots := []model.Snapshot{first}
		if count == 2 {
			builder.snaps[model.KindDoc] = []model.Snapshot{second}
			snapshots = append(snapshots, second)
		}
		for _, snapshot := range snapshots {
			entity, err := sourceview.Describe(string(snapshot.Meta().Kind), model.SHA(snapshot.EntityID()), model.PackCommit{SHA: model.SHA(snapshot.EntityID())}, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			fixture.entities[entity.Ref] = entity
			prefix := "attachment-short/" + entity.ID.Short() + "/"
			fixture.members[prefix] = append(fixture.members[prefix], entity)
			if entity.SkippedOps != snapshot.Meta().SkippedOps {
				t.Fatal("partial diagnostic changed")
			}
		}
		if err := builder.buildAttachments(); err != nil {
			t.Fatal(err)
		}
		actual := []authorityObject{{key: "attachments", name: "attachments", kind: catalog.KindDirectory, mode: 0o755}}
		for _, entity := range fixture.entities {
			objects, err := sourceview.Emit(entity, &fixture)
			if err != nil {
				t.Fatal(err)
			}
			for _, descriptor := range objects {
				if strings.HasPrefix(descriptor.Key, "attachment") {
					object, err := indexedAuthorityObject(descriptor)
					if err != nil {
						t.Fatal(err)
					}
					actual = append(actual, object)
				}
			}
		}
		slices.SortFunc(actual, func(a, b authorityObject) int { return strings.Compare(a.key, b.key) })
		slices.SortFunc(builder.objects, func(a, b authorityObject) int { return strings.Compare(a.key, b.key) })
		if !reflect.DeepEqual(actual, builder.objects) {
			t.Fatalf("Short7 group %d: got=%+v want=%+v", count, actual, builder.objects)
		}
	}
	if _, err := indexedAuthorityObject(sourceview.Object{Kind: 99}); !errors.Is(err, sourcedriver.ErrIntegrity) {
		t.Fatalf("unknown kind error=%v", err)
	}
	for _, attachment := range []model.Attachment{{Name: "bad.bin", OID: "bad", Size: 1}, {Name: "negative.bin", OID: oid, Size: -1}} {
		snapshot := model.Note{ID: firstID, Attachments: []model.Attachment{attachment}}
		builder := projectionBuilder{snaps: map[model.Kind][]model.Snapshot{model.KindNote: {snapshot}}}
		entity, err := sourceview.Describe("note", model.SHA(firstID), model.PackCommit{SHA: model.SHA(firstID)}, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		fixture := authorityEmissionFixture{members: map[string][]sourceview.Entity{"attachment-short/" + firstID.Short() + "/": {entity}}}
		_, emittedErr := sourceview.Emit(entity, &fixture)
		if builder.buildAttachments() == nil || emittedErr == nil {
			t.Fatalf("invalid attachment accepted: %+v emitter=%v", attachment, emittedErr)
		}
	}
}

type authorityEmissionFixture struct {
	entities map[string]sourceview.Entity
	members  map[string][]sourceview.Entity
}

func (f *authorityEmissionFixture) Entity(kind model.Kind, id model.EntityID) (sourceview.Entity, bool, error) {
	value, found := f.entities[refs.For(kind, id)]
	return value, found, nil
}

func (f *authorityEmissionFixture) Members(prefix string) ([]sourceview.Entity, error) {
	return f.members[prefix], nil
}

func TestSnapshotValuesBoundsBaseRangesAcrossTenantBoundary(t *testing.T) {
	tenant := testTenant(t)
	second := tenant
	second.ID = "cc-notes-second"
	var ranges [][2]int
	index := authoritySnapshotIndex{
		count: 10000,
		rangeObjects: func(start, limit int) ([]authorityObject, error) {
			ranges = append(ranges, [2]int{start, limit})
			objects := make([]authorityObject, limit)
			for offset := range objects {
				name := fmt.Sprintf("entry:%05d", start+offset)
				objects[offset] = authorityObject{key: name, name: name, kind: catalog.KindDirectory, mode: 0o755}
			}
			return objects, nil
		},
	}
	values, err := snapshotValues(&index, []Tenant{tenant, second}, sourcedriver.RevisionToken(strings.Repeat("a", 40)), 9998, 10005)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ranges, [][2]int{{9998, 2}, {0, 5}}) {
		t.Fatalf("page ranges=%v, want bounded tail and head", ranges)
	}
	for position, value := range values {
		absolute := 9998 + position
		wantTenant := tenant.ID
		if absolute >= 10000 {
			wantTenant = second.ID
		}
		if value.ID != sourcedriver.LogicalID(fmt.Sprintf("entry:%05d", absolute%10000)) || value.Tenant != wantTenant {
			t.Fatalf("projection %d=%+v", position, value)
		}
	}
}
