package notes

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

// TaskSpec is the input to CreateTask. Title is required. Branch names the
// task's branch; an empty Branch resolves the repository's current branch —
// degrading to the backlog on a detached HEAD — unless Backlog is set, which
// puts the task on the backlog unconditionally. Type defaults to
// model.TypeTask. Criteria are added verbatim — none is auto-injected, so an
// empty slice creates a task with no acceptance criteria. Parent, Sprint,
// Project, and BlockedBy carry full entity ids; the caller resolves any prefix
// first.
type TaskSpec struct {
	Title       string
	Description string
	Type        model.TaskType
	Priority    model.Priority
	Branch      model.Branch
	Backlog     bool
	Parent      model.EntityID
	Sprint      model.EntityID
	Project     model.EntityID
	Labels      []string
	Criteria    []string
	BlockedBy   []model.EntityID
}

// TaskCreated is the result of CreateTask: the folded task plus two signals the
// caller renders. Reused reports that Create's best-effort duplicate guard
// converged on an existing task rather than rooting a new one. Degraded reports
// that an empty-branch, non-backlog spec ran on a detached HEAD and landed on
// the backlog instead of a resolved branch. The two are mutually exclusive: a
// converged create rooted nothing, so a Reused result never degrades.
type TaskCreated struct {
	Task     model.Task
	Reused   bool
	Degraded bool
}

// StartResult is the result of StartTask: the folded task plus the branch it
// was moved onto. BranchSet reports whether a branch was set; it is false when
// an empty-branch start ran on a detached HEAD, which claims the task without
// setting a branch.
type StartResult struct {
	Task      model.Task
	Branch    model.Branch
	BranchSet bool
}

// BranchScope selects the branch a task listing scopes to.
type BranchScope int

const (
	// ScopeAllBranches matches tasks on every branch.
	ScopeAllBranches BranchScope = iota
	// ScopeCurrentBranch matches tasks on the repository's current branch,
	// degrading to the backlog on a detached HEAD.
	ScopeCurrentBranch
	// ScopeBacklog matches only backlog tasks (empty branch).
	ScopeBacklog
	// ScopeNamed matches tasks on the named branch.
	ScopeNamed
)

// TaskFilter narrows a task listing. The zero value matches every task. Scope
// selects the branch dimension (Branch supplies the name for ScopeNamed).
// Statuses, Labels, Type, and Assignee constrain the respective fields — a
// zero-length or zero value imposes no constraint, except AssigneeSet, which
// distinguishes "no assignee filter" from "match the empty assignee".
// ArchiveCutoff, when non-zero, drops done or cancelled tasks closed before it.
type TaskFilter struct {
	Scope         BranchScope
	Branch        model.Branch
	Statuses      []model.Status
	Labels        []string
	Type          model.TaskType
	Assignee      string
	AssigneeSet   bool
	ArchiveCutoff time.Time
}

// TaskEdit is the field mask for EditTask: every field is pointer-optional, the
// sanctioned tri-state. A nil pointer leaves the field untouched; a non-nil
// pointer sets it, and a pointer to the zero value clears it. AddLabels and
// RemoveLabels are applied in order. An all-nil, all-empty mask is ErrEmptyEdit.
type TaskEdit struct {
	Title        *string
	Description  *string
	Type         *model.TaskType
	Priority     *model.Priority
	Status       *model.Status
	Assignee     *model.Actor
	AddLabels    []string
	RemoveLabels []string
	Parent       *model.EntityID
	Sprint       *model.EntityID
	Project      *model.EntityID
	Branch       *model.Branch
}

