package model

// Snapshot is the folded state of one entity chain: a Note or a Task. The
// concrete type discriminates the entity kind.
type Snapshot interface {
	// EntityID returns the entity id: the full oid of the chain's root commit.
	EntityID() EntityID
}

// EntityID returns the note's entity id.
func (n Note) EntityID() EntityID { return n.ID }

// EntityID returns the task's entity id.
func (t Task) EntityID() EntityID { return t.ID }
