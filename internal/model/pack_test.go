package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	testNonce  = "0123456789abcdef0123456789abcdef"
	testID     = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"
	testParent = "00112233445566778899aabbccddeeff00112233"
)

func TestPackRoundTripEveryOpKind(t *testing.T) {
	cases := []struct {
		kind string
		op   Op
	}{
		{"create_note", CreateNote{
			Nonce: testNonce,
			Title: "Deploy runbook",
			Body:  "Ship from green main only.",
			Tags:  []string{"ops", "ci"},
			Anchors: []Anchor{
				{Kind: AnchorCommit, Value: testID},
				{Kind: AnchorPath, Value: "docs/deploy.md"},
				{Kind: AnchorBranch, Value: "main"},
			},
		}},
		{"set_title", SetTitle{Title: "New title"}},
		{"set_body", SetBody{Body: "New body"}},
		{"add_tag", AddTag{Tag: "urgent"}},
		{"remove_tag", RemoveTag{Tag: "stale"}},
		{"add_anchor", AddAnchor{Anchor: Anchor{Kind: AnchorPath, Value: "internal/model/pack.go"}}},
		{"remove_anchor", RemoveAnchor{Anchor: Anchor{Kind: AnchorBranch, Value: "main"}}},
		{"delete_note", DeleteNote{}},
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
		{"add_label", AddLabel{Label: "backend"}},
		{"remove_label", RemoveLabel{Label: "frontend"}},
		{"add_dep", AddDep{ID: testID}},
		{"remove_dep", RemoveDep{ID: testID}},
		{"set_parent", SetParent{Parent: testParent}},
		{"add_comment", AddComment{Body: "Taking this one."}},
		{"promote", Promote{From: "feature/sync", To: "main"}},
	}

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
			name: "empty merge pack",
			pack: Pack{Lamport: 9},
			want: `{"v":1,"lamport":9,"ops":[]}`,
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
		{"priority 4", `{"v":1,"lamport":1,"ops":[{"kind":"set_priority","priority":4}]}`, ErrInvalidValue},
		{"negative priority", `{"v":1,"lamport":1,"ops":[{"kind":"set_priority","priority":-1}]}`, ErrInvalidValue},
		{"bad task type", `{"v":1,"lamport":1,"ops":[{"kind":"set_type","type":"chore"}]}`, ErrInvalidValue},
		{"bad anchor kind", `{"v":1,"lamport":1,"ops":[{"kind":"add_anchor","anchor":{"kind":"url","value":"https://x"}}]}`, ErrInvalidValue},
		{"bad anchor kind in create_note", `{"v":1,"lamport":1,"ops":[{"kind":"create_note","nonce":"00","title":"t","body":"","tags":[],"anchors":[{"kind":"tag","value":"v"}]}]}`, ErrInvalidValue},
		{"bad priority in create_task", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"task","priority":7,"branch":"main","parent":"","labels":[]}]}`, ErrInvalidValue},
		{"bad type in create_task", `{"v":1,"lamport":1,"ops":[{"kind":"create_task","nonce":"00","title":"t","description":"","type":"story","priority":0,"branch":"main","parent":"","labels":[]}]}`, ErrInvalidValue},
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