// CreateTask roots a new task chain and returns its folded snapshot with the
// reuse and degrade signals. Create is idempotent over content on a best-effort
// basis: an exact duplicate of a live task returns that existing task
// (Reused=true) instead of rooting a new one, though truly concurrent identical
// creates can both land. Ops follow the CLI's exact order — create, then
// SetSprint/SetProject, one AddCriterion per Criteria text, one AddDep per
// BlockedBy id — so the entity id and dedupe key stay stable.
func (c *Client) CreateTask(ctx context.Context, spec TaskSpec) (TaskCreated, error) {
	branch := spec.Branch
	var degraded bool
	switch {
	case spec.Backlog:
		branch = ""
	case spec.Branch == "":
		resolved, toBacklog, err := c.currentBranchOrBacklog(ctx)
		if err != nil {
			return TaskCreated{}, err
		}
		branch, degraded = resolved, toBacklog
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
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		return TaskCreated{Task: dup.Existing.(model.Task), Reused: true}, nil
	}
	if err != nil {
		return TaskCreated{}, err
	}
	return TaskCreated{Task: snapshot.(model.Task), Degraded: degraded}, nil
}

// Tasks folds every task, filters it through f, and returns the survivors in
// task list order: priority ascending, created ascending, id ascending.
func (c *Client) Tasks(ctx context.Context, f TaskFilter) ([]model.Task, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	inScope, err := c.branchScope(ctx, f.Scope, f.Branch)
	if err != nil {
		return nil, err
	}
	tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
		switch {
		case !inScope(t):
			return true
		case len(f.Statuses) > 0 && !slices.Contains(f.Statuses, t.Status):
			return true
		case !hasAll(t.Labels, f.Labels):
			return true
		case f.AssigneeSet && string(t.Assignee) != f.Assignee:
			return true
		case f.Type != "" && t.Type != f.Type:
			return true
		case !f.ArchiveCutoff.IsZero() && isArchived(t, f.ArchiveCutoff):
			return true
		default:
			return false
		}
	})
	sortTasks(tasks)
	return tasks, nil
}

// ReadyTasks folds every task and returns the unblocked, unassigned, open tasks
// in the given branch scope, in task list order. A task is unblocked when every
// blocker resolves to a live task that is done or cancelled.
func (c *Client) ReadyTasks(ctx context.Context, scope BranchScope, branch model.Branch) ([]model.Task, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	inScope, err := c.branchScope(ctx, scope, branch)
	if err != nil {
		return nil, err
	}
	live := taskMap(tasks)
	tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
		return !inScope(t) || t.Status != model.StatusOpen || t.Assignee != "" || !unblocked(live, t)
	})
	sortTasks(tasks)
	return tasks, nil
}

// StaleTasks folds every task and returns the in-progress tasks idle past ttl,
// in task list order.
func (c *Client) StaleTasks(ctx context.Context, ttl time.Duration) ([]model.Task, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
		return !isStale(t, now, ttl)
	})
	sortTasks(tasks)
	return tasks, nil
}

// ArchivedTasks folds every task and returns the done or cancelled tasks closed
// strictly before cutoff, in task list order.
func (c *Client) ArchivedTasks(ctx context.Context, cutoff time.Time) ([]model.Task, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
		return !isArchived(t, cutoff)
	})
	sortTasks(tasks)
	return tasks, nil
}

// ClaimTask claims the task for the client's actor. It refuses with a
// *ConflictError a task that is assigned or not open, and again if the claim
// loses the fold race to another actor.
func (c *Client) ClaimTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	me, err := c.s.Actor(ctx)
	if err != nil {
		return model.Task{}, err
	}
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if err := claimable(task); err != nil {
		return model.Task{}, err
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.Claim{Assignee: me}})
	if err != nil {
		return model.Task{}, err
	}
	task = snapshot.(model.Task)
	if task.Assignee != me {
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("already claimed by %s (%s)", task.Assignee, task.Status)}
	}
	return task, nil
}

