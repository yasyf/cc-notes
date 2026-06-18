// Package notes is the public, in-process Go API for cc-notes: open a Client
// over a git repository and drive projects, sprints, and tasks without
// shelling out to the cc-notes CLI. Its methods return the same folded
// model.Project/Sprint/Task snapshots the CLI prints. The API reaches
// cc-notes' entity store directly and never touches FUSE or cgo, so a consumer
// can link it into a static, CGO-free binary.
package notes

import (
	"context"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// Client drives one repository's cc-notes entities. Construct it with Open.
type Client struct {
	s *store.Store
}

// Open opens the cc-notes store for the git repository containing dir,
// following worktree and subdirectory indirection. It does not require the
// repository to hold any cc-notes entity yet. The author identity for writes
// resolves lazily on each write: the CC_NOTES_ACTOR environment variable
// ("Name <email>") when set, otherwise git's configured author identity.
func Open(dir string) (*Client, error) {
	s, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Client{s: s}, nil
}

// HasNotes reports whether the repository holds any cc-notes entity — any ref
// under refs/cc-notes/. A repository that has never used cc-notes returns
// false, the signal a caller uses to gate optional cc-notes wiring.
func (c *Client) HasNotes(ctx context.Context) (bool, error) {
	return c.s.HasNotes(ctx)
}

// Actor returns the identity that signs this client's writes — the
// CC_NOTES_ACTOR override when set, otherwise git's author identity — as
// "Name <email>".
func (c *Client) Actor(ctx context.Context) (model.Actor, error) {
	return c.s.Actor(ctx)
}
