package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const (
	testNonce  = "0123456789abcdef0123456789abcdef"
	testID     = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"
	testParent = "00112233445566778899aabbccddeeff00112233"
	testOID    = "e3b1c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type opSample struct {
	kind string
	op   Op
}

// everyOpSample holds one value of every op kind: the enumeration
// TestPackRoundTripEveryOpKind pins exhaustive against opDecoders and
// TestNoOpOrSnapshotHasCustomJSON reflects over.
var everyOpSample = []opSample{
	{"create_note", CreateNote{
		Nonce: testNonce,
		Title: "Deploy runbook",
		Body:  "Ship from green main only.",
		Tags:  []string{"ops", "ci"},
		Anchors: []Anchor{
			{Kind: AnchorCommit, Value: testID},
			{Kind: AnchorPath, Value: "docs/deploy.md"},
			{Kind: AnchorDir, Value: "internal/auth"},
			{Kind: AnchorBranch, Value: "main"},
		},
	}},
	{"set_title", SetTitle{Title: "New title"}},
	{"set_body", SetBody{Body: "New body"}},
	{"set_when", SetWhen{When: "before touching the auth flow"}},
	{"add_tag", AddTag{Tag: "urgent"}},
	{"remove_tag", RemoveTag{Tag: "stale"}},
	{"add_anchor", AddAnchor{Anchor: Anchor{Kind: AnchorPath, Value: "internal/model/pack.go"}}},
	{"remove_anchor", RemoveAnchor{Anchor: Anchor{Kind: AnchorBranch, Value: "main"}}},
	{"delete_note", DeleteNote{}},
	{"verify_note", VerifyNote{
		Witness: []AnchorWitness{
			{Anchor: Anchor{Kind: AnchorPath, Value: "docs/deploy.md"}, OID: "f1e2d3c4b5a60718293a4b5c6d7e8f90a1b2c3d4"},
			{Anchor: Anchor{Kind: AnchorCommit, Value: testID}, OID: testID},
		},
		VerifiedCommit: testParent,
	}},
	{"add_superseded_by", AddSupersededBy{ID: testID}},
	{"remove_superseded_by", RemoveSupersededBy{ID: testID}},
	{"mark_stale", MarkStale{Reason: "broken in prod"}},
	{"clear_stale", ClearStale{}},
	{"create_task", CreateTask{
		Nonce:       testNonce,
		Title:       "Fix flaky sync",
		Description: "Two-clone round-trip flakes",
		Type:        TypeBug,
		Priority:    0,
		Branch:      "feature/sync",
		Parent:      testParent,
		Labels:      []string{"ci", "sync"},
	}},
	{"set_description", SetDescription{Description: "Updated description"}},
	{"set_type", SetType{Type: TypeEpic}},
	{"set_priority", SetPriority{Priority: 3}},
	{"set_status", SetStatus{Status: StatusDone}},
	{"set_assignee", SetAssignee{Assignee: "agent-1"}},
	{"claim", Claim{Assignee: "agent-2"}},
	{"renew", Renew{}},
	{"reclaim", Reclaim{Assignee: "agent-2", From: "agent-1", AfterLamport: 5}},
	{"add_label", AddLabel{Label: "backend"}},
	{"remove_label", RemoveLabel{Label: "frontend"}},
	{"add_dep", AddDep{ID: testID}},
	{"remove_dep", RemoveDep{ID: testID}},
	{"link_commit", LinkCommit{SHA: testID}},
	{"unlink_commit", UnlinkCommit{SHA: testParent}},
	{"set_parent", SetParent{Parent: testParent}},
	{"add_comment", AddComment{Body: "Taking this one."}},
	{"set_branch", SetBranch{Branch: "feature/x"}},
	{"create_sprint", CreateSprint{
		Nonce:       testNonce,
		Title:       "Q3 sync hardening",
		Description: "Stabilize two-clone sync",
		Project:     testParent,
		Labels:      []string{"ops", "sync"},
	}},
	{"create_project", CreateProject{
		Nonce:       testNonce,
		Title:       "Platform",
		Description: "Core infrastructure",
		Labels:      []string{"infra"},
	}},
	{"create_doc", CreateDoc{
		Nonce: testNonce,
		Title: "Auth architecture",
		Body:  "How the token refresh loop works.",
		When:  "before touching the auth flow",
		Tags:  []string{"auth", "arch"},
		Anchors: []Anchor{
			{Kind: AnchorCommit, Value: testID},
			{Kind: AnchorPath, Value: "internal/auth/token.go"},
			{Kind: AnchorDir, Value: "internal/auth"},
			{Kind: AnchorBranch, Value: "main"},
		},
	}},
	{"create_log", CreateLog{
		Nonce: testNonce,
		Title: "Auth rollout",
		Tags:  []string{"ops", "auth"},
		Anchors: []Anchor{
			{Kind: AnchorCommit, Value: testID},
			{Kind: AnchorPath, Value: "internal/auth/token.go"},
			{Kind: AnchorDir, Value: "internal/auth"},
			{Kind: AnchorBranch, Value: "main"},
		},
	}},
	{"append_entry", AppendEntry{Text: "flipped to 5%", Model: "claude-opus-4-8"}},
	{"set_sprint", SetSprint{Sprint: testID}},
	{"set_project", SetProject{Project: testParent}},
	{"set_sprint_status", SetSprintStatus{Status: SprintActive}},
	{"set_project_status", SetProjectStatus{Status: ProjectArchived}},
	{"set_start_date", SetStartDate{Date: 1700000000}},
	{"set_end_date", SetEndDate{Date: 1701000000}},
	{"add_criterion", AddCriterion{ID: "crit-1", Text: "all tests pass", Script: "go test ./..."}},
	{"remove_criterion", RemoveCriterion{ID: "crit-1"}},
	{"set_criterion_text", SetCriterionText{ID: "crit-1", Text: "all tests pass under -race"}},
	{"set_criterion_status", SetCriterionStatus{ID: "crit-1", Status: CriterionMet}},
	{"set_criterion_script", SetCriterionScript{ID: "crit-1", Script: "make check"}},
	{"add_attachment", AddAttachment{Name: "trace.png", OID: testOID, Size: 2048}},
	{"remove_attachment", RemoveAttachment{Name: "trace.png"}},
	{"create_runbook", CreateRunbook{
		Nonce:       testNonce,
		Title:       "Deploy",
		Description: "Ship from green main only.",
		Labels:      []string{"deploy", "ops"},
	}},
	{"add_step", AddStep{ID: "step-1", Text: "run tests", Command: "go test ./...", Position: "i"}},
	{"remove_step", RemoveStep{ID: "step-1"}},
	{"set_step_text", SetStepText{ID: "step-1", Text: "run tests under -race"}},
	{"set_step_command", SetStepCommand{ID: "step-1", Command: "go test -race ./..."}},
	{"set_step_position", SetStepPosition{ID: "step-1", Position: "a"}},
	{"start_run", StartRun{ID: "run-1", Task: testID}},
	{"set_run_step_status", SetRunStepStatus{RunID: "run-1", StepID: "step-1", Status: StepDone, Note: "green"}},
	{"finish_run", FinishRun{ID: "run-1", Status: RunSucceeded}},
	{"set_runbook_status", SetRunbookStatus{Status: RunbookArchived}},
	{"checkpoint", Checkpoint{
		EntityID: testID,
		State: Note{
			ID: testID, Title: "Deploy runbook", Body: "Ship from green main only.",
			Tags: []string{"ops"}, Anchors: []Anchor{{Kind: AnchorCommit, Value: testID}},
			Author: "ada", CreatedAt: 100, UpdatedAt: 200,
			SupersededBy: []EntityID{}, Head: testParent,
		},
		CoversLamport: 5,
		CoversShas:    []SHA{testParent, testID},
	}},
}