// StealTask reclaims an in-progress task whose lease is stale past the
// caller-supplied ttl, for the client's actor. A fresh lease is refused with a
// *ConflictError naming the time remaining; a holder who renewed past the
// observed heartbeat (or a stealer who lost the race) makes the Reclaim a fold
// no-op, surfaced as a *ConflictError after the reload.
func (c *Client) StealTask(ctx context.Context, id model.EntityID, ttl time.Duration) (model.Task, error) {
	me, err := c.s.Actor(ctx)
	if err != nil {
		return model.Task{}, err
	}
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	now := time.Now()
	if !isStale(task, now, ttl) {
		remaining := ttl - now.Sub(time.Unix(task.HeartbeatAt, 0))
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("lease held by %s, %s remaining", task.Assignee, remaining.Round(time.Second))}
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.Reclaim{Assignee: me, From: task.Assignee, AfterLamport: task.HeartbeatLamport}})
	if err != nil {
		return model.Task{}, err
	}
	task = snapshot.(model.Task)
	if task.Assignee != me {
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("held by %s (renewed in time or lost steal race)", task.Assignee)}
	}
	return task, nil
}

// ClaimTaskSync claims the task, syncs the derived remote, then reloads and
// yields with a *ConflictError if another agent's claim linearized first over
// the sync.
func (c *Client) ClaimTaskSync(ctx context.Context, id model.EntityID) (model.Task, error) {
	claimed, err := c.ClaimTask(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	me := claimed.Assignee
	remote, err := c.deriveRemote(ctx)
	if err != nil {
		return model.Task{}, err
	}
	if _, err := ccsync.Sync(ctx, c.dir, remote, false); err != nil {
		return model.Task{}, err
	}
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if task.Assignee != me {
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("claimed by %s", task.Assignee)}
	}
	return task, nil
}

// StartTask claims the task for the client's actor and moves it onto branch. An
// empty branch resolves the repository's current branch, degrading on a
// detached HEAD to claim the task without a branch (BranchSet=false). It refuses
// with a *ConflictError a task that is assigned or not open.
func (c *Client) StartTask(ctx context.Context, id model.EntityID, branch model.Branch) (StartResult, error) {
	task, err := c.Task(ctx, id)
	if err != nil {
		return StartResult{}, err
	}
	if err := claimable(task); err != nil {
		return StartResult{}, err
	}
	me, err := c.s.Actor(ctx)
	if err != nil {
		return StartResult{}, err
	}
	// Resolve the branch before any mutation: a hard resolution failure must
	// leave the task untouched, so it cannot claim first and error second.
	target, degraded := branch, false
	if branch == "" {
		resolved, toBacklog, err := c.currentBranchOrBacklog(ctx)
		if err != nil {
			return StartResult{}, err
		}
		target, degraded = resolved, toBacklog
	}
	ref := refs.For(model.KindTask, id)
	snapshot, err := c.s.Append(ctx, ref, []model.Op{model.Claim{Assignee: me}})
	if err != nil {
		return StartResult{}, err
	}
	task = snapshot.(model.Task)
	if task.Assignee != me {
		return StartResult{}, &ConflictError{ID: id, Msg: fmt.Sprintf("already claimed by %s (%s)", task.Assignee, task.Status)}
	}
	if degraded {
		return StartResult{Task: task}, nil
	}
	snapshot, err = c.s.Append(ctx, ref, []model.Op{model.SetBranch{Branch: target}})
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{Task: snapshot.(model.Task), Branch: target, BranchSet: true}, nil
}

