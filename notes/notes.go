// Package notes is the public, in-process Go API for cc-notes: open a Client
// over a git repository and drive projects, sprints, and tasks without
// shelling out to the cc-notes CLI. Its methods return the same folded
// model.Project/Sprint/Task snapshots the CLI prints. The API reaches
// cc-notes' entity store directly and never touches FUSE or cgo, so a consumer
// can link it into a static, CGO-free binary.
package notes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

// DefaultArchiveAge is how long a closed task stays out of the archive view: a
// done or cancelled task is archived only once it has been closed at least this
// long.
const DefaultArchiveAge = 720 * time.Hour

const (
	defaultRemote = "origin"

	leaseTTLEnv     = "CC_NOTES_LEASE_TTL"
	leaseTTLConfig  = "cc-notes.leaseTTL"
	defaultLeaseTTL = time.Hour

	noteStaleAfterEnv     = "CC_NOTES_NOTE_STALE_AFTER"
	noteStaleAfterConfig  = "cc-notes.noteStaleAfter"
	defaultNoteStaleAfter = 90 * 24 * time.Hour
)

// Client drives one repository's cc-notes entities. Construct it with Open.
type Client struct {
	s   *store.Store
	dir string
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
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &Client{s: s, dir: abs}, nil
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

// currentBranchOrBacklog resolves the repository's current branch, jj-colocation
// aware. A detached HEAD with no resolvable branch reports backlog=true — the
// signal to degrade onto the empty-branch backlog rather than error.
func (c *Client) currentBranchOrBacklog(ctx context.Context) (model.Branch, bool, error) {
	branch, err := c.s.Git.CurrentBranch(ctx)
	if errors.Is(err, ErrDetachedHead) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	return branch, false, nil
}

// head returns the repository's HEAD commit, or "" on an unborn branch.
func (c *Client) head(ctx context.Context) (model.SHA, error) {
	head, err := c.s.Repo.Tip(ctx, "HEAD")
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return head, nil
}

// LeaseTTL resolves the task-lease staleness threshold with precedence
// env > git config > 1h default: CC_NOTES_LEASE_TTL overrides the last
// cc-notes.leaseTTL git config value. A malformed value is a plain error.
func (c *Client) LeaseTTL(ctx context.Context) (time.Duration, error) {
	if value, ok := os.LookupEnv(leaseTTLEnv); ok {
		return parseDuration(value)
	}
	values, err := c.s.Git.ConfigGetAll(ctx, leaseTTLConfig)
	if err != nil {
		return 0, err
	}
	if len(values) > 0 {
		return parseDuration(values[len(values)-1])
	}
	return defaultLeaseTTL, nil
}

// NoteStaleAfter resolves the note staleness threshold with precedence
// env > git config > 90d default: CC_NOTES_NOTE_STALE_AFTER overrides the last
// cc-notes.noteStaleAfter git config value. A malformed value is a plain error.
func (c *Client) NoteStaleAfter(ctx context.Context) (time.Duration, error) {
	if value, ok := os.LookupEnv(noteStaleAfterEnv); ok {
		return parseDuration(value)
	}
	values, err := c.s.Git.ConfigGetAll(ctx, noteStaleAfterConfig)
	if err != nil {
		return 0, err
	}
	if len(values) > 0 {
		return parseDuration(values[len(values)-1])
	}
	return defaultNoteStaleAfter, nil
}

// parseDuration parses a threshold value, returning a plain error naming the
// bad value — presentation (UsageError, exit-2 mapping) stays with the caller.
func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// deriveRemote resolves the remote a best-effort sync or install targets when
// none is named: the sole cc-notes-wired remote when exactly one is wired, else
// the default remote. WiredRemotes failures propagate.
func (c *Client) deriveRemote(ctx context.Context) (string, error) {
	wired, err := ccsync.WiredRemotes(ctx, c.s.Git)
	if err != nil {
		return "", err
	}
	if len(wired) == 1 {
		return wired[0], nil
	}
	return defaultRemote, nil
}
