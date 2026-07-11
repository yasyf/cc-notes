package model

import "fmt"

// Op is a single immutable operation in an entity's history. Each op
// serializes as a JSON object whose first field is the "kind" discriminator;
// the set of kinds is closed and enforced by the pack codec.
type Op interface {
	// OpKind returns the wire discriminator for this operation.
	OpKind() string
}

// CreateOp is the root operation that starts an entity chain. CreateKind
// reports the kind the chain folds to.
type CreateOp interface {
	Op
	CreateKind() Kind
}

// CreateNote is the root operation of a note chain. The nonce makes
// otherwise-identical creates hash to distinct entity ids.
type CreateNote struct {
	Nonce   string   `json:"nonce"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
	Anchors []Anchor `json:"anchors"`
}

// OpKind returns "create_note".
func (CreateNote) OpKind() string { return "create_note" }

// CreateKind returns KindNote.
func (CreateNote) CreateKind() Kind { return KindNote }

func (o CreateNote) validate() error {
	for _, a := range o.Anchors {
		if err := a.Kind.validate(); err != nil {
			return err
		}
	}
	return nil
}

// SetTitle replaces the title of a note or task.
type SetTitle struct {
	Title string `json:"title"`
}

// OpKind returns "set_title".
func (SetTitle) OpKind() string { return "set_title" }

// SetBody replaces the body of a note.
type SetBody struct {
	Body string `json:"body"`
}

// OpKind returns "set_body".
func (SetBody) OpKind() string { return "set_body" }

// SetWhen replaces the free-text "read this when…" trigger of a doc. When is an
// LWW scalar: the last SetWhen (or the CreateDoc default) in linearization
// order wins.
type SetWhen struct {
	When string `json:"when"`
}

// OpKind returns "set_when".
func (SetWhen) OpKind() string { return "set_when" }

// AddTag adds one tag to a note's tag set.
type AddTag struct {
	Tag string `json:"tag"`
}

// OpKind returns "add_tag".
func (AddTag) OpKind() string { return "add_tag" }

// RemoveTag removes one tag from a note's tag set.
type RemoveTag struct {
	Tag string `json:"tag"`
}

// OpKind returns "remove_tag".
func (RemoveTag) OpKind() string { return "remove_tag" }

// AddAnchor adds one anchor to a note's anchor set.
type AddAnchor struct {
	Anchor Anchor `json:"anchor"`
}

// OpKind returns "add_anchor".
func (AddAnchor) OpKind() string { return "add_anchor" }

func (o AddAnchor) validate() error { return o.Anchor.Kind.validate() }

// RemoveAnchor removes one anchor from a note's anchor set.
type RemoveAnchor struct {
	Anchor Anchor `json:"anchor"`
}

// OpKind returns "remove_anchor".
func (RemoveAnchor) OpKind() string { return "remove_anchor" }

func (o RemoveAnchor) validate() error { return o.Anchor.Kind.validate() }

// DeleteNote tombstones a note. Deletion is an op, never a ref deletion, so
// it propagates through sync.
type DeleteNote struct{}

// OpKind returns "delete_note".
func (DeleteNote) OpKind() string { return "delete_note" }

// VerifyNote records that a note was reconfirmed true: the per-anchor content
// witness and the HEAD commit it was checked against. Who and when come from
// the carrying commit's identity, so this op stays a separate appended commit
// and never folds into create_note (which would change the entity id).
type VerifyNote struct {
	Witness        []AnchorWitness `json:"witness"`
	VerifiedCommit SHA             `json:"verified_commit"`
}

// OpKind returns "verify_note".
func (VerifyNote) OpKind() string { return "verify_note" }

func (o VerifyNote) validate() error {
	for _, w := range o.Witness {
		if err := w.Anchor.Kind.validate(); err != nil {
			return err
		}
	}
	return nil
}

// AddSupersededBy records that the note is replaced by the note with the given
// id; a note with any supersede edge is a soft tombstone.
type AddSupersededBy struct {
	ID EntityID `json:"id"`
}

// OpKind returns "add_superseded_by".
func (AddSupersededBy) OpKind() string { return "add_superseded_by" }

// RemoveSupersededBy removes a supersede edge.
type RemoveSupersededBy struct {
	ID EntityID `json:"id"`
}

// OpKind returns "remove_superseded_by".
func (RemoveSupersededBy) OpKind() string { return "remove_superseded_by" }

// MarkStale flags a note as explicitly out-of-date. It carries only the
// optional Reason; who and when come from the carrying commit's identity at
// fold time, so the op stays a separate appended commit and never duplicates
// commit metadata.
type MarkStale struct {
	Reason string `json:"reason"`
}

// OpKind returns "mark_stale".
func (MarkStale) OpKind() string { return "mark_stale" }

// ClearStale drops a note's explicit out-of-date flag.
type ClearStale struct{}

// OpKind returns "clear_stale".
func (ClearStale) OpKind() string { return "clear_stale" }

// CreateTask is the root operation of a task chain. The nonce makes
// otherwise-identical creates hash to distinct entity ids; an empty Parent
// means no parent.
type CreateTask struct {
	Nonce       string   `json:"nonce"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Type        TaskType `json:"type"`
	Priority    Priority `json:"priority"`
	Branch      Branch   `json:"branch"`
	Parent      EntityID `json:"parent"`
	Labels      []string `json:"labels"`
}