// RenewTask refreshes the lease heartbeat on a task the client's actor holds.
// It refuses with a *ConflictError if the caller is not the assignee.
func (c *Client) RenewTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	me, err := c.s.Actor(ctx)
	if err != nil {
		return model.Task{}, err
	}
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if task.Assignee != me {
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("held by %s, not you", orDash(string(task.Assignee)))}
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.Renew{}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// DoneTask marks the task done and links the repository's HEAD commit when one
// exists. It refuses with a *ConflictError a task already closed, and — unless
// force is set — with an *UnmetCriteriaError a task whose acceptance criteria
// have not all been met.
func (c *Client) DoneTask(ctx context.Context, id model.EntityID, force bool) (model.Task, error) {
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if err := openOrInProgress(id, task.Status); err != nil {
		return model.Task{}, err
	}
	if !force {
		var unmet []model.Criterion
		for _, crit := range task.Criteria {
			if crit.Status != model.CriterionMet {
				unmet = append(unmet, crit)
			}
		}
		if len(unmet) > 0 {
			return model.Task{}, &UnmetCriteriaError{ID: id, Unmet: unmet}
		}
	}
	ops := []model.Op{model.SetStatus{Status: model.StatusDone}}
	head, err := c.head(ctx)
	if err != nil {
		return model.Task{}, err
	}
	if head != "" {
		ops = append(ops, model.LinkCommit{SHA: head})
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), ops)
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// CancelTask marks the task cancelled. It refuses with a *ConflictError a task
// already closed.
func (c *Client) CancelTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if err := openOrInProgress(id, task.Status); err != nil {
		return model.Task{}, err
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.SetStatus{Status: model.StatusCancelled}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// EditTask applies the field mask to the task in one append and returns the
// folded snapshot. An empty mask fails with ErrEmptyEdit before any write.
func (c *Client) EditTask(ctx context.Context, id model.EntityID, edit TaskEdit) (model.Task, error) {
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Description != nil {
		ops = append(ops, model.SetDescription{Description: *edit.Description})
	}
	if edit.Type != nil {
		ops = append(ops, model.SetType{Type: *edit.Type})
	}
	if edit.Priority != nil {
		ops = append(ops, model.SetPriority{Priority: *edit.Priority})
	}
	if edit.Status != nil {
		ops = append(ops, model.SetStatus{Status: *edit.Status})
	}
	if edit.Assignee != nil {
		ops = append(ops, model.SetAssignee{Assignee: *edit.Assignee})
	}
	for _, label := range edit.AddLabels {
		ops = append(ops, model.AddLabel{Label: label})
	}
	for _, label := range edit.RemoveLabels {
		ops = append(ops, model.RemoveLabel{Label: label})
	}
	if edit.Parent != nil {
		ops = append(ops, model.SetParent{Parent: *edit.Parent})
	}
	if edit.Sprint != nil {
		ops = append(ops, model.SetSprint{Sprint: *edit.Sprint})
	}
	if edit.Project != nil {
		ops = append(ops, model.SetProject{Project: *edit.Project})
	}
	if edit.Branch != nil {
		ops = append(ops, model.SetBranch{Branch: *edit.Branch})
	}
	if len(ops) == 0 {
		return model.Task{}, ErrEmptyEdit
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), ops)
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// CommentTask appends a comment to the task and returns the folded snapshot.
func (c *Client) CommentTask(ctx context.Context, id model.EntityID, body string) (model.Task, error) {
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.AddComment{Body: body}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// AddDep marks id blocked by blocker. It refuses with ErrCycle an edge that
// would close a cycle: blocker must not already depend on id through the
// blocked-by closure.
func (c *Client) AddDep(ctx context.Context, id, blocker model.EntityID) (model.Task, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return model.Task{}, err
	}
	if hasPath(taskMap(tasks), blocker, id) {
		return model.Task{}, fmt.Errorf("%w: %s already blocks %s", ErrCycle, id.Short(), blocker.Short())
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.AddDep{ID: blocker}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// RemoveDep removes the blocked-by edge from id to blocker and returns the
// folded snapshot.
func (c *Client) RemoveDep(ctx context.Context, id, blocker model.EntityID) (model.Task, error) {
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.RemoveDep{ID: blocker}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// AddCriterion adds an acceptance criterion to the task. An empty script leaves
// the criterion without a validation check.
func (c *Client) AddCriterion(ctx context.Context, id model.EntityID, text, script string) (model.Task, error) {
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.AddCriterion{ID: model.NewNonce(), Text: text, Script: script}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// RemoveCriterion removes the criterion whose id crit uniquely prefixes from the
// task. An unknown or ambiguous prefix fails with ErrNotFound or ErrAmbiguous.
func (c *Client) RemoveCriterion(ctx context.Context, id model.EntityID, crit string) (model.Task, error) {
	resolved, err := c.resolveCriterion(ctx, id, crit)
	if err != nil {
		return model.Task{}, err
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.RemoveCriterion{ID: resolved.ID}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// SetCriterionStatus sets the status of the criterion whose id crit uniquely
// prefixes on the task. An unknown or ambiguous prefix fails with ErrNotFound or
// ErrAmbiguous.
func (c *Client) SetCriterionStatus(ctx context.Context, id model.EntityID, crit string, status model.CriterionStatus) (model.Task, error) {
	resolved, err := c.resolveCriterion(ctx, id, crit)
	if err != nil {
		return model.Task{}, err
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.SetCriterionStatus{ID: resolved.ID, Status: status}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// SetCriterionScript sets or clears (empty script) the validation check of the
// criterion whose id crit uniquely prefixes on the task. An unknown or ambiguous
// prefix fails with ErrNotFound or ErrAmbiguous.
func (c *Client) SetCriterionScript(ctx context.Context, id model.EntityID, crit, script string) (model.Task, error) {
	resolved, err := c.resolveCriterion(ctx, id, crit)
	if err != nil {
		return model.Task{}, err
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.SetCriterionScript{ID: resolved.ID, Script: script}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// ValidateTask runs each criterion's validation script under sh in the
// repository root, bounded per script by timeout, appends one
// SetCriterionStatus op per verdict in a single write, and returns the folded
// task. onVerdict, when non-nil, is called with each criterion and its verdict
// as it is decided, before the write — so the caller can stream the results; an
// error it returns aborts the run before any verdict is persisted. ValidateTask
// never prompts, echoes, or gates: consent and display stay with the caller.
func (c *Client) ValidateTask(ctx context.Context, id model.EntityID, criteria []model.Criterion, timeout time.Duration, onVerdict func(model.Criterion, model.CriterionStatus) error) (model.Task, error) {
	ops := make([]model.Op, len(criteria))
	for i, crit := range criteria {
		status := runScript(ctx, c.s.Git.Dir, crit.Script, timeout)
		if onVerdict != nil {
			if err := onVerdict(crit, status); err != nil {
				return model.Task{}, err
			}
		}
		ops[i] = model.SetCriterionStatus{ID: crit.ID, Status: status}
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), ops)
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// ResolveCriterion expands a criterion id prefix — matched case-insensitively —
// against a task's criteria. No match fails with ErrNotFound; several matches
// fail with ErrAmbiguous listing each candidate's short id and text.
func ResolveCriterion(t model.Task, prefix string) (model.Criterion, error) {
	lowered := strings.ToLower(prefix)
	var matches []model.Criterion
	for _, crit := range t.Criteria {
		if strings.HasPrefix(strings.ToLower(crit.ID), lowered) {
			matches = append(matches, crit)
		}
	}
	switch len(matches) {
	case 0:
		return model.Criterion{}, fmt.Errorf("%w: no criterion matches %q", ErrNotFound, prefix)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for i, crit := range matches {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s %s", crit.ID[:7], crit.Text)
		}
		return model.Criterion{}, fmt.Errorf("%w: criterion prefix %q matches %d: %s", ErrAmbiguous, prefix, len(matches), b.String())
	}
}

// resolveCriterion loads the task and resolves a criterion id prefix against it.
func (c *Client) resolveCriterion(ctx context.Context, id model.EntityID, prefix string) (model.Criterion, error) {
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Criterion{}, err
	}
	return ResolveCriterion(task, prefix)
}

// branchScope resolves a scope into a predicate over a task's folded branch.
// ScopeCurrentBranch degrades to the backlog when HEAD has no resolvable branch.
func (c *Client) branchScope(ctx context.Context, scope BranchScope, branch model.Branch) (func(model.Task) bool, error) {
	switch scope {
	case ScopeAllBranches:
		return func(model.Task) bool { return true }, nil
	case ScopeBacklog:
		return func(t model.Task) bool { return t.Branch == "" }, nil
	case ScopeNamed:
		return func(t model.Task) bool { return t.Branch == branch }, nil
	case ScopeCurrentBranch:
		resolved, toBacklog, err := c.currentBranchOrBacklog(ctx)
		if err != nil {
			return nil, err
		}
		if toBacklog {
			return func(t model.Task) bool { return t.Branch == "" }, nil
		}
		return func(t model.Task) bool { return t.Branch == resolved }, nil
	default:
		panic(fmt.Sprintf("notes: unknown branch scope %d", scope))
	}
}

// openOrInProgress reports a *ConflictError unless status is open or
// in_progress — the only states a task close transition accepts.
func openOrInProgress(id model.EntityID, status model.Status) error {
	switch status {
	case model.StatusOpen, model.StatusInProgress:
		return nil
	}
	return &ConflictError{ID: id, Msg: "already " + string(status)}
}

// claimable reports a *ConflictError when a task cannot be claimed: it is
// already assigned or not open.
func claimable(t model.Task) error {
	switch {
	case t.Assignee != "":
		return &ConflictError{ID: t.ID, Msg: fmt.Sprintf("already claimed by %s (%s)", t.Assignee, t.Status)}
	case t.Status != model.StatusOpen:
		return &ConflictError{ID: t.ID, Msg: fmt.Sprintf("not open (%s)", t.Status)}
	default:
		return nil
	}
}

// unblocked reports whether every blocker of t resolves to a live task that is
// done or cancelled. A blocker id matching no live task does not count as
// resolved.
func unblocked(live map[model.EntityID]model.Task, t model.Task) bool {
	for _, dep := range t.BlockedBy {
		blocker, ok := live[dep]
		if !ok {
			return false
		}
		if blocker.Status != model.StatusDone && blocker.Status != model.StatusCancelled {
			return false
		}
	}
	return true
}

// hasPath reports whether target is reachable from start (inclusive) through the
// blocked_by closure over live tasks.
func hasPath(live map[model.EntityID]model.Task, start, target model.EntityID) bool {
	seen := map[model.EntityID]bool{}
	stack := []model.EntityID{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, live[id].BlockedBy...)
	}
	return false
}

// taskMap keys tasks by entity id for blocker lookups.
func taskMap(tasks []model.Task) map[model.EntityID]model.Task {
	m := make(map[model.EntityID]model.Task, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return m
}

// sortTasks orders tasks by priority ascending, then created_at ascending, then
// id ascending.
func sortTasks(tasks []model.Task) {
	slices.SortFunc(tasks, func(a, b model.Task) int {
		if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
			return c
		}
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// isStale reports an in-progress task whose idle time exceeds ttl.
func isStale(t model.Task, now time.Time, ttl time.Duration) bool {
	return t.Status == model.StatusInProgress &&
		now.Sub(time.Unix(t.HeartbeatAt, 0)) > ttl
}

// isArchived reports a done or cancelled task closed strictly before cutoff.
func isArchived(t model.Task, cutoff time.Time) bool {
	return (t.Status == model.StatusDone || t.Status == model.StatusCancelled) &&
		t.ClosedAt != 0 && time.Unix(t.ClosedAt, 0).Before(cutoff)
}

// hasAll reports whether have contains every element of want.
func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// orDash renders an empty string as a single dash, matching the CLI's rendering
// of an absent assignee in a conflict message.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runScript executes one criterion's check command under sh in dir, bounded by
// timeout and ctx cancellation. Exit 0 is met; a non-zero exit or a timeout is
// failed.
func runScript(ctx context.Context, dir, script string, timeout time.Duration) model.CriterionStatus {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	//nolint:gosec // G204: running the operator-defined criterion check command under sh is this feature's whole purpose.
	cmd := exec.CommandContext(tctx, "sh", "-c", script)
	cmd.Dir = dir
	// WaitDelay force-closes the script's inherited pipes shortly after the
	// timeout fires, so a child that outlives sh (holding stdout open) cannot
	// hang CombinedOutput past the bound.
	cmd.WaitDelay = 5 * time.Second
	if _, err := cmd.CombinedOutput(); err != nil {
		return model.CriterionFailed
	}
	return model.CriterionMet
}
