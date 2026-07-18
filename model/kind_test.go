package model

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestKindsCanonicalOrder(t *testing.T) {
	want := []Kind{KindNote, KindDoc, KindLog, KindTask, KindSprint, KindProject, KindRunbook, KindInvestigation}
	if got := Kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
}

func TestKindsFreshSlice(t *testing.T) {
	a := Kinds()
	a[0] = "corrupted"
	if got := Kinds()[0]; got != KindNote {
		t.Fatalf("Kinds() shares its backing array: second call yields %q, want %q", got, KindNote)
	}
}

func TestParseKind(t *testing.T) {
	for _, k := range Kinds() {
		got, err := ParseKind(string(k))
		if err != nil {
			t.Errorf("ParseKind(%q) error: %v", k, err)
			continue
		}
		if got != k {
			t.Errorf("ParseKind(%q) = %q, want %q", k, got, k)
		}
	}
	for _, bad := range []string{"", "notes", "Task", "RUNBOOK", "sprints", "xyz", " note"} {
		if _, err := ParseKind(bad); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("ParseKind(%q) error = %v, want ErrInvalidValue", bad, err)
		}
	}
}

func TestKindZero(t *testing.T) {
	for _, k := range Kinds() {
		if got := k.Zero().Meta().Kind; got != k {
			t.Errorf("%s.Zero().Meta().Kind = %s, want %s", k, got, k)
		}
	}
}

func TestKindDecodeSnapshotRoundTrip(t *testing.T) {
	for _, tc := range metaCases {
		k := tc.want.Kind
		data, err := json.Marshal(tc.snap)
		if err != nil {
			t.Fatalf("marshal %s snapshot: %v", k, err)
		}
		got, err := k.DecodeSnapshot(data)
		if err != nil {
			t.Fatalf("%s.DecodeSnapshot: %v", k, err)
		}
		if !reflect.DeepEqual(got, tc.snap) {
			t.Errorf("%s.DecodeSnapshot round-trip = %#v, want %#v", k, got, tc.snap)
		}
	}
}

func TestKindDecodeSnapshotRejectsMalformed(t *testing.T) {
	for _, bad := range [][]byte{[]byte("{"), []byte("not json"), []byte("[1,2,3]"), nil} {
		if _, err := KindTask.DecodeSnapshot(bad); err == nil {
			t.Errorf("KindTask.DecodeSnapshot(%q) = nil error, want error", bad)
		}
	}
}

