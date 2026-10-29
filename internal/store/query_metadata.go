package store

import (
	"slices"

	"github.com/yasyf/cc-notes/model"
)

const queryMetadataVersion = 1

type queryMetadata struct {
	Version      int              `json:"version"`
	Binding      string           `json:"binding"`
	Tip          model.SHA        `json:"tip"`
	ID           model.EntityID   `json:"id"`
	Kind         model.Kind       `json:"kind"`
	CreatedAt    int64            `json:"created_at"`
	UpdatedAt    int64            `json:"updated_at"`
	Deleted      bool             `json:"deleted"`
	Labels       []string         `json:"labels"`
	Anchors      []model.Anchor   `json:"anchors"`
	SupersededBy []model.EntityID `json:"superseded_by"`
	Branch       model.Branch     `json:"branch"`
	SkippedOps   int              `json:"skipped_ops"`
}

func projectQueryMetadata(binding string, tip model.SHA, snapshot model.Snapshot) queryMetadata {
	meta := snapshot.Meta()
	row := queryMetadata{
		Version:    queryMetadataVersion,
		Binding:    binding,
		Tip:        tip,
		ID:         snapshot.EntityID(),
		Kind:       meta.Kind,
		CreatedAt:  meta.CreatedAt.Unix(),
		UpdatedAt:  meta.UpdatedAt.Unix(),
		Deleted:    meta.Deleted,
		Labels:     slices.Clone(meta.Labels),
		SkippedOps: meta.SkippedOps,
	}
	var anchors []model.Anchor
	var supersededBy []model.EntityID
	switch value := snapshot.(type) {
	case model.Note:
		anchors, supersededBy = value.Anchors, value.SupersededBy
	case model.Doc:
		anchors, supersededBy = value.Anchors, value.SupersededBy
	case model.Answer:
		anchors, supersededBy = value.Anchors, value.SupersededBy
	case model.Log:
		anchors = value.Anchors
	case model.Task:
		anchors, row.Branch = value.Anchors, value.Branch
	case model.Sprint, model.Project:
	case model.Runbook:
		anchors = value.Anchors
	case model.Ledger:
		anchors = value.Anchors
	case model.Investigation:
		anchors, supersededBy = value.Anchors, value.SupersededBy
	case model.Plan:
		anchors, supersededBy = value.Anchors, value.SupersededBy
	default:
		panic("unsupported query metadata snapshot")
	}
	row.Anchors = slices.Clone(anchors)
	row.SupersededBy = slices.Clone(supersededBy)
	return row
}