func TestPackRoundTripEveryOpKind(t *testing.T) {
	cases := everyOpSample

	if got, want := len(cases), len(opDecoders); got != want {
		t.Errorf("round-trip table has %d kinds, opDecoders registry has %d; every op needs both", got, want)
	}
	covered := make(map[string]bool, len(cases))
	for _, tc := range cases {
		if covered[tc.kind] {
			t.Errorf("duplicate kind %q in round-trip table", tc.kind)
		}
		covered[tc.kind] = true
		if _, ok := opDecoders[tc.kind]; !ok {
			t.Errorf("kind %q has no opDecoders entry", tc.kind)
		}
	}
	for kind := range opDecoders {
		if !covered[kind] {
			t.Errorf("registry kind %q not covered by the round-trip table", kind)
		}
	}

	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if got := tc.op.OpKind(); got != tc.kind {
				t.Fatalf("OpKind() = %q, want %q", got, tc.kind)
			}
			pack := Pack{Lamport: 42, Ops: []Op{tc.op}}
			data, err := json.Marshal(pack)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			prefix := fmt.Sprintf(`{"v":1,"lamport":42,"ops":[{"kind":%q`, tc.kind)
			if !strings.HasPrefix(string(data), prefix) {
				t.Fatalf("marshal = %s, want prefix %s", data, prefix)
			}
			got, err := DecodePack(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got, pack) {
				t.Fatalf("round-trip = %#v, want %#v", got, pack)
			}
		})
	}
}

func TestPackRoundTripCheckpointStateWithAttachments(t *testing.T) {
	op := Checkpoint{
		EntityID: testID,
		State: Note{
			ID: testID, Title: "Deploy runbook", Tags: []string{}, Anchors: []Anchor{},
			Author: "ada", CreatedAt: 100, UpdatedAt: 200,
			SupersededBy: []EntityID{}, Head: testParent,
			Attachments: []Attachment{
				{Name: "diagram.svg", OID: testOID, Size: 512},
				{Name: "trace.png", OID: testOID, Size: 2048},
			},
		},
		CoversLamport: 5,
		CoversShas:    []SHA{testParent, testID},
	}
	pack := Pack{Lamport: 6, Ops: []Op{op}}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, pack) {
		t.Fatalf("round-trip = %#v, want %#v", got, pack)
	}
	cp, ok := got.Ops[0].(Checkpoint)
	if !ok {
		t.Fatalf("Ops[0] = %T, want Checkpoint", got.Ops[0])
	}
	state, ok := cp.State.(Note)
	if !ok {
		t.Fatalf("decoded State = %T, want Note", cp.State)
	}
	if !reflect.DeepEqual(state.Attachments, op.State.(Note).Attachments) {
		t.Fatalf("Attachments = %+v, want %+v", state.Attachments, op.State.(Note).Attachments)
	}
}

func TestPackRoundTripCheckpointDocState(t *testing.T) {
	op := Checkpoint{
		EntityID: testID,
		State: Doc{
			ID: testID, Title: "Auth architecture", Body: "How the token refresh loop works.",
			When: "before touching the auth flow", Tags: []string{"auth"},
			Anchors: []Anchor{{Kind: AnchorPath, Value: "internal/auth/token.go"}},
			Author:  "ada", CreatedAt: 100, UpdatedAt: 200,
			SupersededBy: []EntityID{}, Head: testParent,
		},
		CoversLamport: 5,
		CoversShas:    []SHA{testParent, testID},
	}
	pack := Pack{Lamport: 6, Ops: []Op{op}}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, pack) {
		t.Fatalf("round-trip = %#v, want %#v", got, pack)
	}
	cp, ok := got.Ops[0].(Checkpoint)
	if !ok {
		t.Fatalf("Ops[0] = %T, want Checkpoint", got.Ops[0])
	}
	if _, ok := cp.State.(Doc); !ok {
		t.Fatalf("decoded State = %T, want Doc", cp.State)
	}
}

