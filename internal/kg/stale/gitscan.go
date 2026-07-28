package stale

import (
	"context"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

// touch is one file's line churn in one commit.
type touch struct {
	Path  string
	TS    int64
	Lines int
}

// churnLog flattens every tracked-file touch since into one slice, so a
// whole-corpus assessment costs one git invocation rather than one per anchor.
func churnLog(ctx context.Context, g gitcmd.Git, since time.Time) ([]touch, error) {
	commits, err := g.NumstatLog(ctx, gitcmd.NumstatScope{Since: since})
	if err != nil {
		return nil, err
	}
	var touches []touch
	for _, c := range commits {
		for _, f := range c.Files {
			touches = append(touches, touch{Path: f.Path, TS: c.Time.Unix(), Lines: f.Added + f.Deleted})
		}
	}
	return touches, nil
}
