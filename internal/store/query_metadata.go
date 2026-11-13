package store

import (
	"maps"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/answermatch"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
)

func queryMetadataVersion(kind model.Kind) int {
	if kind == model.KindAnswer {
		return 9
	}
	return 8
}

type queryMetadata struct {
	StatusRecord
	UpdatedTime         time.Time        `json:"updated_time"`
	AnswerQuestionTerms []string         `json:"answer_question_terms,omitempty"`
	AnswerBodyTerms     []string         `json:"answer_body_terms,omitempty"`
	TextDigest          string           `json:"text_digest,omitempty"`
	DuplicateContent    string           `json:"duplicate_content"`
	Priority            model.Priority   `json:"priority,omitempty"`
	Parent              model.EntityID   `json:"parent,omitempty"`
	RunTasks            []model.EntityID `json:"run_tasks,omitempty"`
}

func projectQueryMetadata(binding string, tip model.SHA, snapshot model.Snapshot) queryMetadata {
	meta := snapshot.Meta()
	row := queryMetadata{
		StatusRecord: StatusRecord{
			Version: queryMetadataVersion(meta.Kind), Binding: binding, Tip: tip,
			ID: snapshot.EntityID(), Kind: meta.Kind,
			CreatedAt: meta.CreatedAt.Unix(), UpdatedAt: meta.UpdatedAt.Unix(),
			Deleted: meta.Deleted, Labels: slices.Clone(meta.Labels), SkippedOps: meta.SkippedOps,
		},
		UpdatedTime:      meta.UpdatedAt,
		DuplicateContent: duplicateFingerprint(snapshot),
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
		row.AnswerQuestionTerms = slices.Sorted(maps.Keys(answermatch.QuestionTerms(value.Title, value.Tags)))
		row.AnswerBodyTerms = slices.Sorted(maps.Keys(answermatch.Terms(value.Body)))
		row.TextDigest = answerCorpusDigest([]byte(render.AnswerMarkdown(value.Title, value.Body)))
	case model.Log:
		anchors = value.Anchors
		row.LogEntryCount = len(value.Entries)
	case model.Task:
		anchors, row.Branch = value.Anchors, value.Branch
		row.TaskStatus, row.Priority, row.Assignee = value.Status, value.Priority, value.Assignee
		row.BlockedBy = slices.Clone(value.BlockedBy)
		row.Parent = value.Parent
	case model.Sprint, model.Project:
	case model.Runbook:
		anchors = value.Anchors
		row.RunbookStatus = value.Status
		for _, run := range value.Runs {
			row.RunTasks = append(row.RunTasks, run.Task)
			if run.Status == model.RunRunning {
				row.HasRunningRun = true
			}
		}
		slices.Sort(row.RunTasks)
		row.RunTasks = slices.Compact(row.RunTasks)
	case model.Ledger:
		anchors, row.LedgerStatus = value.Anchors, value.Status
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
