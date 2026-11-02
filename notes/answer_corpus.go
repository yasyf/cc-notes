package notes

import (
	"context"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// AnswerCorpusFilter selects durable answers by program label or branch anchor.
type AnswerCorpusFilter struct {
	Program  string
	Branch   string
	CacheDir string
}

// AnswerCorpusRecord describes a canonical Markdown document without its body.
type AnswerCorpusRecord = store.CorpusRecord

// AnswerCorpusCut owns one validated selection and its captured canonical words.
// Keep it open through ranking and Pick, and close it before releasing the client.
type AnswerCorpusCut struct {
	Binding    string
	Generation string
	Records    []AnswerCorpusRecord
	view       *store.AnswerCorpusCut
}

// OpenAnswerCorpus refreshes native metadata before capturing a single selection.
// Its owned read view remains valid after source writes, deletion, or pruning.
func (c *Client) OpenAnswerCorpus(ctx context.Context, f AnswerCorpusFilter) (*AnswerCorpusCut, error) {
	view, err := c.s.OpenAnswerCorpus(ctx, f.Program, f.Branch, f.CacheDir)
	if err != nil {
		return nil, err
	}
	return &AnswerCorpusCut{Binding: view.Binding, Generation: view.Generation, Records: view.Records, view: view}, nil
}

// Text reads captured Markdown only for a document the caller needs to index.
func (c *AnswerCorpusCut) Text(ctx context.Context, shortID string) (string, error) {
	return c.view.Text(ctx, shortID)
}

// Pick returns captured canonical answers in the requested hit order.
func (c *AnswerCorpusCut) Pick(ctx context.Context, shortIDs []string) ([]model.Answer, error) {
	return c.view.Pick(ctx, shortIDs)
}

// Close releases the owned read transaction and its database handles.
func (c *AnswerCorpusCut) Close() error {
	return c.view.Close()
}
