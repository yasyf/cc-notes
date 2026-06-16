// Package model defines the cc-notes vocabulary: identifiers, enums, the
// operation union, the pack wire codec, and folded entity snapshots. It
// depends only on the standard library; every other internal package builds
// on it.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// SHA is the full hex object id of a git commit.
type SHA string

// EntityID identifies one note or task: the full hex oid of the root commit
// of its chain. It never changes across merges or branch reassignments.
type EntityID string

// Short returns the 7-character display prefix of the id.
func (id EntityID) Short() string { return string(id)[:7] }

// Branch names a git branch: the part after refs/heads/.
type Branch string

func (b Branch) validate() error {
	if !refNameValid(string(b)) {
		return fmt.Errorf("%w: branch %q", ErrInvalidValue, b)
	}
	return nil
}

// refNameValid reports whether name passes git's check-ref-format rules for
// a branch name: non-empty components, no component starting with '.' or
// ending in '.lock', no '..', no ASCII control characters, space, or any of
// ~^:?*[\, no trailing '.', no '@{', and not the single character '@'.
func refNameValid(name string) bool {
	if name == "" || name == "@" || strings.HasSuffix(name, ".") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\\x7f") {
		return false
	}
	for i := range len(name) {
		if name[i] < 0x20 {
			return false
		}
	}
	for component := range strings.SplitSeq(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

// Actor identifies the author of an operation, taken from the git identity.
type Actor string

// Lamport is a per-entity logical clock: tip+1 on append, max(parents)+1 on
// merge.
type Lamport uint64

// Status is the lifecycle state of a task.
type Status string

// Task lifecycle states.
const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusCancelled  Status = "cancelled"
)

func (s Status) validate() error {
	switch s {
	case StatusOpen, StatusInProgress, StatusDone, StatusCancelled:
		return nil
	}
	return fmt.Errorf("%w: status %q", ErrInvalidValue, s)
}

// TaskType categorizes a task.
type TaskType string

// Task categories.
const (
	TypeTask     TaskType = "task"
	TypeBug      TaskType = "bug"
	TypeEpic     TaskType = "epic"
	TypeQuestion TaskType = "question"
)

func (t TaskType) validate() error {
	switch t {
	case TypeTask, TypeBug, TypeEpic, TypeQuestion:
		return nil
	}
	return fmt.Errorf("%w: task type %q", ErrInvalidValue, t)
}

// Priority ranks a task from 0 (P0, most urgent) through 3.
type Priority int

const maxPriority = 3

func (p Priority) validate() error {
	if p < 0 || p > maxPriority {
		return fmt.Errorf("%w: priority %d", ErrInvalidValue, p)
	}
	return nil
}

// AnchorKind discriminates what an Anchor points at.
type AnchorKind string

// Anchor kinds.
const (
	AnchorCommit AnchorKind = "commit"
	AnchorPath   AnchorKind = "path"
	AnchorBranch AnchorKind = "branch"
)

func (k AnchorKind) validate() error {
	switch k {
	case AnchorCommit, AnchorPath, AnchorBranch:
		return nil
	}
	return fmt.Errorf("%w: anchor kind %q", ErrInvalidValue, k)
}

// Anchor pins a note to a location in the repository: a commit, a file path,
// or a branch.
type Anchor struct {
	Kind  AnchorKind `json:"kind"`
	Value string     `json:"value"`
}

// Comment is one append-only comment on a task. TS is unix seconds.
type Comment struct {
	Author Actor  `json:"author"`
	TS     int64  `json:"ts"`
	Body   string `json:"body"`
}

// Note is the folded snapshot of a note entity. Timestamps are unix seconds;
// rendering to RFC3339 happens at output time. Tags is sorted; Head is the
// chain tip the snapshot was folded from.
type Note struct {
	ID        EntityID `json:"id"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Tags      []string `json:"tags"`
	Anchors   []Anchor `json:"anchors"`
	Author    Actor    `json:"author"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
	Deleted   bool     `json:"deleted"`
	Head      SHA      `json:"head"`
}

// Task is the folded snapshot of a task entity. Timestamps are unix seconds;
// zero means unset for StartedAt and ClosedAt, and an empty Parent or
// Assignee means none. Labels and BlockedBy are sorted; Head is the chain tip
// the snapshot was folded from.
type Task struct {
	ID          EntityID   `json:"id"`
	Branch      Branch     `json:"branch"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Type        TaskType   `json:"type"`
	Status      Status     `json:"status"`
	Priority    Priority   `json:"priority"`
	Assignee    Actor      `json:"assignee"`
	Labels      []string   `json:"labels"`
	BlockedBy   []EntityID `json:"blocked_by"`
	Parent      EntityID   `json:"parent"`
	Comments    []Comment  `json:"comments"`
	CreatedAt   int64      `json:"created_at"`
	UpdatedAt   int64      `json:"updated_at"`
	StartedAt   int64      `json:"started_at"`
	ClosedAt    int64      `json:"closed_at"`
	Head        SHA        `json:"head"`
}

// NewNonce returns 16 crypto/rand bytes hex-encoded (32 characters). Create
// ops embed a nonce so otherwise-identical creates hash to distinct entity
// ids.
func NewNonce() string {
	b := make([]byte, 16)
	rand.Read(b) // never fails: crypto/rand.Read panics internally instead of returning an error
	return hex.EncodeToString(b)
}
