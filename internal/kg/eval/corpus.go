package eval

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// Entity is one retrievable cc-notes record flattened for scoring: its id and
// kind, the title, the concatenated long-form text a retriever matches against,
// the folded tag or label set, and the superseding edges the superseded-leak
// metric reads.
type Entity struct {
	ID           model.EntityID
	Kind         model.Kind
	Title        string
	Body         string
	Tags         []string
	UpdatedAt    int64
	SupersededBy []model.EntityID
}

// Text is the searchable text of the entity: kind, title, tags, and body.
func (e Entity) Text() string {
	return strings.Join([]string{string(e.Kind), e.Title, strings.Join(e.Tags, " "), e.Body}, "\n")
}

// LoadCorpus folds every cc-notes entity in the repository the client is open
// over into a scoring corpus, sorted by id. Superseded notes and docs and
// archived runbooks are included: a retriever that surfaces them is what the
// superseded-leak metric measures. Tombstoned records are not.
func LoadCorpus(ctx context.Context, c *notes.Client) ([]Entity, error) {
	var out []Entity

	ns, err := c.Notes(ctx, notes.DocumentFilter{IncludeSuperseded: true})
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	for _, n := range ns {
		out = append(out, Entity{
			ID: n.ID, Kind: model.KindNote, Title: n.Title, Body: n.Body,
			Tags: n.Tags, UpdatedAt: n.UpdatedAt, SupersededBy: n.SupersededBy,
		})
	}

	docs, err := c.Docs(ctx, notes.DocumentFilter{IncludeSuperseded: true})
	if err != nil {
		return nil, fmt.Errorf("list docs: %w", err)
	}
	for _, d := range docs {
		out = append(out, Entity{
			ID: d.ID, Kind: model.KindDoc, Title: d.Title, Body: join(d.When, d.Body),
			Tags: d.Tags, UpdatedAt: d.UpdatedAt, SupersededBy: d.SupersededBy,
		})
	}

	logs, err := c.Logs(ctx, notes.LogFilter{})
	if err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	for _, l := range logs {
		out = append(out, Entity{
			ID: l.ID, Kind: model.KindLog, Title: l.Title, Body: entryText(l.Entries),
			Tags: l.Tags, UpdatedAt: l.UpdatedAt,
		})
	}

	tasks, err := c.Tasks(ctx, notes.TaskFilter{Scope: notes.ScopeAllBranches})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	for _, t := range tasks {
		out = append(out, Entity{
			ID: t.ID, Kind: model.KindTask, Title: t.Title,
			Body: join(t.Description, commentText(t.Comments), criterionText(t.Criteria)),
			Tags: t.Labels, UpdatedAt: t.UpdatedAt,
		})
	}

	sprints, err := c.Sprints(ctx, notes.SprintFilter{})
	if err != nil {
		return nil, fmt.Errorf("list sprints: %w", err)
	}
	for _, s := range sprints {
		out = append(out, Entity{
			ID: s.ID, Kind: model.KindSprint, Title: s.Title,
			Body: join(s.Description, commentText(s.Comments)),
			Tags: s.Labels, UpdatedAt: s.UpdatedAt,
		})
	}

	projects, err := c.Projects(ctx, notes.ProjectFilter{})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		out = append(out, Entity{
			ID: p.ID, Kind: model.KindProject, Title: p.Title,
			Body: join(p.Description, commentText(p.Comments)),
			Tags: p.Labels, UpdatedAt: p.UpdatedAt,
		})
	}

	runbooks, err := c.Runbooks(ctx, notes.RunbookFilter{IncludeArchived: true})
	if err != nil {
		return nil, fmt.Errorf("list runbooks: %w", err)
	}
	for _, rb := range runbooks {
		out = append(out, Entity{
			ID: rb.ID, Kind: model.KindRunbook, Title: rb.Title,
			Body: join(rb.Description, stepText(rb.Steps), commentText(rb.Comments)),
			Tags: rb.Labels, UpdatedAt: rb.UpdatedAt,
		})
	}

	invs, err := c.Investigations(ctx, notes.InvestigationFilter{})
	if err != nil {
		return nil, fmt.Errorf("list investigations: %w", err)
	}
	for _, iv := range invs {
		out = append(out, Entity{
			ID: iv.ID, Kind: model.KindInvestigation, Title: iv.Title,
			Body: join(iv.Premise, iv.Body, iv.RootCause, findingText(iv.Findings), entryText(iv.Entries)),
			Tags: iv.Tags, UpdatedAt: iv.UpdatedAt, SupersededBy: iv.SupersededBy,
		})
	}

	slices.SortFunc(out, func(a, b Entity) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

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

// Tokenize lowercases and splits text into alphanumeric runs. It is the one
// tokenization every scorer over this corpus shares.
func Tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