func TestPackRoundTripCheckpointLogState(t *testing.T) {
	op := Checkpoint{
		EntityID: testID,
		State: Log{
			ID: testID, Title: "Auth rollout",
			Entries: []LogEntry{
				{Author: "ada", TS: 150, Text: "flipped to 5%"},
				{Author: "bob", TS: 250, Text: "flipped to 50%"},
			},
			Tags:    []string{"ops"},
			Anchors: []Anchor{{Kind: AnchorDir, Value: "internal/auth"}},
			Author:  "ada", CreatedAt: 100, UpdatedAt: 250, Head: testParent,
		},
		CoversLamport: 5,
		CoversShas:    []SHA{testParent, testID},
	}
	pack := Pack{Lamport: 6, Ops: []Op{op}}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, pack) {
		t.Fatalf("round-trip = %#v, want %#v", got, pack)
	}
	cp, ok := got.Ops[0].(Checkpoint)
	if !ok {
		t.Fatalf("Ops[0] = %T, want Checkpoint", got.Ops[0])
	}
	if _, ok := cp.State.(Log); !ok {
		t.Fatalf("decoded State = %T, want Log", cp.State)
	}
}

func TestPackRoundTripCheckpointTaskState(t *testing.T) {
	op := Checkpoint{
		EntityID: testID,
		State: Task{
			ID: testID, Branch: "main", Title: "Fix flaky sync", Description: "round-trip flakes",
			Type: TypeBug, Status: StatusInProgress, Priority: 1, Assignee: "agent-7",
			HeartbeatAt: 300, HeartbeatLamport: 4, Labels: []string{"ci"}, BlockedBy: []EntityID{},
			Comments: []Comment{{Author: "agent-7", TS: 300, Body: "on it"}}, CreatedAt: 100,
			UpdatedAt: 300, StartedAt: 200, Commits: []SHA{}, Head: testParent,
		},
		CoversLamport: 7,
		CoversShas:    []SHA{testParent, testID},
	}
	pack := Pack{Lamport: 8, Ops: []Op{op}}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, pack) {
		t.Fatalf("round-trip = %#v, want %#v", got, pack)
	}
	cp, ok := got.Ops[0].(Checkpoint)
	if !ok {
		t.Fatalf("Ops[0] = %T, want Checkpoint", got.Ops[0])
	}
	if _, ok := cp.State.(Task); !ok {
		t.Fatalf("decoded State = %T, want Task", cp.State)
	}
}

func TestPackRoundTripCheckpointSprintState(t *testing.T) {
	op := Checkpoint{
		EntityID: testID,
		State: Sprint{
			ID: testID, Project: testParent, Title: "Q3 sync hardening", Description: "Stabilize sync",
			Status: SprintActive, StartDate: 1700000000, EndDate: 1701000000,
			Labels: []string{"ops"}, Commits: []SHA{}, Comments: []Comment{{Author: "agent-7", TS: 300, Body: "kickoff"}},
			Author: "ada", CreatedAt: 100, UpdatedAt: 300, StartedAt: 200, ClosedAt: 0, Head: testParent,
		},
		CoversLamport: 7,
		CoversShas:    []SHA{testParent, testID},
	}
	pack := Pack{Lamport: 8, Ops: []Op{op}}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, pack) {
		t.Fatalf("round-trip = %#v, want %#v", got, pack)
	}
	cp, ok := got.Ops[0].(Checkpoint)
	if !ok {
		t.Fatalf("Ops[0] = %T, want Checkpoint", got.Ops[0])
	}
	if _, ok := cp.State.(Sprint); !ok {
		t.Fatalf("decoded State = %T, want Sprint", cp.State)
	}
}

func TestPackRoundTripCheckpointProjectState(t *testing.T) {
	op := Checkpoint{
		EntityID: testID,
		State: Project{
			ID: testID, Title: "Platform", Description: "Core infrastructure", Status: ProjectActive,
			Labels: []string{"infra"}, Commits: []SHA{}, Comments: []Comment{{Author: "ada", TS: 100, Body: "init"}},
			Author: "ada", CreatedAt: 100, UpdatedAt: 200, ClosedAt: 0, Head: testParent,
		},
		CoversLamport: 4,
		CoversShas:    []SHA{testParent, testID},
	}
	pack := Pack{Lamport: 5, Ops: []Op{op}}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, pack) {
		t.Fatalf("round-trip = %#v, want %#v", got, pack)
	}
	cp, ok := got.Ops[0].(Checkpoint)
	if !ok {
		t.Fatalf("Ops[0] = %T, want Checkpoint", got.Ops[0])
	}
	if _, ok := cp.State.(Project); !ok {
		t.Fatalf("decoded State = %T, want Project", cp.State)
	}
}

func TestPackRoundTripCheckpointRunbookState(t *testing.T) {
	op := Checkpoint{
		EntityID: testID,
		State: Runbook{
			ID: testID, Title: "Deploy", Description: "Ship from green main only.",
			Status: RunbookActive,
			Steps: []RunbookStep{
				{ID: "step-1", Text: "run tests", Command: "go test ./...", Position: "a"},
				{ID: "step-2", Text: "tag release", Command: "", Position: "i"},
			},
			Runs: []RunbookRun{
				{
					ID: "run-1", Task: testParent, Status: RunSucceeded,
					Runner: "agent-7", StartedAt: 200, FinishedAt: 300,
					Results: []RunbookStepResult{
						{StepID: "step-1", Status: StepDone, Note: "green", Actor: "agent-7", TS: 250},
					},
				},
			},
			Labels: []string{"deploy"}, Comments: []Comment{{Author: "ada", TS: 100, Body: "init"}},
			Author: "ada", CreatedAt: 100, UpdatedAt: 300, ArchivedAt: 0, Head: testParent,
		},
		CoversLamport: 6,
		CoversShas:    []SHA{testParent, testID},
	}
	pack := Pack{Lamport: 7, Ops: []Op{op}}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, pack) {
		t.Fatalf("round-trip = %#v, want %#v", got, pack)
	}
	cp, ok := got.Ops[0].(Checkpoint)
	if !ok {
		t.Fatalf("Ops[0] = %T, want Checkpoint", got.Ops[0])
	}
	if _, ok := cp.State.(Runbook); !ok {
		t.Fatalf("decoded State = %T, want Runbook", cp.State)
	}
}

