package viz

import "github.com/yasyf/cc-notes/model"

// Graph is the whole visualization payload for one repository: the repo
// header, the branch swimlanes, the classified entity lifecycle events, and a
// lean per-entity summary for the legend. Its JSON tags are the phase-3 wire
// format — pinned field order, lowercase snake_case names — so the server
// serializes this type directly.
type Graph struct {
	Repo     RepoInfo        `json:"repo"`
	Lanes    []Lane          `json:"lanes"`
	Events   []Event         `json:"events"`
	Entities []EntitySummary `json:"entities"`
}

// RepoInfo is the graph header: the worktree root, the resolved trunk branch,
// the branch HEAD points at (empty when detached), the RFC3339 generation
// instant, and whether the commit walk hit its cap.
type RepoInfo struct {
	Root        string `json:"root"`
	Trunk       string `json:"trunk"`
	Head        string `json:"head"`
	GeneratedAt string `json:"generated_at"`
	Truncated   bool   `json:"truncated"`
}

// Point is a commit on a lane: its sha and commit time in unix seconds.
type Point struct {
	SHA  model.SHA `json:"sha"`
	Time int64     `json:"time"`
}

// MergePoint is where a lane rejoins another: the merge commit's sha and time,
// the branch it merged Into, and the Kind of merge — "merge" (a merge commit),
// "fast-forward" (linear absorption), or "inferred" (a squash detected from a
// cc-task trailer on a trunk commit).
type MergePoint struct {
	SHA  model.SHA `json:"sha"`
	Time int64     `json:"time"`
	Into string    `json:"into"`
	Kind string    `json:"kind"`
}

// Lane is one branch's lifeline. Name is the short branch name; Parent is the
// lane it forked from (the trunk when parentage is flat or unknown). Fork is
// the divergence point and Merge the rejoin point, each nil when absent. Status
// is "active", "merged", or "deleted", where "deleted" means no live ref backs
// the branch. Inferred is true only for a deleted lane reconstructed from task
// history alone, with no DAG proof — a fork- and tip-less rumor; a ref-backed
// lane or a deleted lane mined from the git DAG (real fork, tip, and merge) is
// false. Tip is the branch tip. Start is the fork time and End the merge time,
// with End 0 meaning still open. Commits counts the walked commits attributed
// to the lane.
type Lane struct {
	Name     string      `json:"name"`
	Parent   string      `json:"parent"`
	Fork     *Point      `json:"fork"`
	Merge    *MergePoint `json:"merge"`
	Status   string      `json:"status"`
	Inferred bool        `json:"inferred"`
	Tip      *Point      `json:"tip"`
	Start    int64       `json:"start"`
	End      int64       `json:"end"`
	Commits  int         `json:"commits"`
}

// EntityRef identifies the entity an event belongs to: its kind, full id, the
// 7-character short id, and its title.
type EntityRef struct {
	Kind  string         `json:"kind"`
	ID    model.EntityID `json:"id"`
	Short string         `json:"short"`
	Title string         `json:"title"`
}

// Event is one point on an entity's lifecycle: which entity, the event Type,
// the unix-seconds Time, the Branch it happened on, the carrying commit SHA,
// and a kind-specific Detail bag.
type Event struct {
	Entity EntityRef         `json:"entity"`
	Type   string            `json:"type"`
	Time   int64             `json:"time"`
	Branch string            `json:"branch"`
	SHA    model.SHA         `json:"sha"`
	Detail map[string]string `json:"detail"`
}

// EntitySummary is one entity's lean legend row: the identity fields every kind
// carries plus the per-kind fields the legend surfaces. Tasks fill Status,
// Branch, Assignee, StartedAt, ClosedAt, Sprint, and Project; notes and docs
// fill VerifiedAt, Stale, and Superseded; sprints and projects fill Status,
// StartDate, EndDate, and Project. Absent fields marshal away via omitempty.
type EntitySummary struct {
	Kind       string         `json:"kind"`
	ID         model.EntityID `json:"id"`
	Short      string         `json:"short"`
	Title      string         `json:"title"`
	Status     string         `json:"status,omitempty"`
	Branch     string         `json:"branch,omitempty"`
	Assignee   string         `json:"assignee,omitempty"`
	StartedAt  int64          `json:"started_at,omitempty"`
	ClosedAt   int64          `json:"closed_at,omitempty"`
	Sprint     string         `json:"sprint,omitempty"`
	Project    string         `json:"project,omitempty"`
	VerifiedAt int64          `json:"verified_at,omitempty"`
	Stale      bool           `json:"stale,omitempty"`
	Superseded bool           `json:"superseded,omitempty"`
	StartDate  int64          `json:"start_date,omitempty"`
	EndDate    int64          `json:"end_date,omitempty"`
}
