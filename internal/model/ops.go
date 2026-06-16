package model

// Op is a single immutable operation in an entity's history. Each op
// serializes as a JSON object whose first field is the "kind" discriminator;
// the set of kinds is closed and enforced by the pack codec.
type Op interface {
	// OpKind returns the wire discriminator for this operation.
	OpKind() string
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

// Checkpoint compacts an entity's history into a single seed. State is the
// full folded snapshot of every commit in CoversShas, CoversLamport is the
// lamport of the covered tip, and EntityID is the immutable root sha the
// snapshot belongs to. Checkpoint is always appended, never a root, so it
// never changes an entity id: a fold uses the newest seed-safe checkpoint as
// its starting snapshot and treats every other checkpoint as a no-op. The pack
// codec carries State kind-tagged (note or task) so it decodes back to the
// concrete model.Note or model.Task; the snapshot's kind drives fold dispatch.
type Checkpoint struct {
	EntityID      EntityID
	State         Snapshot
	CoversLamport Lamport
	CoversShas    []SHA
}

// OpKind returns "checkpoint".
func (Checkpoint) OpKind() string { return "checkpoint" }