func TestPackGoldenBytes(t *testing.T) {
	cases := []struct {
		name string
		pack Pack
		want string
	}{
		{
			name: "task lifecycle pack",
			pack: Pack{
				Lamport: 7,
				Ops: []Op{
					CreateTask{
						Nonce:       testNonce,
						Title:       "Fix flaky sync",
						Description: "Two-clone round-trip flakes",
						Type:        TypeBug,
						Priority:    1,
						Branch:      "main",
						Parent:      "",
						Labels:      []string{"ci", "sync"},
					},
					Claim{Assignee: "agent-7"},
					SetStatus{Status: StatusInProgress},
				},
			},
			want: `{"v":1,"lamport":7,"ops":[{"kind":"create_task","nonce":"0123456789abcdef0123456789abcdef","title":"Fix flaky sync","description":"Two-clone round-trip flakes","type":"bug","priority":1,"branch":"main","parent":"","labels":["ci","sync"]},{"kind":"claim","assignee":"agent-7"},{"kind":"set_status","status":"in_progress"}]}`,
		},
		{
			name: "coordination pack",
			pack: Pack{
				Lamport: 5,
				Ops: []Op{
					Claim{Assignee: "agent-2"},
					Renew{},
					Reclaim{Assignee: "agent-2", From: "agent-1", AfterLamport: 5},
					LinkCommit{SHA: testID},
				},
			},
			want: `{"v":1,"lamport":5,"ops":[{"kind":"claim","assignee":"agent-2"},{"kind":"renew"},{"kind":"reclaim","assignee":"agent-2","from":"agent-1","after_lamport":5},{"kind":"link_commit","sha":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
		},
		{
			name: "session pack",
			pack: Pack{
				Lamport: 7,
				Session: "0b5c9b3a-7e2f-4c1d-9a8b-2f3e4d5c6b7a",
				Ops:     []Op{AddTag{Tag: "audit"}},
			},
			want: `{"v":1,"lamport":7,"session":"0b5c9b3a-7e2f-4c1d-9a8b-2f3e4d5c6b7a","ops":[{"kind":"add_tag","tag":"audit"}]}`,
		},
		{
			name: "note pack with anchors",
			pack: Pack{
				Lamport: 1,
				Ops: []Op{
					CreateNote{
						Nonce: "ffffffffffffffffffffffffffffffff",
						Title: "Deploy runbook",
						Body:  "Ship from green main only.",
						Tags:  []string{"ops"},
						Anchors: []Anchor{
							{Kind: AnchorCommit, Value: testID},
							{Kind: AnchorPath, Value: "docs/deploy.md"},
						},
					},
					AddTag{Tag: "runbook"},
				},
			},
			want: `{"v":1,"lamport":1,"ops":[{"kind":"create_note","nonce":"ffffffffffffffffffffffffffffffff","title":"Deploy runbook","body":"Ship from green main only.","tags":["ops"],"anchors":[{"kind":"commit","value":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"},{"kind":"path","value":"docs/deploy.md"}]},{"kind":"add_tag","tag":"runbook"}]}`,
		},
		{
			name: "log pack with anchors and entry",
			pack: Pack{
				Lamport: 2,
				Ops: []Op{
					CreateLog{
						Nonce: "ffffffffffffffffffffffffffffffff",
						Title: "Auth rollout",
						Tags:  []string{"ops"},
						Anchors: []Anchor{
							{Kind: AnchorCommit, Value: testID},
							{Kind: AnchorDir, Value: "internal/auth"},
						},
					},
					AppendEntry{Text: "flipped to 5%"},
				},
			},
			want: `{"v":1,"lamport":2,"ops":[{"kind":"create_log","nonce":"ffffffffffffffffffffffffffffffff","title":"Auth rollout","tags":["ops"],"anchors":[{"kind":"commit","value":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"},{"kind":"dir","value":"internal/auth"}]},{"kind":"append_entry","text":"flipped to 5%"}]}`,
		},
		{
			name: "note hygiene pack",
			pack: Pack{
				Lamport: 4,
				Ops: []Op{
					VerifyNote{
						Witness: []AnchorWitness{
							{Anchor: Anchor{Kind: AnchorPath, Value: "docs/deploy.md"}, OID: "f1e2d3c4b5a60718293a4b5c6d7e8f90a1b2c3d4"},
						},
						VerifiedCommit: testID,
					},
					AddSupersededBy{ID: testParent},
				},
			},
			want: `{"v":1,"lamport":4,"ops":[{"kind":"verify_note","witness":[{"anchor":{"kind":"path","value":"docs/deploy.md"},"oid":"f1e2d3c4b5a60718293a4b5c6d7e8f90a1b2c3d4"}],"verified_commit":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"},{"kind":"add_superseded_by","id":"00112233445566778899aabbccddeeff00112233"}]}`,
		},
		{
			name: "empty merge pack",
			pack: Pack{Lamport: 9},
			want: `{"v":1,"lamport":9,"ops":[]}`,
		},
		{
			name: "checkpoint over a note",
			pack: Pack{
				Lamport: 6,
				Ops: []Op{Checkpoint{
					EntityID: testID,
					State: Note{
						ID: testID, Title: "Deploy runbook", Body: "Ship from green main only.",
						Tags: []string{"ops"}, Anchors: []Anchor{{Kind: AnchorCommit, Value: testID}},
						Author: "ada <ada@example.com>", CreatedAt: 100, UpdatedAt: 200,
						SupersededBy: []EntityID{}, Head: testParent,
					},
					CoversLamport: 5,
					CoversShas:    []SHA{testParent, testID},
				}},
			},
			want: `{"v":1,"lamport":6,"ops":[{"kind":"checkpoint","entity_id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","state_kind":"note","state":{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Deploy runbook","body":"Ship from green main only.","tags":["ops"],"anchors":[{"kind":"commit","value":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}],"author":"ada \u003cada@example.com\u003e","created_at":100,"updated_at":200,"deleted":false,"verified_at":0,"verified_by":"","verified_commit":"","witness":null,"superseded_by":[],"stale_at":0,"stale_by":"","stale_reason":"","head":"00112233445566778899aabbccddeeff00112233"},"covers_lamport":5,"covers_shas":["00112233445566778899aabbccddeeff00112233","a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"]}]}`,
		},
		{
			name: "checkpoint over a task",
			pack: Pack{
				Lamport: 8,
				Ops: []Op{Checkpoint{
					EntityID: testID,
					State: Task{
						ID: testID, Branch: "main", Title: "Fix flaky sync", Description: "round-trip flakes",
						Type: TypeBug, Status: StatusInProgress, Priority: 1, Assignee: "agent-7",
						HeartbeatAt: 300, HeartbeatLamport: 4, Labels: []string{"ci"}, BlockedBy: []EntityID{},
						Comments: []Comment{{Author: "agent-7", TS: 300, Body: "on it"}}, CreatedAt: 100,
						UpdatedAt: 300, StartedAt: 200, Commits: []SHA{}, Head: testParent,
					},
					CoversLamport: 7,
					CoversShas:    []SHA{testParent, testID},
				}},
			},
			want: `{"v":1,"lamport":8,"ops":[{"kind":"checkpoint","entity_id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","state_kind":"task","state":{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","branch":"main","title":"Fix flaky sync","description":"round-trip flakes","type":"bug","status":"in_progress","priority":1,"assignee":"agent-7","heartbeat_at":300,"heartbeat_lamport":4,"labels":["ci"],"blocked_by":[],"parent":"","comments":[{"author":"agent-7","ts":300,"body":"on it"}],"created_at":100,"updated_at":300,"started_at":200,"closed_at":0,"commits":[],"head":"00112233445566778899aabbccddeeff00112233","sprint":"","project":"","criteria":null},"covers_lamport":7,"covers_shas":["00112233445566778899aabbccddeeff00112233","a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"]}]}`,
		},
		{
			name: "checkpoint over a sprint",
			pack: Pack{
				Lamport: 6,
				Ops: []Op{Checkpoint{
					EntityID: testID,
					State: Sprint{
						ID: testID, Project: testParent, Title: "Sprint 1", Description: "first sprint",
						Status: SprintActive, StartDate: 1000, EndDate: 2000, Labels: []string{"q3"},
						Commits: []SHA{}, Comments: []Comment{{Author: "ada", TS: 150, Body: "kickoff"}},
						Author: "ada", CreatedAt: 100, UpdatedAt: 200, StartedAt: 120, Head: testParent,
					},
					CoversLamport: 5,
					CoversShas:    []SHA{testParent, testID},
				}},
			},
			want: `{"v":1,"lamport":6,"ops":[{"kind":"checkpoint","entity_id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","state_kind":"sprint","state":{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","project":"00112233445566778899aabbccddeeff00112233","title":"Sprint 1","description":"first sprint","status":"active","start_date":1000,"end_date":2000,"labels":["q3"],"commits":[],"comments":[{"author":"ada","ts":150,"body":"kickoff"}],"author":"ada","created_at":100,"updated_at":200,"started_at":120,"closed_at":0,"head":"00112233445566778899aabbccddeeff00112233"},"covers_lamport":5,"covers_shas":["00112233445566778899aabbccddeeff00112233","a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"]}]}`,
		},
		{
			name: "checkpoint over a project",
			pack: Pack{
				Lamport: 10,
				Ops: []Op{Checkpoint{
					EntityID: testID,
					State: Project{
						ID: testID, Title: "Q3 Platform", Description: "platform work",
						Status: ProjectActive, Labels: []string{"infra"}, Commits: []SHA{}, Comments: []Comment{},
						Author: "ada", CreatedAt: 100, UpdatedAt: 200, Head: testParent,
					},
					CoversLamport: 9,
					CoversShas:    []SHA{testParent, testID},
				}},
			},
			want: `{"v":1,"lamport":10,"ops":[{"kind":"checkpoint","entity_id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","state_kind":"project","state":{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Q3 Platform","description":"platform work","status":"active","labels":["infra"],"commits":[],"comments":[],"author":"ada","created_at":100,"updated_at":200,"closed_at":0,"head":"00112233445566778899aabbccddeeff00112233"},"covers_lamport":9,"covers_shas":["00112233445566778899aabbccddeeff00112233","a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"]}]}`,
		},
		{
			name: "checkpoint over a log",
			pack: Pack{
				Lamport: 6,
				Ops: []Op{Checkpoint{
					EntityID: testID,
					State: Log{
						ID: testID, Title: "Auth rollout",
						Entries: []LogEntry{{Author: "ada", TS: 150, Text: "flipped to 5%"}},
						Tags:    []string{"ops"}, Anchors: []Anchor{{Kind: AnchorDir, Value: "internal/auth"}},
						Author: "ada", CreatedAt: 100, UpdatedAt: 150, Head: testParent,
					},
					CoversLamport: 5,
					CoversShas:    []SHA{testParent, testID},
				}},
			},
			want: `{"v":1,"lamport":6,"ops":[{"kind":"checkpoint","entity_id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","state_kind":"log","state":{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Auth rollout","entries":[{"author":"ada","ts":150,"text":"flipped to 5%"}],"tags":["ops"],"anchors":[{"kind":"dir","value":"internal/auth"}],"author":"ada","created_at":100,"updated_at":150,"deleted":false,"head":"00112233445566778899aabbccddeeff00112233"},"covers_lamport":5,"covers_shas":["00112233445566778899aabbccddeeff00112233","a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.pack)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestPackSessionRoundTrip(t *testing.T) {
	const session = "0b5c9b3a-7e2f-4c1d-9a8b-2f3e4d5c6b7a"
	pack := Pack{
		Lamport: 7,
		Session: session,
		Ops:     []Op{AddTag{Tag: "audit"}},
	}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("marshal session pack: %v", err)
	}
	got, err := DecodePack(data)
	if err != nil {
		t.Fatalf("decode session pack: %v", err)
	}
	if got.Session != session {
		t.Fatalf("Session = %q, want %q", got.Session, session)
	}

	emptySession := Pack{Lamport: 3, Session: "", Ops: []Op{Renew{}}}
	emptyData, err := json.Marshal(emptySession)
	if err != nil {
		t.Fatalf("marshal empty session pack: %v", err)
	}
	if strings.Contains(string(emptyData), `"session"`) {
		t.Fatalf("marshal empty session pack = %s, want no session key", emptyData)
	}
	withoutSession := Pack{Lamport: 3, Ops: []Op{Renew{}}}
	withoutData, err := json.Marshal(withoutSession)
	if err != nil {
		t.Fatalf("marshal session-less pack: %v", err)
	}
	if string(emptyData) != string(withoutData) {
		t.Fatalf("marshal empty session pack = %s, want %s", emptyData, withoutData)
	}
}

func TestDecodePackFailures(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"unknown kind", `{"v":1,"lamport":1,"ops":[{"kind":"explode"}]}`, ErrUnknownKind},
		{"missing kind", `{"v":1,"lamport":1,"ops":[{"title":"x"}]}`, ErrUnknownKind},
		{"version 2", `{"v":2,"lamport":1,"ops":[]}`, ErrUnsupportedVersion},
		{"missing version", `{"lamport":1,"ops":[]}`, ErrUnsupportedVersion},
		{"bad status", `{"v":1,"lamport":1,"ops":[{"kind":"set_status","status":"paused"}]}`, ErrInvalidValue},
		{"bad sprint status", `{"v":1,"lamport":1,"ops":[{"kind":"set_sprint_status","status":"paused"}]}`, ErrInvalidValue},
		{"bad project status", `{"v":1,"lamport":1,"ops":[{"kind":"set_project_status","status":"frozen"}]}`, ErrInvalidValue},
		{"bad criterion status", `{"v":1,"lamport":1,"ops":[{"kind":"set_criterion_status","id":"crit-1","status":"unknown"}]}`, ErrInvalidValue},
		{"priority 4", `{"v":1,"lamport":1,"ops":[{"kind":"set_priority","priority":4}]}`, ErrInvalidValue},
		{"negative priority", `{"v":1,"lamport":1,"ops":[{"kind":"set_priority","priority":-1}]}`, ErrInvalidValue},
		{"bad task type", `{"v":1,"lamport":1,"ops":[{"kind":"set_type","type":"chore"}]}`, ErrInvalidValue},
		{"bad anchor kind", `{"v":1,"lamport":1,"ops":[{"kind":"add_anchor","anchor":{"kind":"url","value":"https://x"}}]}`, ErrInvalidValue},
		{"bad anchor kind in create_note", `{"v":1,"lamport":1,"ops":[{"kind":"create_note","nonce":"00","title":"t","body":"","tags":[],"anchors":[{"kind":"tag","value":"v"}]}]}`, ErrInvalidValue},
		{"bad anchor kind in create_runbook", `{"v":1,"lamport":1,"ops":[{"kind":"create_runbook","nonce":"00","title":"t","description":"","labels":[],"anchors":[{"kind":"url","value":"https://x"}]}]}`, ErrInvalidValue},
		{"bad witness anchor kind in verify_note", `{"v":1,"lamport":1,"ops":[{"kind":"verify_note","witness":[{"anchor":{"kind":"url","value":"https://x"},"oid":"abc"}],"verified_commit":"def"}]}`, ErrInvalidValue},
		{"bad priority in create_task", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"task","priority":7,"branch":"main","parent":"","labels":[]}]}`, ErrInvalidValue},
		{"bad type in create_task", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"story","priority":0,"branch":"main","parent":"","labels":[]}]}`, ErrInvalidValue},
		{"traversal branch in create_task", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"task","priority":0,"branch":"../evil","parent":"","labels":[]}]}`, ErrInvalidValue},
		{"space in create_task branch", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"task","priority":0,"branch":"feat ure","parent":"","labels":[]}]}`, ErrInvalidValue},
		{"dot-leading create_task branch", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"task","priority":0,"branch":".hidden","parent":"","labels":[]}]}`, ErrInvalidValue},
		{"empty component in create_task branch", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"task","priority":0,"branch":"a//b","parent":"","labels":[]}]}`, ErrInvalidValue},
		{"traversal branch in set_branch", `{"v":1,"lamport":1,"ops":[{"kind":"set_branch","branch":"../evil"}]}`, ErrInvalidValue},
		{"trailing slash in set_branch", `{"v":1,"lamport":1,"ops":[{"kind":"set_branch","branch":"feature/"}]}`, ErrInvalidValue},
		{"unknown checkpoint state_kind", `{"v":1,"lamport":1,"ops":[{"kind":"checkpoint","entity_id":"a1","state_kind":"epic","state":{"id":"a1"},"covers_lamport":1,"covers_shas":["a1"]}]}`, ErrInvalidValue},
		{"missing checkpoint state", `{"v":1,"lamport":1,"ops":[{"kind":"checkpoint","entity_id":"a1","state_kind":"note","covers_lamport":1,"covers_shas":["a1"]}]}`, ErrInvalidValue},
		{"empty attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"","oid":"` + testOID + `","size":1}]}`, ErrInvalidValue},
		{"slash in attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"a/b.png","oid":"` + testOID + `","size":1}]}`, ErrInvalidValue},
		{"dot attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":".","oid":"` + testOID + `","size":1}]}`, ErrInvalidValue},
		{"dot-dot attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"..","oid":"` + testOID + `","size":1}]}`, ErrInvalidValue},
		{"control char in attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"a\u0001b","oid":"` + testOID + `","size":1}]}`, ErrInvalidValue},
		{"overlong attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"` + strings.Repeat("x", 256) + `","oid":"` + testOID + `","size":1}]}`, ErrInvalidValue},
		{"short attachment oid", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"a.png","oid":"abc123","size":1}]}`, ErrInvalidValue},
		{"uppercase attachment oid", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"a.png","oid":"` + strings.ToUpper(testOID) + `","size":1}]}`, ErrInvalidValue},
		{"sha1-length attachment oid", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"a.png","oid":"` + testID + `","size":1}]}`, ErrInvalidValue},
		{"zero attachment size", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"a.png","oid":"` + testOID + `","size":0}]}`, ErrInvalidValue},
		{"negative attachment size", `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"a.png","oid":"` + testOID + `","size":-1}]}`, ErrInvalidValue},
		{"empty remove_attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"remove_attachment","name":""}]}`, ErrInvalidValue},
		{"traversal remove_attachment name", `{"v":1,"lamport":1,"ops":[{"kind":"remove_attachment","name":".."}]}`, ErrInvalidValue},
		{"empty add_step position", `{"v":1,"lamport":1,"ops":[{"kind":"add_step","id":"s1","text":"t","command":"","position":""}]}`, ErrInvalidValue},
		{"trailing-zero add_step position", `{"v":1,"lamport":1,"ops":[{"kind":"add_step","id":"s1","text":"t","command":"","position":"a0"}]}`, ErrInvalidValue},
		{"non-digit add_step position", `{"v":1,"lamport":1,"ops":[{"kind":"add_step","id":"s1","text":"t","command":"","position":"A"}]}`, ErrInvalidValue},
		{"empty set_step_position position", `{"v":1,"lamport":1,"ops":[{"kind":"set_step_position","id":"s1","position":""}]}`, ErrInvalidValue},
		{"finish_run running", `{"v":1,"lamport":1,"ops":[{"kind":"finish_run","id":"r1","status":"running"}]}`, ErrInvalidValue},
		{"finish_run bogus status", `{"v":1,"lamport":1,"ops":[{"kind":"finish_run","id":"r1","status":"paused"}]}`, ErrInvalidValue},
		{"bogus set_run_step_status status", `{"v":1,"lamport":1,"ops":[{"kind":"set_run_step_status","run_id":"r1","step_id":"s1","status":"pending","note":""}]}`, ErrInvalidValue},
		{"bogus set_runbook_status status", `{"v":1,"lamport":1,"ops":[{"kind":"set_runbook_status","status":"deleted"}]}`, ErrInvalidValue},
		{"empty add_step id", `{"v":1,"lamport":1,"ops":[{"kind":"add_step","id":"","text":"t","command":"","position":"a"}]}`, ErrInvalidValue},
		{"empty remove_step id", `{"v":1,"lamport":1,"ops":[{"kind":"remove_step","id":""}]}`, ErrInvalidValue},
		{"empty set_step_text id", `{"v":1,"lamport":1,"ops":[{"kind":"set_step_text","id":"","text":"t"}]}`, ErrInvalidValue},
		{"empty set_step_command id", `{"v":1,"lamport":1,"ops":[{"kind":"set_step_command","id":"","command":"go test"}]}`, ErrInvalidValue},
		{"empty set_step_position id", `{"v":1,"lamport":1,"ops":[{"kind":"set_step_position","id":"","position":"a"}]}`, ErrInvalidValue},
		{"empty start_run id", `{"v":1,"lamport":1,"ops":[{"kind":"start_run","id":"","task":""}]}`, ErrInvalidValue},
		{"empty set_run_step_status run_id", `{"v":1,"lamport":1,"ops":[{"kind":"set_run_step_status","run_id":"","step_id":"s1","status":"done","note":""}]}`, ErrInvalidValue},
		{"empty set_run_step_status step_id", `{"v":1,"lamport":1,"ops":[{"kind":"set_run_step_status","run_id":"r1","step_id":"","status":"done","note":""}]}`, ErrInvalidValue},
		{"empty finish_run id", `{"v":1,"lamport":1,"ops":[{"kind":"finish_run","id":"","status":"succeeded"}]}`, ErrInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePack([]byte(tc.input))
			if !errors.Is(err, tc.want) {
				t.Fatalf("DecodePack(%s) error = %v, want errors.Is %v", tc.input, err, tc.want)
			}
		})
	}
}

