// Package model defines the cc-notes vocabulary: identifiers, enums, the
// operation union, the pack wire codec, and folded entity snapshots. It
// depends only on the standard library; every other internal package builds
// on it.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
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

// SprintStatus is the lifecycle state of a sprint.
type SprintStatus string

// Sprint lifecycle states.
const (
	SprintPlanned   SprintStatus = "planned"
	SprintActive    SprintStatus = "active"
	SprintCompleted SprintStatus = "completed"
	SprintCancelled SprintStatus = "cancelled"
)

func (s SprintStatus) validate() error {
	switch s {
	case SprintPlanned, SprintActive, SprintCompleted, SprintCancelled:
		return nil
	}
	return fmt.Errorf("%w: sprint status %q", ErrInvalidValue, s)
}

// ProjectStatus is the lifecycle state of a project.
type ProjectStatus string

// Project lifecycle states.
const (
	ProjectActive    ProjectStatus = "active"
	ProjectCompleted ProjectStatus = "completed"
	ProjectArchived  ProjectStatus = "archived"
	ProjectCancelled ProjectStatus = "cancelled"
)

func (s ProjectStatus) validate() error {
	switch s {
	case ProjectActive, ProjectCompleted, ProjectArchived, ProjectCancelled:
		return nil
	}
	return fmt.Errorf("%w: project status %q", ErrInvalidValue, s)
}

// CriterionStatus is the validation state of a task acceptance criterion.
type CriterionStatus string

// Criterion validation states.
const (
	CriterionPending CriterionStatus = "pending"
	CriterionMet     CriterionStatus = "met"
	CriterionFailed  CriterionStatus = "failed"
)

func (s CriterionStatus) validate() error {
	switch s {
	case CriterionPending, CriterionMet, CriterionFailed:
		return nil
	}
	return fmt.Errorf("%w: criterion status %q", ErrInvalidValue, s)
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
	AnchorDir    AnchorKind = "dir"
)

func (k AnchorKind) validate() error {
	switch k {
	case AnchorCommit, AnchorPath, AnchorBranch, AnchorDir:
		return nil
	}
	return fmt.Errorf("%w: anchor kind %q", ErrInvalidValue, k)
}

// Anchor pins a note to a location in the repository: a commit, a file path,
// a directory, or a branch.
type Anchor struct {
	Kind  AnchorKind `json:"kind"`
	Value string     `json:"value"`
}

// AnchorWitness records the git oid of an anchor's content at verify time, so
// the reader can detect drift without storing the verdict.
type AnchorWitness struct {
	Anchor Anchor `json:"anchor"`
	OID    SHA    `json:"oid"`
}

// Comment is one append-only comment on a task. TS is unix seconds.
type Comment struct {
	Author Actor  `json:"author"`
	TS     int64  `json:"ts"`
	Body   string `json:"body"`
}

// LogEntry is one append-only entry in a log: a timestamped, authored fact that
// never moves or changes once written. Author and TS come from the carrying
// commit's identity; TS is unix seconds.
type LogEntry struct {
	Author Actor  `json:"author"`
	TS     int64  `json:"ts"`
	Text   string `json:"text"`
}

// Criterion is one structured acceptance criterion on a task. ID is a nonce
// stable within the task; Script is an optional check command ("" means none);
// Status is the latest validation verdict.
type Criterion struct {
	ID     string          `json:"id"`
	Text   string          `json:"text"`
	Script string          `json:"script"`
	Status CriterionStatus `json:"status"`
}

// maxAttachmentNameBytes bounds an attachment name to a single filesystem
// name component.
const maxAttachmentNameBytes = 255

// attachmentOIDRE matches a git-lfs object id: the sha256 of the content,
// 64 lower-hex characters.
var attachmentOIDRE = regexp.MustCompile(`\A[0-9a-f]{64}\z`)

// ValidAttachmentOID reports whether oid is a well-formed git-lfs object id:
// the sha256 of the content, 64 lower-hex characters.
func ValidAttachmentOID(oid string) bool {
	return attachmentOIDRE.MatchString(oid)
}

// Attachment is one named large-content reference on a note, doc, or log.
// Name is the file name, unique per entity (attachments resolve LWW by Name
// at fold time); OID is the git-lfs object id (sha256 of the content, 64
// lower hex); Size is the content length in bytes, always positive — git-lfs
// never stores the empty object. The bytes themselves live in the local LFS
// object store and move over the LFS batch API at sync time, never in the
// entity chain.
type Attachment struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

func (a Attachment) validate() error {
	if err := validateAttachmentName(a.Name); err != nil {
		return err
	}
	if !attachmentOIDRE.MatchString(a.OID) {
		return fmt.Errorf("%w: attachment oid %q", ErrInvalidValue, a.OID)
	}
	if a.Size <= 0 {
		return fmt.Errorf("%w: attachment size %d", ErrInvalidValue, a.Size)
	}
	return nil
}