// metaCases holds one fully-populated snapshot per kind alongside its expected
// Meta. TestSnapshotMeta pins the field mappings; TestKindDecodeSnapshotRoundTrip
// reuses the snapshots for codec fidelity.
var metaCases = []struct {
	snap Snapshot
	want Meta
}{
	{
		snap: Note{
			ID: testID, Title: "Deploy runbook", Body: "ship from green main",
			Tags: []string{"ops"}, Author: "ada", CreatedAt: 100, UpdatedAt: 200,
			Deleted: true, SupersededBy: []EntityID{testParent}, Head: testParent,
			Attachments: []Attachment{{Name: "trace.png", OID: testOID, Size: 2048}},
		},
		want: Meta{
			Kind: KindNote, Title: "Deploy runbook", Head: testParent,
			CreatedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(200, 0).UTC(),
			Deleted: true, Superseded: true,
			Attachments: []Attachment{{Name: "trace.png", OID: testOID, Size: 2048}},
		},
	},
	{
		snap: Doc{
			ID: testID, Title: "Auth architecture", Body: "token refresh loop",
			When: "before touching auth", Author: "ada", CreatedAt: 300, UpdatedAt: 400,
			Head:        testParent,
			Attachments: []Attachment{{Name: "diagram.png", OID: testOID, Size: 1024}},
		},
		want: Meta{
			Kind: KindDoc, Title: "Auth architecture", Head: testParent,
			CreatedAt: time.Unix(300, 0).UTC(), UpdatedAt: time.Unix(400, 0).UTC(),
			Deleted: false, Superseded: false,
			Attachments: []Attachment{{Name: "diagram.png", OID: testOID, Size: 1024}},
		},
	},
	{
		snap: Log{
			ID: testID, Title: "Auth rollout", Tags: []string{"ops"},
			Entries: []LogEntry{{Author: "ada", TS: 350, Text: "flipped to 5%"}},
			Author:  "ada", CreatedAt: 500, UpdatedAt: 600, Deleted: true, Head: testParent,
			Attachments: []Attachment{{Name: "run.log", OID: testOID, Size: 512}},
		},
		want: Meta{
			Kind: KindLog, Title: "Auth rollout", Head: testParent,
			CreatedAt: time.Unix(500, 0).UTC(), UpdatedAt: time.Unix(600, 0).UTC(),
			Deleted: true, Superseded: false,
			Attachments: []Attachment{{Name: "run.log", OID: testOID, Size: 512}},
		},
	},
	{
		snap: Task{
			ID: testID, Title: "Fix flaky sync", Description: "two-clone flake",
			Type: TypeBug, Status: StatusInProgress, Priority: 1, Assignee: "agent-1",
			CreatedAt: 700, UpdatedAt: 800, Head: testParent,
		},
		want: Meta{
			Kind: KindTask, Title: "Fix flaky sync", Head: testParent,
			CreatedAt: time.Unix(700, 0).UTC(), UpdatedAt: time.Unix(800, 0).UTC(),
		},
	},
	{
		snap: Sprint{
			ID: testID, Title: "Q3 hardening", Description: "stabilize sync",
			Status: SprintActive, Author: "ada", CreatedAt: 900, UpdatedAt: 1000, Head: testParent,
		},
		want: Meta{
			Kind: KindSprint, Title: "Q3 hardening", Head: testParent,
			CreatedAt: time.Unix(900, 0).UTC(), UpdatedAt: time.Unix(1000, 0).UTC(),
		},
	},
	{
		snap: Project{
			ID: testID, Title: "Platform", Description: "core infra",
			Status: ProjectActive, Author: "ada", CreatedAt: 1100, UpdatedAt: 1200, Head: testParent,
		},
		want: Meta{
			Kind: KindProject, Title: "Platform", Head: testParent,
			CreatedAt: time.Unix(1100, 0).UTC(), UpdatedAt: time.Unix(1200, 0).UTC(),
		},
	},
	{
		snap: Runbook{
			ID: testID, Title: "Deploy", Description: "ship from green main",
			Status: RunbookActive, Author: "ada", CreatedAt: 1300, UpdatedAt: 1400, Head: testParent,
		},
		want: Meta{
			Kind: KindRunbook, Title: "Deploy", Head: testParent,
			CreatedAt: time.Unix(1300, 0).UTC(), UpdatedAt: time.Unix(1400, 0).UTC(),
		},
	},
	{
		snap: Investigation{
			ID: testID, Title: "TestPool deadlock", Premise: "hangs after 3d55ae2e",
			Status: InvestigationRootCaused, Author: "ada", CreatedAt: 1500, UpdatedAt: 1600,
			Deleted: true, SupersededBy: []EntityID{testParent}, Head: testParent,
			Attachments: []Attachment{{Name: "stacks.txt", OID: testOID, Size: 4096}},
		},
		want: Meta{
			Kind: KindInvestigation, Title: "TestPool deadlock", Head: testParent,
			CreatedAt: time.Unix(1500, 0).UTC(), UpdatedAt: time.Unix(1600, 0).UTC(),
			Deleted: true, Superseded: true,
			Attachments: []Attachment{{Name: "stacks.txt", OID: testOID, Size: 4096}},
		},
	},
}

func TestSnapshotMeta(t *testing.T) {
	if got, want := len(metaCases), len(Kinds()); got != want {
		t.Fatalf("metaCases covers %d kinds, want %d", got, want)
	}
	seen := map[Kind]bool{}
	for _, tc := range metaCases {
		seen[tc.want.Kind] = true
		if got := tc.snap.Meta(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s Meta() = %+v, want %+v", tc.want.Kind, got, tc.want)
		}
	}
	for _, k := range Kinds() {
		if !seen[k] {
			t.Errorf("metaCases missing kind %s", k)
		}
	}
}

func TestCreateOpExhaustive(t *testing.T) {
	want := map[string]Kind{
		"create_note":          KindNote,
		"create_doc":           KindDoc,
		"create_log":           KindLog,
		"create_task":          KindTask,
		"create_sprint":        KindSprint,
		"create_project":       KindProject,
		"create_runbook":       KindRunbook,
		"create_investigation": KindInvestigation,
	}
	got := map[string]Kind{}
	for _, s := range everyOpSample {
		if co, ok := s.op.(CreateOp); ok {
			got[s.op.OpKind()] = co.CreateKind()
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreateOp implementors = %v, want %v", got, want)
	}
}