func TestDecodePackEmptyBranch(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  Op
	}{
		{
			"set_branch to backlog",
			`{"v":1,"lamport":1,"ops":[{"kind":"set_branch","branch":""}]}`,
			SetBranch{Branch: ""},
		},
		{
			"create_task on backlog",
			`{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"task","priority":0,"branch":"","parent":"","labels":[]}]}`,
			CreateTask{Nonce: "00", Title: "t", Type: TypeTask, Priority: 0, Labels: []string{}},
		},
		{
			"reclaim with zero after_lamport",
			`{"v":1,"lamport":1,"ops":[{"kind":"reclaim","assignee":"agent-2","from":"agent-1","after_lamport":0}]}`,
			Reclaim{Assignee: "agent-2", From: "agent-1", AfterLamport: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack, err := DecodePack([]byte(tc.input))
			if err != nil {
				t.Fatalf("DecodePack(%s) error = %v, want nil", tc.input, err)
			}
			if len(pack.Ops) != 1 {
				t.Fatalf("len(Ops) = %d, want 1", len(pack.Ops))
			}
			if !reflect.DeepEqual(pack.Ops[0], tc.want) {
				t.Fatalf("Ops[0] = %#v, want %#v", pack.Ops[0], tc.want)
			}
		})
	}
}

