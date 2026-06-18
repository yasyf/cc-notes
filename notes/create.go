package notes

import (
	"context"

	"github.com/yasyf/cc-notes/model"
)

// ProjectSpec is the input to CreateProject. Title is required; the rest are
// optional and may be left zero.
type ProjectSpec struct {
	Title       string
	Description string
	Labels      []string
}

// SprintSpec is the input to CreateSprint. Title is required. Project, when
// non-empty, makes the sprint a member of that project. StartDate and EndDate
// are unix seconds; zero leaves them unset.
type SprintSpec struct {
	Title       string
	Description string
	Project     model.EntityID
	Labels      []string
	StartDate   int64
	EndDate     int64
}

// TaskSpec is the input to CreateTask. Title is required. Branch names the
// task's branch; an empty Branch puts it on the backlog. BranchFromHead, when
// true, resolves the repository's current branch instead and overrides Branch
// — it errors on a detached HEAD. Type defaults to model.TypeTask. Criteria
// are added verbatim — none is auto-injected, so an empty slice creates a task
// with no acceptance criteria.
type TaskSpec struct {
	Title          string
	Description    string
	Type           model.TaskType
	Priority       model.Priority
	Branch         model.Branch
	BranchFromHead bool
	Parent         model.EntityID
	Sprint         model.EntityID
	Project        model.EntityID
	Labels         []string
	Criteria       []string
	BlockedBy      []model.EntityID
}

// CreateProject roots a new project chain and returns its folded snapshot.
func (c *Client) CreateProject(ctx context.Context, spec ProjectSpec) (model.Project, error) {
	snapshot, err := c.s.Create(ctx, []model.Op{model.CreateProject{
		Nonce:       model.NewNonce(),
		Title:       spec.Title,
		Description: spec.Description,
		Labels:      spec.Labels,
	}})
	if err != nil {
		return model.Project{}, err
	}
	return snapshot.(model.Project), nil
}

// CreateSprint roots a new sprint chain and returns its folded snapshot.
func (c *Client) CreateSprint(ctx context.Context, spec SprintSpec) (model.Sprint, error) {
	ops := []model.Op{model.CreateSprint{
		Nonce:       model.NewNonce(),
		Title:       spec.Title,
		Description: spec.Description,
		Project:     spec.Project,
		Labels:      spec.Labels,
	}}
	if spec.StartDate != 0 {
		ops = append(ops, model.SetStartDate{Date: spec.StartDate})
	}
	if spec.EndDate != 0 {
		ops = append(ops, model.SetEndDate{Date: spec.EndDate})
	}
	snapshot, err := c.s.Create(ctx, ops)
	if err != nil {
		return model.Sprint{}, err
	}
	return snapshot.(model.Sprint), nil
}

// CreateTask roots a new task chain and returns its folded snapshot. SetSprint
// and SetProject ops follow the create when the spec names them, one
// AddCriterion per Criteria text, and one AddDep per BlockedBy id.
func (c *Client) CreateTask(ctx context.Context, spec TaskSpec) (model.Task, error) {
	branch := spec.Branch
	if spec.BranchFromHead {
		head, err := c.s.Git.HeadBranch(ctx)
		if err != nil {
			return model.Task{}, err
		}
		branch = head
	}
	taskType := spec.Type
	if taskType == "" {
		taskType = model.TypeTask
	}
	ops := []model.Op{model.CreateTask{
		Nonce:       model.NewNonce(),
		Title:       spec.Title,
		Description: spec.Description,
		Type:        taskType,
		Priority:    spec.Priority,
		Branch:      branch,
		Parent:      spec.Parent,
		Labels:      spec.Labels,
	}}
	if spec.Sprint != "" {
		ops = append(ops, model.SetSprint{Sprint: spec.Sprint})
	}
	if spec.Project != "" {
		ops = append(ops, model.SetProject{Project: spec.Project})
	}
	for _, text := range spec.Criteria {
		ops = append(ops, model.AddCriterion{ID: model.NewNonce(), Text: text})
	}
	for _, dep := range spec.BlockedBy {
		ops = append(ops, model.AddDep{ID: dep})
	}
	snapshot, err := c.s.Create(ctx, ops)
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}
