package store

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryMetadataProjection(t *testing.T) {
	labels := []string{"scope:durable", "label\x00with:delimiters"}
	anchors := []model.Anchor{
		{Kind: model.AnchorCommit, Value: "1234567890"},
		{Kind: model.AnchorPath, Value: "src/a.go"},
		{Kind: model.AnchorDir, Value: "src/"},
		{Kind: model.AnchorBranch, Value: "anchor-branch"},
	}
	targets := []model.EntityID{"replacement-a", "replacement-b"}
	cases := []struct {
		snapshot     model.Snapshot
		anchors      []model.Anchor
		supersededBy []model.EntityID
		branch       model.Branch
	}{
		{model.Note{ID: "note", Tags: labels, Anchors: anchors, SupersededBy: targets, CreatedAt: 3, UpdatedAt: 5, Deleted: true, SkippedOps: 2}, anchors, targets, ""},
		{model.Doc{ID: "doc", Tags: labels, Anchors: anchors, SupersededBy: targets, CreatedAt: 7, UpdatedAt: 11, Deleted: true, SkippedOps: 3}, anchors, targets, ""},
		{model.Answer{ID: "answer", Tags: labels, Anchors: anchors, SupersededBy: targets, CreatedAt: 13, UpdatedAt: 17, Deleted: true, SkippedOps: 4}, anchors, targets, ""},
		{model.Log{ID: "log", Tags: labels, Anchors: anchors, CreatedAt: 19, UpdatedAt: 23, Deleted: true, SkippedOps: 5}, anchors, nil, ""},
		{model.Task{ID: "task", Labels: labels, Anchors: anchors, Branch: "task-branch", CreatedAt: 29, UpdatedAt: 31, Deleted: true, SkippedOps: 6}, anchors, nil, "task-branch"},
		{model.Sprint{ID: "sprint", Labels: labels, CreatedAt: 37, UpdatedAt: 41, Deleted: true, SkippedOps: 7}, nil, nil, ""},
		{model.Project{ID: "project", Labels: labels, CreatedAt: 43, UpdatedAt: 47, Deleted: true, SkippedOps: 8}, nil, nil, ""},
		{model.Runbook{ID: "runbook", Labels: labels, Anchors: anchors, CreatedAt: 53, UpdatedAt: 59, Deleted: true, SkippedOps: 9}, anchors, nil, ""},
		{model.Ledger{ID: "ledger", Labels: labels, Anchors: anchors, CreatedAt: 61, UpdatedAt: 67, Deleted: true, SkippedOps: 10}, anchors, nil, ""},
		{model.Investigation{ID: "investigation", Tags: labels, Anchors: anchors, SupersededBy: targets, CreatedAt: 71, UpdatedAt: 73, Deleted: true, SkippedOps: 11}, anchors, targets, ""},
		{model.Plan{ID: "plan", Labels: labels, Anchors: anchors, SupersededBy: targets, CreatedAt: 79, UpdatedAt: 83, Deleted: true, SkippedOps: 12}, anchors, targets, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.snapshot.Meta().Kind), func(t *testing.T) {
			meta := tc.snapshot.Meta()
			got := projectQueryMetadata("records-binding", "captured-tip", tc.snapshot)
			want := queryMetadata{
				Version: queryMetadataVersion, Binding: "records-binding", Tip: "captured-tip",
				ID: tc.snapshot.EntityID(), Kind: meta.Kind,
				CreatedAt: meta.CreatedAt.Unix(), UpdatedAt: meta.UpdatedAt.Unix(),
				Deleted: meta.Deleted, Labels: labels, Anchors: tc.anchors,
				SupersededBy: tc.supersededBy, Branch: tc.branch, SkippedOps: meta.SkippedOps,
				DuplicateContent: duplicateFingerprint(tc.snapshot),
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("projection = %#v, want %#v", got, want)
			}
			for _, opts := range []ListOpts{{}, {IncludeDeleted: true}, {IncludeSuperseded: true}, {IncludeDeleted: true, IncludeSuperseded: true}} {
				projected := model.Meta{Deleted: got.Deleted, Superseded: len(got.SupersededBy) > 0}
				if keepInList(projected, opts) != keepInList(meta, opts) {
					t.Fatalf("lifecycle differs under %#v", opts)
				}
			}
		})
	}
}

func TestQueryMetadataOwnsCapturedValues(t *testing.T) {
	snapshot := model.Doc{
		ID: "id", Head: "snapshot-head", Tags: []string{"first"},
		Anchors:      []model.Anchor{{Kind: model.AnchorPath, Value: "original"}},
		SupersededBy: []model.EntityID{"original-target"},
	}
	got := projectQueryMetadata("captured-binding", "captured-tip", snapshot)
	snapshot.Tags[0] = "changed"
	snapshot.Anchors[0].Value = "changed"
	snapshot.SupersededBy[0] = "changed"
	if got.Binding != "captured-binding" || got.Tip != "captured-tip" || got.ID != "id" {
		t.Fatalf("captured source identity = %#v", got)
	}
	if !slices.Equal(got.Labels, []string{"first"}) || got.Anchors[0].Value != "original" || got.SupersededBy[0] != "original-target" {
		t.Fatalf("projection aliases mutable snapshot fields: %#v", got)
	}
}

func TestQueryMetadataRoundTripPreservesSkippedOps(t *testing.T) {
	snapshot := model.Answer{ID: "id", Body: "answer", SkippedOps: 3}
	original, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(original), "skipped_ops") {
		t.Fatal("snapshot unexpectedly persists reader-local skipped ops")
	}
	row := projectQueryMetadata("binding", "tip", snapshot)
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var restored queryMetadata
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, row) || restored.SkippedOps != 3 {
		t.Fatalf("roundtrip = %#v, want %#v", restored, row)
	}
}

func TestQueryMetadataSizeDoesNotDependOnDocumentBody(t *testing.T) {
	snapshot := model.Doc{ID: "id", Title: "title", Body: "short", When: "trigger"}
	short, err := json.Marshal(projectQueryMetadata("binding", "tip", snapshot))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Title = strings.Repeat("title", 10000)
	snapshot.Body = strings.Repeat("body", 1<<18)
	snapshot.When = strings.Repeat("trigger", 10000)
	large, err := json.Marshal(projectQueryMetadata("binding", "tip", snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if string(short) != string(large) || len(large) > 512 {
		t.Fatalf("metadata depends on document payload: short=%d large=%d", len(short), len(large))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(large, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"version", "binding", "tip", "id", "kind", "created_at", "updated_at", "deleted", "labels", "anchors", "superseded_by", "branch", "skipped_ops"}
	if len(fields) != len(want) {
		t.Fatalf("metadata fields = %v, want %v", fields, want)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Errorf("metadata omits %q", key)
		}
	}
}

func TestQueryMetadataEmptyCollectionsRemainEmpty(t *testing.T) {
	row := projectQueryMetadata("binding", "tip", model.Doc{ID: "id"})
	if row.Labels != nil || row.Anchors != nil || row.SupersededBy != nil {
		t.Fatalf("empty projection = %#v", row)
	}
}
