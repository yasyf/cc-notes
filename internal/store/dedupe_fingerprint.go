package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/yasyf/cc-notes/model"
)

type duplicateContent struct {
	Version     int
	Kind        model.Kind
	Text        []string
	Numbers     []int64
	Groups      [][]string
	Anchors     []model.Anchor
	Attachments []model.Attachment
}

func duplicateSlice[T any](values []T) []T {
	return append([]T{}, values...)
}

func duplicateFingerprint(snapshot model.Snapshot) string {
	content := duplicateContent{Version: 1, Kind: snapshot.Meta().Kind}
	switch value := snapshot.(type) {
	case model.Note:
		content.Text = []string{value.Title, value.Body}
		content.Groups = [][]string{duplicateSlice(value.Tags)}
		content.Anchors, content.Attachments = duplicateSlice(value.Anchors), duplicateSlice(value.Attachments)
	case model.Doc:
		content.Text = []string{value.Title, value.Body, value.When}
		content.Groups = [][]string{duplicateSlice(value.Tags)}
		content.Anchors, content.Attachments = duplicateSlice(value.Anchors), duplicateSlice(value.Attachments)
	case model.Answer:
		content.Text = []string{value.Title, value.Body}
		content.Groups = [][]string{duplicateSlice(value.Tags)}
		content.Anchors, content.Attachments = duplicateSlice(value.Anchors), duplicateSlice(value.Attachments)
	case model.Log:
		content.Text = []string{value.Title}
		content.Groups = [][]string{duplicateSlice(value.Tags)}
		content.Anchors, content.Attachments = duplicateSlice(value.Anchors), duplicateSlice(value.Attachments)
	case model.Task:
		content.Text = []string{string(value.Branch), value.Title, value.Description, string(value.Type), string(value.Parent), string(value.Sprint), string(value.Project), string(value.Plan)}
		content.Numbers = []int64{int64(value.Priority)}
		criteria := make([]string, 0, len(value.Criteria)*2)
		for _, criterion := range value.Criteria {
			criteria = append(criteria, criterion.Text, criterion.Script)
		}
		dependencies := make([]string, 0, len(value.BlockedBy))
		for _, id := range value.BlockedBy {
			dependencies = append(dependencies, string(id))
		}
		content.Groups = [][]string{duplicateSlice(value.Labels), dependencies, criteria}
		content.Anchors = duplicateSlice(value.Anchors)
	case model.Sprint:
		content.Text = []string{string(value.Project), value.Title, value.Description}
		content.Numbers = []int64{value.StartDate, value.EndDate}
		content.Groups = [][]string{duplicateSlice(value.Labels)}
	case model.Project:
		content.Text = []string{value.Title, value.Description}
		content.Groups = [][]string{duplicateSlice(value.Labels)}
	case model.Runbook:
		content.Text = []string{value.Title, value.Description}
		steps := make([]string, 0, len(value.Steps)*2)
		for _, step := range value.Steps {
			steps = append(steps, step.Text, step.Command)
		}
		content.Groups = [][]string{duplicateSlice(value.Labels), steps}
		content.Anchors = duplicateSlice(value.Anchors)
	case model.Investigation:
		content.Text = []string{value.Title, value.Premise}
		content.Groups = [][]string{duplicateSlice(value.Tags)}
		content.Anchors, content.Attachments = duplicateSlice(value.Anchors), duplicateSlice(value.Attachments)
	case model.Plan:
		content.Text = []string{value.Title, value.Body, string(value.Status)}
		content.Groups = [][]string{duplicateSlice(value.Labels)}
		content.Anchors = duplicateSlice(value.Anchors)
	case model.Ledger:
		content.Text = []string{value.Title, value.Description}
		content.Groups = [][]string{duplicateSlice(value.Columns), duplicateSlice(value.Labels)}
		content.Anchors = duplicateSlice(value.Anchors)
	default:
		panic("unsupported duplicate snapshot")
	}
	data, err := json.Marshal(content)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
