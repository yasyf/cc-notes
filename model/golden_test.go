package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// preAttachmentGoldens pins the exact v1 wire bytes of every op kind that
// existed before attachments, one single-op pack per kind. The bytes are part
// of the storage format — commit hashes and therefore entity ids derive from
// them — so any marshal-layout drift must fail here.
var preAttachmentGoldens = []struct {
	kind string
	op   Op
	want string
}{
	{"create_note", CreateNote{
		Nonce: testNonce, Title: "Deploy runbook", Body: "Ship from green main only.",
		Tags: []string{"ops", "ci"},
		Anchors: []Anchor{
			{Kind: AnchorCommit, Value: testID},
			{Kind: AnchorPath, Value: "docs/deploy.md"},
		},
	}, `{"v":1,"lamport":42,"ops":[{"kind":"create_note","nonce":"0123456789abcdef0123456789abcdef","title":"Deploy runbook","body":"Ship from green main only.","tags":["ops","ci"],"anchors":[{"kind":"commit","value":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"},{"kind":"path","value":"docs/deploy.md"}]}]}`},
	{
		"set_title",
		SetTitle{Title: "New title"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_title","title":"New title"}]}`,
	},
	{
		"set_body",
		SetBody{Body: "New body"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_body","body":"New body"}]}`,
	},
	{
		"set_when",
		SetWhen{When: "before touching the auth flow"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_when","when":"before touching the auth flow"}]}`,
	},
	{
		"add_tag",
		AddTag{Tag: "urgent"},
		`{"v":1,"lamport":42,"ops":[{"kind":"add_tag","tag":"urgent"}]}`,
	},
	{
		"remove_tag",
		RemoveTag{Tag: "stale"},
		`{"v":1,"lamport":42,"ops":[{"kind":"remove_tag","tag":"stale"}]}`,
	},
	{
		"add_anchor",
		AddAnchor{Anchor: Anchor{Kind: AnchorPath, Value: "model/pack.go"}},
		`{"v":1,"lamport":42,"ops":[{"kind":"add_anchor","anchor":{"kind":"path","value":"model/pack.go"}}]}`,
	},
	{
		"remove_anchor",
		RemoveAnchor{Anchor: Anchor{Kind: AnchorBranch, Value: "main"}},
		`{"v":1,"lamport":42,"ops":[{"kind":"remove_anchor","anchor":{"kind":"branch","value":"main"}}]}`,
	},
	{
		"delete_note",
		DeleteNote{},
		`{"v":1,"lamport":42,"ops":[{"kind":"delete_note"}]}`,
	},
	{"verify_note", VerifyNote{
		Witness: []AnchorWitness{
			{Anchor: Anchor{Kind: AnchorPath, Value: "docs/deploy.md"}, OID: "f1e2d3c4b5a60718293a4b5c6d7e8f90a1b2c3d4"},
		},
		VerifiedCommit: testParent,
	}, `{"v":1,"lamport":42,"ops":[{"kind":"verify_note","witness":[{"anchor":{"kind":"path","value":"docs/deploy.md"},"oid":"f1e2d3c4b5a60718293a4b5c6d7e8f90a1b2c3d4"}],"verified_commit":"00112233445566778899aabbccddeeff00112233"}]}`},
	{
		"add_superseded_by",
		AddSupersededBy{ID: testID},
		`{"v":1,"lamport":42,"ops":[{"kind":"add_superseded_by","id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
	},
	{
		"remove_superseded_by",
		RemoveSupersededBy{ID: testID},
		`{"v":1,"lamport":42,"ops":[{"kind":"remove_superseded_by","id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
	},
	{
		"mark_stale",
		MarkStale{Reason: "broken in prod"},
		`{"v":1,"lamport":42,"ops":[{"kind":"mark_stale","reason":"broken in prod"}]}`,
	},
	{
		"clear_stale",
		ClearStale{},
		`{"v":1,"lamport":42,"ops":[{"kind":"clear_stale"}]}`,
	},
	{"create_task", CreateTask{
		Nonce: testNonce, Title: "Fix flaky sync", Description: "Two-clone round-trip flakes",
		Type: TypeBug, Priority: 1, Branch: "feature/sync", Parent: testParent,
		Labels: []string{"ci", "sync"},
	}, `{"v":1,"lamport":42,"ops":[{"kind":"create_task","nonce":"0123456789abcdef0123456789abcdef","title":"Fix flaky sync","description":"Two-clone round-trip flakes","type":"bug","priority":1,"branch":"feature/sync","parent":"00112233445566778899aabbccddeeff00112233","labels":["ci","sync"]}]}`},
	{"create_sprint", CreateSprint{
		Nonce: testNonce, Title: "Q3 sync hardening", Description: "Stabilize two-clone sync",
		Project: testParent, Labels: []string{"ops", "sync"},
	}, `{"v":1,"lamport":42,"ops":[{"kind":"create_sprint","nonce":"0123456789abcdef0123456789abcdef","title":"Q3 sync hardening","description":"Stabilize two-clone sync","project":"00112233445566778899aabbccddeeff00112233","labels":["ops","sync"]}]}`},
	{"create_project", CreateProject{
		Nonce: testNonce, Title: "Platform", Description: "Core infrastructure", Labels: []string{"infra"},
	}, `{"v":1,"lamport":42,"ops":[{"kind":"create_project","nonce":"0123456789abcdef0123456789abcdef","title":"Platform","description":"Core infrastructure","labels":["infra"]}]}`},
	{"create_doc", CreateDoc{
		Nonce: testNonce, Title: "Auth architecture", Body: "How the token refresh loop works.",
		When: "before touching the auth flow", Tags: []string{"auth", "arch"},
		Anchors: []Anchor{{Kind: AnchorPath, Value: "internal/auth/token.go"}},
	}, `{"v":1,"lamport":42,"ops":[{"kind":"create_doc","nonce":"0123456789abcdef0123456789abcdef","title":"Auth architecture","body":"How the token refresh loop works.","when":"before touching the auth flow","tags":["auth","arch"],"anchors":[{"kind":"path","value":"internal/auth/token.go"}]}]}`},
	{"create_log", CreateLog{
		Nonce: testNonce, Title: "Auth rollout", Tags: []string{"ops", "auth"},
		Anchors: []Anchor{{Kind: AnchorDir, Value: "internal/auth"}},
	}, `{"v":1,"lamport":42,"ops":[{"kind":"create_log","nonce":"0123456789abcdef0123456789abcdef","title":"Auth rollout","tags":["ops","auth"],"anchors":[{"kind":"dir","value":"internal/auth"}]}]}`},
	{
		"append_entry",
		AppendEntry{Text: "flipped to 5%"},
		`{"v":1,"lamport":42,"ops":[{"kind":"append_entry","text":"flipped to 5%"}]}`,
	},
	{
		"set_description",
		SetDescription{Description: "Updated description"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_description","description":"Updated description"}]}`,
	},
	{
		"set_type",
		SetType{Type: TypeEpic},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_type","type":"epic"}]}`,
	},
	{
		"set_priority",
		SetPriority{Priority: 3},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_priority","priority":3}]}`,
	},
	{
		"set_status",
		SetStatus{Status: StatusDone},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_status","status":"done"}]}`,
	},
	{
		"set_assignee",
		SetAssignee{Assignee: "agent-1"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_assignee","assignee":"agent-1"}]}`,
	},
	{
		"claim",
		Claim{Assignee: "agent-2"},
		`{"v":1,"lamport":42,"ops":[{"kind":"claim","assignee":"agent-2"}]}`,
	},
	{
		"renew",
		Renew{},
		`{"v":1,"lamport":42,"ops":[{"kind":"renew"}]}`,
	},
	{
		"reclaim",
		Reclaim{Assignee: "agent-2", From: "agent-1", AfterLamport: 5},
		`{"v":1,"lamport":42,"ops":[{"kind":"reclaim","assignee":"agent-2","from":"agent-1","after_lamport":5}]}`,
	},
	{
		"add_label",
		AddLabel{Label: "backend"},
		`{"v":1,"lamport":42,"ops":[{"kind":"add_label","label":"backend"}]}`,
	},
	{
		"remove_label",
		RemoveLabel{Label: "frontend"},
		`{"v":1,"lamport":42,"ops":[{"kind":"remove_label","label":"frontend"}]}`,
	},
	{
		"add_dep",
		AddDep{ID: testID},
		`{"v":1,"lamport":42,"ops":[{"kind":"add_dep","id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
	},
	{
		"remove_dep",
		RemoveDep{ID: testID},
		`{"v":1,"lamport":42,"ops":[{"kind":"remove_dep","id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
	},
	{
		"link_commit",
		LinkCommit{SHA: testID},
		`{"v":1,"lamport":42,"ops":[{"kind":"link_commit","sha":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
	},
	{
		"unlink_commit",
		UnlinkCommit{SHA: testParent},
		`{"v":1,"lamport":42,"ops":[{"kind":"unlink_commit","sha":"00112233445566778899aabbccddeeff00112233"}]}`,
	},
	{
		"set_parent",
		SetParent{Parent: testParent},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_parent","parent":"00112233445566778899aabbccddeeff00112233"}]}`,
	},
	{
		"add_comment",
		AddComment{Body: "Taking this one."},
		`{"v":1,"lamport":42,"ops":[{"kind":"add_comment","body":"Taking this one."}]}`,
	},
	{
		"set_branch",
		SetBranch{Branch: "feature/x"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_branch","branch":"feature/x"}]}`,
	},
	{
		"set_sprint",
		SetSprint{Sprint: testID},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_sprint","sprint":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
	},
	{
		"set_project",
		SetProject{Project: testParent},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_project","project":"00112233445566778899aabbccddeeff00112233"}]}`,
	},
	{
		"set_sprint_status",
		SetSprintStatus{Status: SprintActive},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_sprint_status","status":"active"}]}`,
	},
	{
		"set_project_status",
		SetProjectStatus{Status: ProjectArchived},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_project_status","status":"archived"}]}`,
	},
	{
		"set_start_date",
		SetStartDate{Date: 1700000000},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_start_date","date":1700000000}]}`,
	},
	{
		"set_end_date",
		SetEndDate{Date: 1701000000},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_end_date","date":1701000000}]}`,
	},
	{
		"add_criterion",
		AddCriterion{ID: "crit-1", Text: "all tests pass", Script: "go test ./..."},
		`{"v":1,"lamport":42,"ops":[{"kind":"add_criterion","id":"crit-1","text":"all tests pass","script":"go test ./..."}]}`,
	},
	{
		"remove_criterion",
		RemoveCriterion{ID: "crit-1"},
		`{"v":1,"lamport":42,"ops":[{"kind":"remove_criterion","id":"crit-1"}]}`,
	},
	{
		"set_criterion_text",
		SetCriterionText{ID: "crit-1", Text: "all tests pass under -race"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_criterion_text","id":"crit-1","text":"all tests pass under -race"}]}`,
	},
	{
		"set_criterion_status",
		SetCriterionStatus{ID: "crit-1", Status: CriterionMet},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_criterion_status","id":"crit-1","status":"met"}]}`,
	},
	{
		"set_criterion_script",
		SetCriterionScript{ID: "crit-1", Script: "make check"},
		`{"v":1,"lamport":42,"ops":[{"kind":"set_criterion_script","id":"crit-1","script":"make check"}]}`,
	},
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
	}, `{"v":1,"lamport":42,"ops":[{"kind":"checkpoint","entity_id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","state_kind":"note","state":{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Deploy runbook","body":"Ship from green main only.","tags":["ops"],"anchors":[{"kind":"commit","value":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}],"author":"ada","created_at":100,"updated_at":200,"deleted":false,"verified_at":0,"verified_by":"","verified_commit":"","witness":null,"superseded_by":[],"stale_at":0,"stale_by":"","stale_reason":"","head":"00112233445566778899aabbccddeeff00112233"},"covers_lamport":5,"covers_shas":["00112233445566778899aabbccddeeff00112233","a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"]}]}`},
}

// preAttachmentKinds is the closed set of op kinds that predate attachments,
// pinned as literals so the golden table cannot silently shrink when the
// registry grows.
var preAttachmentKinds = []string{
	"create_note", "set_title", "set_body", "set_when", "add_tag", "remove_tag",
	"add_anchor", "remove_anchor", "delete_note", "verify_note",
	"add_superseded_by", "remove_superseded_by", "mark_stale", "clear_stale",
	"create_task", "create_sprint", "create_project", "create_doc", "create_log",
	"append_entry", "set_description", "set_type", "set_priority", "set_status",
	"set_assignee", "claim", "renew", "reclaim", "add_label", "remove_label",
	"add_dep", "remove_dep", "link_commit", "unlink_commit", "set_parent",
	"add_comment", "set_branch", "set_sprint", "set_project",
	"set_sprint_status", "set_project_status", "set_start_date", "set_end_date",
	"add_criterion", "remove_criterion", "set_criterion_text",
	"set_criterion_status", "set_criterion_script", "checkpoint",
}

// TestPackGoldenBytesEveryPreAttachmentKind asserts every pre-attachment op
// kind still marshals to its pinned wire bytes and decodes back from them:
// the attachment change must be byte-neutral for existing histories.
func TestPackGoldenBytesEveryPreAttachmentKind(t *testing.T) {
	covered := make(map[string]bool, len(preAttachmentGoldens))
	for _, tc := range preAttachmentGoldens {
		covered[tc.kind] = true
	}
	for _, kind := range preAttachmentKinds {
		if !covered[kind] {
			t.Errorf("pre-attachment kind %q missing from the golden table", kind)
		}
	}
	if got, want := len(preAttachmentGoldens), len(preAttachmentKinds); got != want {
		t.Errorf("golden table has %d rows, want %d", got, want)
	}

	for _, tc := range preAttachmentGoldens {
		t.Run(tc.kind, func(t *testing.T) {
			got, err := json.Marshal(Pack{Lamport: 42, Ops: []Op{tc.op}})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal =\n%s\nwant\n%s", got, tc.want)
			}
			back, err := DecodePack([]byte(tc.want))
			if err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			again, err := json.Marshal(back)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if string(again) != tc.want {
				t.Fatalf("re-marshal =\n%s\nwant\n%s", again, tc.want)
			}
		})
	}
}

// TestPackGoldenBytesAllPreAttachmentKindsOnePack marshals one pack carrying
// every pre-attachment op in golden-table order and asserts the whole-pack
// bytes are exactly the concatenation of the per-kind pinned fragments: the
// pack envelope itself is byte-stable too.
func TestPackGoldenBytesAllPreAttachmentKindsOnePack(t *testing.T) {
	const prefix = `{"v":1,"lamport":42,"ops":[`
	const suffix = `]}`
	ops := make([]Op, len(preAttachmentGoldens))
	fragments := make([]string, len(preAttachmentGoldens))
	for i, tc := range preAttachmentGoldens {
		ops[i] = tc.op
		frag, ok := strings.CutPrefix(tc.want, prefix)
		if !ok {
			t.Fatalf("golden for %q lacks pack prefix", tc.kind)
		}
		frag, ok = strings.CutSuffix(frag, suffix)
		if !ok {
			t.Fatalf("golden for %q lacks pack suffix", tc.kind)
		}
		fragments[i] = frag
	}
	want := prefix + strings.Join(fragments, ",") + suffix
	got, err := json.Marshal(Pack{Lamport: 42, Ops: ops})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != want {
		t.Fatalf("marshal =\n%s\nwant\n%s", got, want)
	}
}

// TestPackGoldenBytesAttachmentOps pins the exact v1 wire bytes of the two
// attachment op kinds, both directions: marshal to the pinned bytes and
// decode back to the identical op.
func TestPackGoldenBytesAttachmentOps(t *testing.T) {
	cases := []struct {
		kind string
		op   Op
		want string
	}{
		{
			"add_attachment",
			AddAttachment{Name: "trace.png", OID: testOID, Size: 2048},
			`{"v":1,"lamport":42,"ops":[{"kind":"add_attachment","name":"trace.png","oid":"e3b1c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855","size":2048}]}`,
		},
		{
			"remove_attachment",
			RemoveAttachment{Name: "trace.png"},
			`{"v":1,"lamport":42,"ops":[{"kind":"remove_attachment","name":"trace.png"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			pack := Pack{Lamport: 42, Ops: []Op{tc.op}}
			got, err := json.Marshal(pack)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal =\n%s\nwant\n%s", got, tc.want)
			}
			back, err := DecodePack([]byte(tc.want))
			if err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			if !reflect.DeepEqual(back, pack) {
				t.Fatalf("decode = %#v, want %#v", back, pack)
			}
		})
	}
}

// TestPackGoldenBytesRunbookOps pins the exact v1 wire bytes of every runbook
// op kind and a runbook checkpoint, both directions: marshal to the pinned
// bytes and decode back to the identical op. These bytes are storage format —
// entity ids derive from them — so any marshal-layout drift fails here.
func TestPackGoldenBytesRunbookOps(t *testing.T) {
	cases := []struct {
		kind string
		op   Op
		want string
	}{
		{
			"create_runbook",
			CreateRunbook{Nonce: testNonce, Title: "Deploy", Description: "ship", Labels: []string{"deploy", "ops"}},
			`{"v":1,"lamport":42,"ops":[{"kind":"create_runbook","nonce":"0123456789abcdef0123456789abcdef","title":"Deploy","description":"ship","labels":["deploy","ops"]}]}`,
		},
		{
			"add_step",
			AddStep{ID: "s1", Text: "run tests", Command: "go test ./...", Position: "i"},
			`{"v":1,"lamport":42,"ops":[{"kind":"add_step","id":"s1","text":"run tests","command":"go test ./...","position":"i"}]}`,
		},
		{
			"remove_step",
			RemoveStep{ID: "s1"},
			`{"v":1,"lamport":42,"ops":[{"kind":"remove_step","id":"s1"}]}`,
		},
		{
			"set_step_text",
			SetStepText{ID: "s1", Text: "run tests under -race"},
			`{"v":1,"lamport":42,"ops":[{"kind":"set_step_text","id":"s1","text":"run tests under -race"}]}`,
		},
		{
			"set_step_command",
			SetStepCommand{ID: "s1", Command: "go test -race ./..."},
			`{"v":1,"lamport":42,"ops":[{"kind":"set_step_command","id":"s1","command":"go test -race ./..."}]}`,
		},
		{
			"set_step_position",
			SetStepPosition{ID: "s1", Position: "a"},
			`{"v":1,"lamport":42,"ops":[{"kind":"set_step_position","id":"s1","position":"a"}]}`,
		},
		{
			"start_run",
			StartRun{ID: "r1", Task: testID},
			`{"v":1,"lamport":42,"ops":[{"kind":"start_run","id":"r1","task":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}]}`,
		},
		{
			"set_run_step_status",
			SetRunStepStatus{RunID: "r1", StepID: "s1", Status: StepDone, Note: "green"},
			`{"v":1,"lamport":42,"ops":[{"kind":"set_run_step_status","run_id":"r1","step_id":"s1","status":"done","note":"green"}]}`,
		},
		{
			"finish_run",
			FinishRun{ID: "r1", Status: RunSucceeded},
			`{"v":1,"lamport":42,"ops":[{"kind":"finish_run","id":"r1","status":"succeeded"}]}`,
		},
		{
			"set_runbook_status",
			SetRunbookStatus{Status: RunbookArchived},
			`{"v":1,"lamport":42,"ops":[{"kind":"set_runbook_status","status":"archived"}]}`,
		},
		{
			"checkpoint_runbook",
			Checkpoint{
				EntityID: testID,
				State: Runbook{
					ID: testID, Title: "Deploy", Description: "ship", Status: RunbookActive,
					Steps: []RunbookStep{{ID: "s1", Text: "test", Command: "go test", Position: "i"}},
					Runs: []RunbookRun{{
						ID: "r1", Task: testParent, Status: RunSucceeded,
						Runner: "ada", StartedAt: 200, FinishedAt: 300,
						Results: []RunbookStepResult{{StepID: "s1", Status: StepDone, Note: "ok", Actor: "ada", TS: 250}},
					}},
					Labels: []string{"ops"}, Comments: []Comment{{Author: "ada", TS: 100, Body: "init"}},
					Author: "ada", CreatedAt: 100, UpdatedAt: 300, ArchivedAt: 0, Head: testParent,
				},
				CoversLamport: 6,
				CoversShas:    []SHA{testParent, testID},
			},
			`{"v":1,"lamport":42,"ops":[{"kind":"checkpoint","entity_id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","state_kind":"runbook","state":{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Deploy","description":"ship","status":"active","steps":[{"id":"s1","text":"test","command":"go test","position":"i"}],"runs":[{"id":"r1","task":"00112233445566778899aabbccddeeff00112233","status":"succeeded","runner":"ada","started_at":200,"finished_at":300,"results":[{"step_id":"s1","status":"done","note":"ok","actor":"ada","ts":250}]}],"labels":["ops"],"comments":[{"author":"ada","ts":100,"body":"init"}],"author":"ada","created_at":100,"updated_at":300,"archived_at":0,"head":"00112233445566778899aabbccddeeff00112233"},"covers_lamport":6,"covers_shas":["00112233445566778899aabbccddeeff00112233","a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			pack := Pack{Lamport: 42, Ops: []Op{tc.op}}
			got, err := json.Marshal(pack)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal =\n%s\nwant\n%s", got, tc.want)
			}
			back, err := DecodePack([]byte(tc.want))
			if err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			if !reflect.DeepEqual(back, pack) {
				t.Fatalf("decode = %#v, want %#v", back, pack)
			}
		})
	}
}

// TestSnapshotGoldenBytesAttachmentless pins the exact json.Marshal bytes of
// attachment-less Note, Doc, and Log snapshots. Checkpoint State embeds these
// bytes verbatim in the wire form, and checkpoint encode determinism across
// replicas is part of the storage format: an attachment-less snapshot written
// by this binary must be byte-identical to one written before attachments
// existed.
func TestSnapshotGoldenBytesAttachmentless(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
		want string
	}{
		{
			name: "note",
			snap: Note{
				ID: testID, Title: "Deploy runbook", Body: "Ship from green main only.",
				Tags: []string{"ops"}, Anchors: []Anchor{{Kind: AnchorCommit, Value: testID}},
				Author: "ada <ada@example.com>", CreatedAt: 100, UpdatedAt: 200,
				VerifiedAt: 150, VerifiedBy: "bob", VerifiedCommit: testParent,
				Witness:      []AnchorWitness{{Anchor: Anchor{Kind: AnchorCommit, Value: testID}, OID: testID}},
				SupersededBy: []EntityID{}, StaleAt: 160, StaleBy: "carol", StaleReason: "drifted",
				Head: testParent,
			},
			want: `{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Deploy runbook","body":"Ship from green main only.","tags":["ops"],"anchors":[{"kind":"commit","value":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}],"author":"ada \u003cada@example.com\u003e","created_at":100,"updated_at":200,"deleted":false,"verified_at":150,"verified_by":"bob","verified_commit":"00112233445566778899aabbccddeeff00112233","witness":[{"anchor":{"kind":"commit","value":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"},"oid":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}],"superseded_by":[],"stale_at":160,"stale_by":"carol","stale_reason":"drifted","head":"00112233445566778899aabbccddeeff00112233"}`,
		},
		{
			name: "doc",
			snap: Doc{
				ID: testID, Title: "Auth architecture", Body: "How the token refresh loop works.",
				When: "before touching the auth flow", Tags: []string{"auth"},
				Anchors: []Anchor{{Kind: AnchorPath, Value: "internal/auth/token.go"}},
				Author:  "ada", CreatedAt: 100, UpdatedAt: 200,
				SupersededBy: []EntityID{}, Head: testParent,
			},
			want: `{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Auth architecture","body":"How the token refresh loop works.","when":"before touching the auth flow","tags":["auth"],"anchors":[{"kind":"path","value":"internal/auth/token.go"}],"author":"ada","created_at":100,"updated_at":200,"deleted":false,"verified_at":0,"verified_by":"","verified_commit":"","witness":null,"superseded_by":[],"stale_at":0,"stale_by":"","stale_reason":"","head":"00112233445566778899aabbccddeeff00112233"}`,
		},
		{
			name: "log",
			snap: Log{
				ID: testID, Title: "Auth rollout",
				Entries: []LogEntry{{Author: "ada", TS: 150, Text: "flipped to 5%"}},
				Tags:    []string{"ops"}, Anchors: []Anchor{{Kind: AnchorDir, Value: "internal/auth"}},
				Author: "ada", CreatedAt: 100, UpdatedAt: 150, Head: testParent,
			},
			want: `{"id":"a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","title":"Auth rollout","entries":[{"author":"ada","ts":150,"text":"flipped to 5%"}],"tags":["ops"],"anchors":[{"kind":"dir","value":"internal/auth"}],"author":"ada","created_at":100,"updated_at":150,"deleted":false,"head":"00112233445566778899aabbccddeeff00112233"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.snap)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("marshal =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