// validateAttachmentName enforces that name is one safe filesystem component:
// non-empty, at most 255 bytes, not "." or "..", and free of '/' and control
// characters.
func validateAttachmentName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > maxAttachmentNameBytes {
		return fmt.Errorf("%w: attachment name %q", ErrInvalidValue, name)
	}
	for i := range len(name) {
		if name[i] < 0x20 || name[i] == 0x7f || name[i] == '/' {
			return fmt.Errorf("%w: attachment name %q", ErrInvalidValue, name)
		}
	}
	return nil
}

// Note is the folded snapshot of a note entity. Timestamps are unix seconds;
// rendering to RFC3339 happens at output time. Tags is sorted; Head is the
// chain tip the snapshot was folded from.
//
// VerifiedAt, VerifiedBy, VerifiedCommit, and Witness record the latest
// verify_note: when and by whom the note was last reconfirmed true, the HEAD
// commit it was checked against, and the per-anchor content witness. A
// never-verified note has VerifiedAt==0. Witness comes back ordered to match
// the anchors it was computed over (not re-sorted); SupersededBy comes back as
// a sorted slice of the notes that replace this one. A note with any
// SupersededBy edge is a soft tombstone. Drift and staleness verdicts are not
// stored — the reader computes them from Witness and VerifiedAt at query time.
//
// StaleAt, StaleBy, and StaleReason record an explicit agent-asserted
// out-of-date flag (who and when from the commit, with an optional reason);
// StaleAt==0 means not flagged, and both clear_stale and verify_note clear it.
//
// Attachments is the folded attachment set, LWW by Name, sorted by Name, and
// nil when empty — the field marshals omitempty so attachment-less snapshots
// keep their pre-attachment bytes.
type Note struct {
	ID             EntityID        `json:"id"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	Tags           []string        `json:"tags"`
	Anchors        []Anchor        `json:"anchors"`
	Author         Actor           `json:"author"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
	Deleted        bool            `json:"deleted"`
	VerifiedAt     int64           `json:"verified_at"`
	VerifiedBy     Actor           `json:"verified_by"`
	VerifiedCommit SHA             `json:"verified_commit"`
	Witness        []AnchorWitness `json:"witness"`
	SupersededBy   []EntityID      `json:"superseded_by"`
	StaleAt        int64           `json:"stale_at"`
	StaleBy        Actor           `json:"stale_by"`
	StaleReason    string          `json:"stale_reason"`
	Head           SHA             `json:"head"`
	Attachments    []Attachment    `json:"attachments,omitempty"`
}

// Doc is the folded snapshot of a doc entity: a long-form markdown document
// written for future agents. It carries the full Note freshness lifecycle
// (verify/witness/expire/supersede) plus a free-text When trigger surfaced
// verbatim by relevance. Timestamps are unix seconds; rendering to RFC3339
// happens at output time. Tags is sorted; Head is the chain tip the snapshot
// was folded from.
//
// When is the free-text "read this when…" trigger, an LWW scalar surfaced
// verbatim by relevance ranking.
//
// VerifiedAt, VerifiedBy, VerifiedCommit, and Witness record the latest
// verify: when and by whom the doc was last reconfirmed true, the HEAD commit
// it was checked against, and the per-anchor content witness. A never-verified
// doc has VerifiedAt==0. Witness comes back ordered to match the anchors it was
// computed over (not re-sorted); SupersededBy comes back as a sorted slice of
// the docs that replace this one. A doc with any SupersededBy edge is a soft
// tombstone. Drift and staleness verdicts are not stored — the reader computes
// them from Witness and VerifiedAt at query time.
//
// StaleAt, StaleBy, and StaleReason record an explicit agent-asserted
// out-of-date flag (who and when from the commit, with an optional reason);
// StaleAt==0 means not flagged, and both clear_stale and verify_note clear it.
//
// Attachments is the folded attachment set, LWW by Name, sorted by Name, and
// nil when empty — the field marshals omitempty so attachment-less snapshots
// keep their pre-attachment bytes.
type Doc struct {
	ID             EntityID        `json:"id"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	When           string          `json:"when"`
	Tags           []string        `json:"tags"`
	Anchors        []Anchor        `json:"anchors"`
	Author         Actor           `json:"author"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
	Deleted        bool            `json:"deleted"`
	VerifiedAt     int64           `json:"verified_at"`
	VerifiedBy     Actor           `json:"verified_by"`
	VerifiedCommit SHA             `json:"verified_commit"`
	Witness        []AnchorWitness `json:"witness"`
	SupersededBy   []EntityID      `json:"superseded_by"`
	StaleAt        int64           `json:"stale_at"`
	StaleBy        Actor           `json:"stale_by"`
	StaleReason    string          `json:"stale_reason"`
	Head           SHA             `json:"head"`
	Attachments    []Attachment    `json:"attachments,omitempty"`
}