func TestDecodePackAttachmentEdgeNames(t *testing.T) {
	cases := []struct {
		name string
		want Op
	}{
		{"with space.png", AddAttachment{Name: "with space.png", OID: testOID, Size: 1}},
		{"снимок.png", AddAttachment{Name: "снимок.png", OID: testOID, Size: 1}},
		{"...", AddAttachment{Name: "...", OID: testOID, Size: 1}},
		{strings.Repeat("x", 255), AddAttachment{Name: strings.Repeat("x", 255), OID: testOID, Size: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := `{"v":1,"lamport":1,"ops":[{"kind":"add_attachment","name":"` + tc.name + `","oid":"` + testOID + `","size":1}]}`
			pack, err := DecodePack([]byte(wire))
			if err != nil {
				t.Fatalf("DecodePack error = %v, want nil", err)
			}
			if !reflect.DeepEqual(pack.Ops[0], tc.want) {
				t.Fatalf("Ops[0] = %#v, want %#v", pack.Ops[0], tc.want)
			}
		})
	}
}

func TestDecodePackMalformedJSON(t *testing.T) {
	if _, err := DecodePack([]byte(`{"v":1,`)); err == nil {
		t.Fatal("DecodePack on truncated JSON returned nil error")
	}
}

func TestDecodeEmptyPack(t *testing.T) {
	pack, err := DecodePack([]byte(`{"v":1,"lamport":9,"ops":[]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pack.Lamport != 9 {
		t.Fatalf("Lamport = %d, want 9", pack.Lamport)
	}
	if len(pack.Ops) != 0 {
		t.Fatalf("len(Ops) = %d, want 0", len(pack.Ops))
	}
}

type fakeOp struct{}

func (fakeOp) OpKind() string { return "fake" }

func TestMarshalUnregisteredOpFails(t *testing.T) {
	_, err := json.Marshal(Pack{Lamport: 1, Ops: []Op{fakeOp{}}})
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("marshal error = %v, want errors.Is ErrUnknownKind", err)
	}
}

// foreignOp is an Op whose OpKind() can collide with a registered kind: it stands
// in for a foreign implementation of the public Op interface. The marshal gate
// keys by concrete type, so it must reject foreignOp even when its kind string
// matches a real op.
type foreignOp struct{ kind string }

func (f foreignOp) OpKind() string { return f.kind }

// foreignSnapshot is a Snapshot whose Meta().Kind is the empty (unregistered)
// kind — a foreign implementation of the public Snapshot interface used as a
// checkpoint state.
type foreignSnapshot struct{}

func (foreignSnapshot) EntityID() EntityID { return "" }
func (foreignSnapshot) Meta() Meta         { return Meta{} }

// TestMarshalRejectsTypeImposters pins that the marshal gate keys ops by concrete
// type, not the OpKind() string. A pointer op, a foreign Op whose OpKind()
// collides with a registered kind, a nil op, a pointer Checkpoint, a checkpoint
// with a nil State, and a checkpoint over a foreign Snapshot each fail with
// ErrUnknownKind — without panicking — instead of emitting a malformed or
// mis-tagged pack into the content-addressed store. These are the exact holes a
// string-keyed gate leaves open that the fakeOp test does not catch.
func TestMarshalRejectsTypeImposters(t *testing.T) {
	note := Note{ID: testID, Title: "t", SupersededBy: []EntityID{}}
	cases := []struct {
		name string
		op   Op
	}{
		{"pointer op", &SetTitle{Title: "x"}},
		{"foreign op with colliding kind", foreignOp{kind: "create_note"}},
		{"nil op", nil},
		{"pointer checkpoint", &Checkpoint{EntityID: testID, State: note, CoversLamport: 1}},
		{"checkpoint with nil state", Checkpoint{EntityID: testID, CoversLamport: 1}},
		{"checkpoint over foreign snapshot", Checkpoint{EntityID: testID, State: foreignSnapshot{}, CoversLamport: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := json.Marshal(Pack{Lamport: 1, Ops: []Op{tc.op}})
			if !errors.Is(err, ErrUnknownKind) {
				t.Fatalf("marshal = %v, want errors.Is ErrUnknownKind", err)
			}
		})
	}
}

var opKindCharset = regexp.MustCompile(`^[a-z0-9_]+$`)

// TestOpKindCharset asserts every registered op kind is a lower-snake token.
// The byte-splicing codec that replaces marshalOp keys ops by this string, so a
// stray uppercase letter, dash, or space would break the discriminator. Kinds
// come from the opDecoders registry, never a hand-list.
func TestOpKindCharset(t *testing.T) {
	if len(opDecoders) == 0 {
		t.Fatal("opDecoders is empty")
	}
	for kind := range opDecoders {
		if !opKindCharset.MatchString(kind) {
			t.Errorf("op kind %q does not match %s", kind, opKindCharset)
		}
	}
}

// everySnapshot holds one zero value of every Snapshot implementor. No runtime
// registry enumerates snapshot types, so this list is the enumeration
// TestNoOpOrSnapshotHasCustomJSON reflects over; the same test asserts every
// entry is a checkpoint state the codec recognizes, catching a stale entry.
// Add a new Snapshot type here when you define one.
var everySnapshot = []Snapshot{Note{}, Doc{}, Log{}, Task{}, Sprint{}, Project{}, Runbook{}}

// TestNoOpOrSnapshotHasCustomJSON asserts no op type and no snapshot type
// implements json.Marshaler or json.Unmarshaler. The byte-splicing codec that
// replaces the marshalOp switch depends on every op and snapshot being a plain
// struct the encoding/json machinery walks field by field; a custom
// (Un)MarshalJSON on any of them would splice bytes that diverge from what the
// reflect path emits. Op types come from everyOpSample (pinned exhaustive
// against opDecoders by TestPackRoundTripEveryOpKind); snapshot types from
// everySnapshot.
func TestNoOpOrSnapshotHasCustomJSON(t *testing.T) {
	marshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	unmarshaler := reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	check := func(_ string, typ reflect.Type) {
		for _, probe := range []reflect.Type{typ, reflect.PointerTo(typ)} {
			if probe.Implements(marshaler) {
				t.Errorf("%s implements json.Marshaler; the byte-splicing codec requires plain structs", probe)
			}
			if probe.Implements(unmarshaler) {
				t.Errorf("%s implements json.Unmarshaler; the byte-splicing codec requires plain structs", probe)
			}
		}
	}
	for _, s := range everyOpSample {
		check(s.kind, reflect.TypeOf(s.op))
	}
	for _, snap := range everySnapshot {
		check(reflect.TypeOf(snap).Name(), reflect.TypeOf(snap))
		if _, err := marshalCheckpoint(Checkpoint{EntityID: snap.EntityID(), State: snap}); err != nil {
			t.Errorf("snapshot %T is not a recognized checkpoint state: %v", snap, err)
		}
	}
}
