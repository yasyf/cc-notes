package kg

import (
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

// join concatenates the non-empty parts, one per line.
func join(parts ...string) string {
	kept := slices.DeleteFunc(slices.Clone(parts), func(s string) bool { return s == "" })
	return strings.Join(kept, "\n")
}

func entryText(entries []model.LogEntry) string {
	texts := make([]string, len(entries))
	for i, e := range entries {
		texts[i] = e.Text
	}
	return join(texts...)
}

func commentText(comments []model.Comment) string {
	texts := make([]string, len(comments))
	for i, c := range comments {
		texts[i] = c.Body
	}
	return join(texts...)
}

func criterionText(criteria []model.Criterion) string {
	texts := make([]string, 0, 2*len(criteria))
	for _, c := range criteria {
		texts = append(texts, c.Text, c.Note)
	}
	return join(texts...)
}

func findingText(findings []model.Finding) string {
	texts := make([]string, 0, 2*len(findings))
	for _, f := range findings {
		texts = append(texts, f.Text, f.Note)
	}
	return join(texts...)
}

func stepText(steps []model.RunbookStep) string {
	texts := make([]string, 0, 2*len(steps))
	for _, s := range steps {
		texts = append(texts, s.Text, s.Command)
	}
	return join(texts...)
}