// Log is the folded snapshot of a log entity: an append-only journal — an
// incident timeline, a rollout log, a debugging-session record — written for
// future agents. Each entry, once written, never moves or changes; the only
// mutation is to append. Like a Doc it carries Tags and Anchors and is surfaced
// by relevance, but it inherits none of the freshness lifecycle (no
// verify/witness/expire/supersede). Timestamps are unix seconds; rendering to
// RFC3339 happens at output time. Tags is sorted; Head is the chain tip the
// snapshot was folded from.
//
// Entries is the ordered list of log entries in linearization order
// (lamport → author-time → sha), the same order Task comments fold in. The
// fold is pure concatenation, so cross-branch sync converges with no reconcile.
//
// Attachments is the folded attachment set, LWW by Name, sorted by Name, and
// nil when empty — the field marshals omitempty so attachment-less snapshots
// keep their pre-attachment bytes.
type Log struct {
	ID          EntityID     `json:"id"`
	Title       string       `json:"title"`
	Entries     []LogEntry   `json:"entries"`
	Tags        []string     `json:"tags"`
	Anchors     []Anchor     `json:"anchors"`
	Author      Actor        `json:"author"`
	CreatedAt   int64        `json:"created_at"`
	UpdatedAt   int64        `json:"updated_at"`
	Deleted     bool         `json:"deleted"`
	Head        SHA          `json:"head"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Task is the folded snapshot of a task entity. Timestamps are unix seconds;
// zero means unset for StartedAt and ClosedAt, and an empty Parent or
// Assignee means none. Labels, BlockedBy, and Commits are sorted; Head is the
// chain tip the snapshot was folded from.
//
// HeartbeatAt and HeartbeatLamport are the lease heartbeat: the AuthorTime and
// lamport of the assignee's latest op (any edit, comment, claim, or renew the
// assignee authored). Both are zero before any claim. Commits is the sorted set
// of commit shas that implement the task (the task->commit direction).
type Task struct {
	ID               EntityID    `json:"id"`
	Branch           Branch      `json:"branch"`
	Title            string      `json:"title"`
	Description      string      `json:"description"`
	Type             TaskType    `json:"type"`
	Status           Status      `json:"status"`
	Priority         Priority    `json:"priority"`
	Assignee         Actor       `json:"assignee"`
	HeartbeatAt      int64       `json:"heartbeat_at"`
	HeartbeatLamport Lamport     `json:"heartbeat_lamport"`
	Labels           []string    `json:"labels"`
	BlockedBy        []EntityID  `json:"blocked_by"`
	Parent           EntityID    `json:"parent"`
	Comments         []Comment   `json:"comments"`
	CreatedAt        int64       `json:"created_at"`
	UpdatedAt        int64       `json:"updated_at"`
	StartedAt        int64       `json:"started_at"`
	ClosedAt         int64       `json:"closed_at"`
	Commits          []SHA       `json:"commits"`
	Head             SHA         `json:"head"`
	Sprint           EntityID    `json:"sprint"`   // LWW membership, empty means none
	Project          EntityID    `json:"project"`  // LWW membership, empty means none (independent of Sprint)
	Criteria         []Criterion `json:"criteria"` // append-ordered by creation (linearization order)
}

// Sprint is the folded snapshot of a sprint entity: a time-boxed grouping of
// tasks, optionally within a project. Timestamps are unix seconds; zero means
// unset for StartDate, EndDate, StartedAt, and ClosedAt, and an empty Project
// means none. StartDate and EndDate are user-set LWW scalars, distinct from the
// CreatedAt and ClosedAt lifecycle stamps. Labels, Commits, and Comments are
// folded collections; Head is the chain tip the snapshot was folded from.
type Sprint struct {
	ID          EntityID     `json:"id"`
	Project     EntityID     `json:"project"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Status      SprintStatus `json:"status"`
	StartDate   int64        `json:"start_date"`
	EndDate     int64        `json:"end_date"`
	Labels      []string     `json:"labels"`
	Commits     []SHA        `json:"commits"`
	Comments    []Comment    `json:"comments"`
	Author      Actor        `json:"author"`
	CreatedAt   int64        `json:"created_at"`
	UpdatedAt   int64        `json:"updated_at"`
	StartedAt   int64        `json:"started_at"`
	ClosedAt    int64        `json:"closed_at"`
	Head        SHA          `json:"head"`
}

// Project is the folded snapshot of a project entity: a long-lived grouping of
// sprints and tasks. Timestamps are unix seconds; zero means unset for ClosedAt.
// Projects carry no start or end dates and no StartedAt — only the CreatedAt and
// ClosedAt lifecycle stamps. Labels, Commits, and Comments are folded
// collections; Head is the chain tip the snapshot was folded from.
type Project struct {
	ID          EntityID      `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      ProjectStatus `json:"status"`
	Labels      []string      `json:"labels"`
	Commits     []SHA         `json:"commits"`
	Comments    []Comment     `json:"comments"`
	Author      Actor         `json:"author"`
	CreatedAt   int64         `json:"created_at"`
	UpdatedAt   int64         `json:"updated_at"`
	ClosedAt    int64         `json:"closed_at"`
	Head        SHA           `json:"head"`
}

// NewNonce returns 16 crypto/rand bytes hex-encoded (32 characters). Create
// ops embed a nonce so otherwise-identical creates hash to distinct entity
// ids.
func NewNonce() string {
	b := make([]byte, 16)
	rand.Read(b) // never fails: crypto/rand.Read panics internally instead of returning an error
	return hex.EncodeToString(b)
}
