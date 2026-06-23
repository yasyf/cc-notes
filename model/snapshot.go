package model

// Snapshot is the folded state of one entity chain: a Note, Doc, Task, Sprint,
// or Project. The concrete type discriminates the entity kind.
type Snapshot interface {
	// EntityID returns the entity id: the full oid of the chain's root commit.
	EntityID() EntityID
}

// EntityID returns the note's entity id.
func (n Note) EntityID() EntityID { return n.ID }

// EntityID returns the doc's entity id.
func (d Doc) EntityID() EntityID { return d.ID }

// EntityID returns the task's entity id.
func (t Task) EntityID() EntityID { return t.ID }

// EntityID returns the sprint's entity id.
func (s Sprint) EntityID() EntityID { return s.ID }

// EntityID returns the project's entity id.
func (p Project) EntityID() EntityID { return p.ID }