// OpKind returns "create_task".
func (CreateTask) OpKind() string { return "create_task" }

// CreateKind returns KindTask.
func (CreateTask) CreateKind() Kind { return KindTask }

func (o CreateTask) validate() error {
	if err := o.Type.validate(); err != nil {
		return err
	}
	if err := o.Priority.validate(); err != nil {
		return err
	}
	if o.Branch == "" {
		return nil
	}
	return o.Branch.validate()
}

// CreateSprint is the root operation of a sprint chain. The nonce makes
// otherwise-identical creates hash to distinct entity ids; an empty Project
// means the sprint belongs to no project.
type CreateSprint struct {
	Nonce       string   `json:"nonce"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Project     EntityID `json:"project"`
	Labels      []string `json:"labels"`
}

// OpKind returns "create_sprint".
func (CreateSprint) OpKind() string { return "create_sprint" }

// CreateKind returns KindSprint.
func (CreateSprint) CreateKind() Kind { return KindSprint }

// CreateProject is the root operation of a project chain. The nonce makes
// otherwise-identical creates hash to distinct entity ids.
type CreateProject struct {
	Nonce       string   `json:"nonce"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

// OpKind returns "create_project".
func (CreateProject) OpKind() string { return "create_project" }

// CreateKind returns KindProject.
func (CreateProject) CreateKind() Kind { return KindProject }

// CreateDoc is the root operation of a doc chain. The nonce makes
// otherwise-identical creates hash to distinct entity ids.
type CreateDoc struct {
	Nonce   string   `json:"nonce"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	When    string   `json:"when"`
	Tags    []string `json:"tags"`
	Anchors []Anchor `json:"anchors"`
}

// OpKind returns "create_doc".
func (CreateDoc) OpKind() string { return "create_doc" }

// CreateKind returns KindDoc.
func (CreateDoc) CreateKind() Kind { return KindDoc }

func (o CreateDoc) validate() error {
	for _, a := range o.Anchors {
		if err := a.Kind.validate(); err != nil {
			return err
		}
	}
	return nil
}

// CreateLog is the root operation of a log chain. The nonce makes
// otherwise-identical creates hash to distinct entity ids.
type CreateLog struct {
	Nonce   string   `json:"nonce"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Anchors []Anchor `json:"anchors"`
}

// OpKind returns "create_log".
func (CreateLog) OpKind() string { return "create_log" }

// CreateKind returns KindLog.
func (CreateLog) CreateKind() Kind { return KindLog }

func (o CreateLog) validate() error {
	for _, a := range o.Anchors {
		if err := a.Kind.validate(); err != nil {
			return err
		}
	}
	return nil
}

// AppendEntry appends one entry to a log; entries are append-only. It carries
// only the entry text — author and timestamp come from the carrying commit's
// identity at fold time, exactly like add_comment.
type AppendEntry struct {
	Text string `json:"text"`
}

// OpKind returns "append_entry".
func (AppendEntry) OpKind() string { return "append_entry" }

// SetDescription replaces the description of a task.
type SetDescription struct {
	Description string `json:"description"`
}

// OpKind returns "set_description".
func (SetDescription) OpKind() string { return "set_description" }

// SetType replaces the type of a task.
type SetType struct {
	Type TaskType `json:"type"`
}

// OpKind returns "set_type".
func (SetType) OpKind() string { return "set_type" }

func (o SetType) validate() error { return o.Type.validate() }

// SetPriority replaces the priority of a task.
type SetPriority struct {
	Priority Priority `json:"priority"`
}

// OpKind returns "set_priority".
func (SetPriority) OpKind() string { return "set_priority" }

func (o SetPriority) validate() error { return o.Priority.validate() }

// SetStatus replaces the lifecycle status of a task.
type SetStatus struct {
	Status Status `json:"status"`
}

// OpKind returns "set_status".
func (SetStatus) OpKind() string { return "set_status" }

func (o SetStatus) validate() error { return o.Status.validate() }

// SetAssignee replaces the assignee of a task; an empty assignee unassigns.
type SetAssignee struct {
	Assignee Actor `json:"assignee"`
}

// OpKind returns "set_assignee".
func (SetAssignee) OpKind() string { return "set_assignee" }

// Claim assigns a task to an actor, conditionally: at fold time it applies
// only if the task is open and unassigned at that point, so concurrent claim
// races resolve first-wins on every replica.
type Claim struct {
	Assignee Actor `json:"assignee"`
}

// OpKind returns "claim".
func (Claim) OpKind() string { return "claim" }

// Renew refreshes the lease heartbeat. It carries no data: the fold reads the
// carrying commit's author and stamps the heartbeat when that author is the
// assignee.
type Renew struct{}

// OpKind returns "renew".
func (Renew) OpKind() string { return "renew" }

// Reclaim steals a task from a stale holder, deterministically. At fold time it
// applies only if the task is still held by From and the holder's heartbeat has
// not advanced past AfterLamport — so a holder who renewed past AfterLamport
// makes it a guaranteed no-op on every replica, and two stealers race
// first-wins (the second sees a From mismatch). Staleness itself is judged in
// the CLI; the fold never reads a clock. "Guaranteed no-op on every replica"
// means determinism — all replicas agree on the outcome under linearization,
// not that the holder always wins regardless of order: a concurrent Renew only
// saves the lease if it linearizes before the Reclaim.
type Reclaim struct {
	Assignee     Actor   `json:"assignee"`
	From         Actor   `json:"from"`
	AfterLamport Lamport `json:"after_lamport"`
}

// OpKind returns "reclaim".
func (Reclaim) OpKind() string { return "reclaim" }

// AddLabel adds one label to a task's label set.
type AddLabel struct {
	Label string `json:"label"`
}

// OpKind returns "add_label".
func (AddLabel) OpKind() string { return "add_label" }

// RemoveLabel removes one label from a task's label set.
type RemoveLabel struct {
	Label string `json:"label"`
}

// OpKind returns "remove_label".
func (RemoveLabel) OpKind() string { return "remove_label" }

// AddDep records that this task is blocked by the task with the given id.
type AddDep struct {
	ID EntityID `json:"id"`
}

// OpKind returns "add_dep".
func (AddDep) OpKind() string { return "add_dep" }

// RemoveDep removes a blocked-by dependency on the task with the given id.
type RemoveDep struct {
	ID EntityID `json:"id"`
}

// OpKind returns "remove_dep".
func (RemoveDep) OpKind() string { return "remove_dep" }

// LinkCommit records that the commit with the given sha implements this task.
type LinkCommit struct {
	SHA SHA `json:"sha"`
}

// OpKind returns "link_commit".
func (LinkCommit) OpKind() string { return "link_commit" }

// UnlinkCommit removes a commit link.
type UnlinkCommit struct {
	SHA SHA `json:"sha"`
}

// OpKind returns "unlink_commit".
func (UnlinkCommit) OpKind() string { return "unlink_commit" }

// SetParent replaces the parent of a task; an empty parent clears it.
type SetParent struct {
	Parent EntityID `json:"parent"`
}

// OpKind returns "set_parent".
func (SetParent) OpKind() string { return "set_parent" }

// AddComment appends one comment to a task; comments are append-only.
type AddComment struct {
	Body string `json:"body"`
}

// OpKind returns "add_comment".
func (AddComment) OpKind() string { return "add_comment" }

// SetBranch reassigns a task to a branch. Branch is an LWW scalar: the last
// SetBranch (or the CreateTask default) in linearization order wins. An
// empty branch means the backlog.
type SetBranch struct {
	Branch Branch `json:"branch"`
}

// OpKind returns "set_branch".
func (SetBranch) OpKind() string { return "set_branch" }

func (o SetBranch) validate() error {
	if o.Branch == "" {
		return nil
	}
	return o.Branch.validate()
}

// SetSprint assigns a task to a sprint; an empty sprint clears the membership.
// Sprint is an LWW scalar resolved at fold time.
type SetSprint struct {
	Sprint EntityID `json:"sprint"`
}

// OpKind returns "set_sprint".
func (SetSprint) OpKind() string { return "set_sprint" }

// SetProject assigns a task or sprint to a project; an empty project clears the
// membership. Project is an LWW scalar interpreted per entity kind at fold time.
type SetProject struct {
	Project EntityID `json:"project"`
}

// OpKind returns "set_project".
func (SetProject) OpKind() string { return "set_project" }

// SetSprintStatus replaces the lifecycle status of a sprint.
type SetSprintStatus struct {
	Status SprintStatus `json:"status"`
}

// OpKind returns "set_sprint_status".
func (SetSprintStatus) OpKind() string { return "set_sprint_status" }

func (o SetSprintStatus) validate() error { return o.Status.validate() }

// SetProjectStatus replaces the lifecycle status of a project.
type SetProjectStatus struct {
	Status ProjectStatus `json:"status"`
}

// OpKind returns "set_project_status".
func (SetProjectStatus) OpKind() string { return "set_project_status" }

func (o SetProjectStatus) validate() error { return o.Status.validate() }

// SetStartDate sets a sprint's user-set start date in unix seconds; zero clears
// it. Date is an LWW scalar, distinct from the CreatedAt lifecycle stamp.
type SetStartDate struct {
	Date int64 `json:"date"`
}

// OpKind returns "set_start_date".
func (SetStartDate) OpKind() string { return "set_start_date" }

// SetEndDate sets a sprint's user-set end date in unix seconds; zero clears it.
// Date is an LWW scalar, distinct from the ClosedAt lifecycle stamp.
type SetEndDate struct {
	Date int64 `json:"date"`
}

// OpKind returns "set_end_date".
func (SetEndDate) OpKind() string { return "set_end_date" }

// AddCriterion appends a structured acceptance criterion to a task. ID is a
// nonce stable within the task; Script is an optional check command ("" means
// none). New criteria start pending.
type AddCriterion struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Script string `json:"script"`
}

