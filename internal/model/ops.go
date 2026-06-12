package model

import "fmt"

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
	return o.Priority.validate()
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

// Promote moves a task between branch namespaces. The op is the tombstone on
// the old chain: ref deletions don't propagate through refspecs, ops do.
type Promote struct {
	From Branch `json:"from"`
	To   Branch `json:"to"`
}

// OpKind returns "promote".
func (Promote) OpKind() string { return "promote" }

func (o Promote) validate() error {
	if o.From == "" || o.To == "" {
		return fmt.Errorf("%w: promote from %q to %q", ErrInvalidValue, o.From, o.To)
	}
	return nil
}
