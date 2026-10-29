package store

import (
	"slices"

	"github.com/yasyf/cc-notes/model"
)

const queryMetadataVersion = 2

type queryMetadata struct {
	Version             int                       `json:"version"`
	Binding             string                    `json:"binding"`
	Tip                 model.SHA                 `json:"tip"`
	ID                  model.EntityID            `json:"id"`
	Kind                model.Kind                `json:"kind"`
	CreatedAt           int64                     `json:"created_at"`
	UpdatedAt           int64                     `json:"updated_at"`
	Deleted             bool                      `json:"deleted"`
	Labels              []string                  `json:"labels"`
	Anchors             []model.Anchor            `json:"anchors"`
	SupersededBy        []model.EntityID          `json:"superseded_by"`
	Branch              model.Branch              `json:"branch"`
	SkippedOps          int                       `json:"skipped_ops"`
	Witness             []model.AnchorWitness     `json:"witness,omitempty"`
	VerifiedAt          int64                     `json:"verified_at,omitempty"`
	StaleAt             int64                     `json:"stale_at,omitempty"`
	TaskStatus          model.Status              `json:"task_status,omitempty"`
	Priority            model.Priority            `json:"priority,omitempty"`
	Assignee            model.Actor               `json:"assignee,omitempty"`
	BlockedBy           []model.EntityID          `json:"blocked_by,omitempty"`
	HasRunningRun       bool                      `json:"has_running_run,omitempty"`
	LogEntryCount       int                       `json:"log_entry_count,omitempty"`
	InvestigationStatus model.InvestigationStatus `json:"investigation_status,omitempty"`
	OpenFindingCount    int                       `json:"open_finding_count,omitempty"`
	PlanStatus          model.PlanStatus          `json:"plan_status,omitempty"`
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
	var witness []model.AnchorWitness
	switch value := snapshot.(type) {
	case model.Note:
		anchors, supersededBy = value.Anchors, value.SupersededBy
		witness, row.VerifiedAt, row.StaleAt = value.Witness, value.VerifiedAt, value.StaleAt
	case model.Doc:
		anchors, supersededBy = value.Anchors, value.SupersededBy
		witness, row.VerifiedAt, row.StaleAt = value.Witness, value.VerifiedAt, value.StaleAt
	case model.Answer:
		anchors, supersededBy = value.Anchors, value.SupersededBy
		witness, row.VerifiedAt, row.StaleAt = value.Witness, value.VerifiedAt, value.StaleAt
	case model.Log:
		anchors = value.Anchors
		row.LogEntryCount = len(value.Entries)
	case model.Task:
		anchors, row.Branch = value.Anchors, value.Branch
		row.TaskStatus, row.Priority, row.Assignee = value.Status, value.Priority, value.Assignee
		row.BlockedBy = slices.Clone(value.BlockedBy)
	case model.Sprint, model.Project:
	case model.Runbook:
		anchors = value.Anchors
		for _, run := range value.Runs {
			if run.Status == model.RunRunning {
				row.HasRunningRun = true
				break
			}
		}
	case model.Ledger:
		anchors = value.Anchors
	case model.Investigation:
		anchors, supersededBy = value.Anchors, value.SupersededBy
		row.InvestigationStatus = value.Status
		for _, finding := range value.Findings {
			if finding.Status == model.FindingOpen {
				row.OpenFindingCount++
			}
		}
	case model.Plan:
		anchors, supersededBy = value.Anchors, value.SupersededBy
		row.PlanStatus = value.Status
	default:
		panic("unsupported query metadata snapshot")
	}
	row.Anchors = slices.Clone(anchors)
	row.SupersededBy = slices.Clone(supersededBy)
	row.Witness = slices.Clone(witness)
	return row
}