// OpKind returns "add_criterion".
func (AddCriterion) OpKind() string { return "add_criterion" }

// RemoveCriterion removes the criterion with the given id from a task.
type RemoveCriterion struct {
	ID string `json:"id"`
}

// OpKind returns "remove_criterion".
func (RemoveCriterion) OpKind() string { return "remove_criterion" }

// SetCriterionText replaces the text of the criterion with the given id.
type SetCriterionText struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// OpKind returns "set_criterion_text".
func (SetCriterionText) OpKind() string { return "set_criterion_text" }

// SetCriterionStatus replaces the validation status of the criterion with the
// given id.
type SetCriterionStatus struct {
	ID     string          `json:"id"`
	Status CriterionStatus `json:"status"`
}

// OpKind returns "set_criterion_status".
func (SetCriterionStatus) OpKind() string { return "set_criterion_status" }

func (o SetCriterionStatus) validate() error { return o.Status.validate() }

// SetCriterionScript replaces the check command of the criterion with the given
// id; an empty script clears it.
type SetCriterionScript struct {
	ID     string `json:"id"`
	Script string `json:"script"`
}

// OpKind returns "set_criterion_script".
func (SetCriterionScript) OpKind() string { return "set_criterion_script" }

// AddAttachment sets the named attachment on a note, doc, or log to the
// content with the given LFS oid and size. Attachments resolve LWW by Name in
// linearization order at fold time, so re-attaching an existing name replaces
// it on every replica.
type AddAttachment struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// OpKind returns "add_attachment".
func (AddAttachment) OpKind() string { return "add_attachment" }

func (o AddAttachment) validate() error {
	return Attachment(o).validate()
}

// RemoveAttachment removes the named attachment from a note, doc, or log.
type RemoveAttachment struct {
	Name string `json:"name"`
}

// OpKind returns "remove_attachment".
func (RemoveAttachment) OpKind() string { return "remove_attachment" }

func (o RemoveAttachment) validate() error { return validateAttachmentName(o.Name) }

// CreateRunbook is the root operation of a runbook chain. The nonce makes
// otherwise-identical creates hash to distinct entity ids. Initial steps ride
// in the same create pack as AddStep ops.
type CreateRunbook struct {
	Nonce       string   `json:"nonce"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

// OpKind returns "create_runbook".
func (CreateRunbook) OpKind() string { return "create_runbook" }

// CreateKind returns KindRunbook.
func (CreateRunbook) CreateKind() Kind { return KindRunbook }

// AddStep adds one step to a runbook. The id is a client-generated nonce that
// makes the add idempotent; Position places the step (see PositionBetween).
type AddStep struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Command  string `json:"command"`
	Position string `json:"position"`
}

// OpKind returns "add_step".
func (AddStep) OpKind() string { return "add_step" }

func (o AddStep) validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: add_step id is empty", ErrInvalidValue)
	}
	return validatePosition(o.Position)
}

// RemoveStep removes the step with the given id. Run results that reference the
// step keep their recorded StepID — history is not rewritten.
type RemoveStep struct {
	ID string `json:"id"`
}

// OpKind returns "remove_step".
func (RemoveStep) OpKind() string { return "remove_step" }

func (o RemoveStep) validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: remove_step id is empty", ErrInvalidValue)
	}
	return nil
}

// SetStepText replaces the instruction text of the step with the given id.
type SetStepText struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// OpKind returns "set_step_text".
func (SetStepText) OpKind() string { return "set_step_text" }

func (o SetStepText) validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: set_step_text id is empty", ErrInvalidValue)
	}
	return nil
}

// SetStepCommand replaces the command of the step with the given id; an empty
// command clears it.
type SetStepCommand struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

// OpKind returns "set_step_command".
func (SetStepCommand) OpKind() string { return "set_step_command" }

func (o SetStepCommand) validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: set_step_command id is empty", ErrInvalidValue)
	}
	return nil
}

// SetStepPosition moves the step with the given id to a new position.
type SetStepPosition struct {
	ID       string `json:"id"`
	Position string `json:"position"`
}

// OpKind returns "set_step_position".
func (SetStepPosition) OpKind() string { return "set_step_position" }

func (o SetStepPosition) validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: set_step_position id is empty", ErrInvalidValue)
	}
	return validatePosition(o.Position)
}

// StartRun begins a tracked run of a runbook. The id is a client-generated
// nonce that makes the start idempotent; Task optionally cites the task this
// run serves — a loose reference, never folded into the task. Runner and start
// time come from the carrying commit at fold time.
type StartRun struct {
	ID   string   `json:"id"`
	Task EntityID `json:"task"`
}

// OpKind returns "start_run".
func (StartRun) OpKind() string { return "start_run" }

func (o StartRun) validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: start_run id is empty", ErrInvalidValue)
	}
	return nil
}

// SetRunStepStatus records the outcome of one step within a run, upserting the
// run's result for that step id. The note carries free-form context (error
// output, a skip reason); recorder and timestamp come from the carrying commit.
type SetRunStepStatus struct {
	RunID  string           `json:"run_id"`
	StepID string           `json:"step_id"`
	Status StepResultStatus `json:"status"`
	Note   string           `json:"note"`
}

// OpKind returns "set_run_step_status".
func (SetRunStepStatus) OpKind() string { return "set_run_step_status" }

func (o SetRunStepStatus) validate() error {
	if o.RunID == "" {
		return fmt.Errorf("%w: set_run_step_status run_id is empty", ErrInvalidValue)
	}
	if o.StepID == "" {
		return fmt.Errorf("%w: set_run_step_status step_id is empty", ErrInvalidValue)
	}
	return o.Status.validate()
}

// FinishRun ends a run with a terminal status; RunRunning is not a finish and
// fails validation. Finish time comes from the carrying commit.
type FinishRun struct {
	ID     string    `json:"id"`
	Status RunStatus `json:"status"`
}

// OpKind returns "finish_run".
func (FinishRun) OpKind() string { return "finish_run" }

func (o FinishRun) validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: finish_run id is empty", ErrInvalidValue)
	}
	if err := o.Status.validate(); err != nil {
		return err
	}
	if o.Status == RunRunning {
		return fmt.Errorf("%w: finish_run status %q", ErrInvalidValue, o.Status)
	}
	return nil
}

// SetRunbookStatus moves a runbook between active and archived.
type SetRunbookStatus struct {
	Status RunbookStatus `json:"status"`
}

// OpKind returns "set_runbook_status".
func (SetRunbookStatus) OpKind() string { return "set_runbook_status" }

func (o SetRunbookStatus) validate() error { return o.Status.validate() }

// Checkpoint compacts an entity's history into a single seed. State is the
// full folded snapshot of every commit in CoversShas, CoversLamport is the
// lamport of the covered tip, and EntityID is the immutable root sha the
// snapshot belongs to. Checkpoint is always appended, never a root, so it
// never changes an entity id: a fold uses the newest seed-safe checkpoint as
// its starting snapshot and treats every other checkpoint as a no-op. The pack
// codec carries State kind-tagged (note, doc, log, task, sprint, project, or
// runbook) so it decodes back to the concrete
// model.Note/Doc/Log/Task/Sprint/Project/Runbook; the snapshot's kind drives
// fold dispatch.
type Checkpoint struct {
	EntityID      EntityID
	State         Snapshot
	CoversLamport Lamport
	CoversShas    []SHA
}

// OpKind returns "checkpoint".
func (Checkpoint) OpKind() string { return "checkpoint" }
