package model

import "time"

// Snapshot is the folded state of one entity chain: a Note, Doc, Log, Task,
// Sprint, Project, or Runbook. The concrete type discriminates the entity kind.
type Snapshot interface {
	// EntityID returns the entity id: the full oid of the chain's root commit.
	EntityID() EntityID
	// Meta returns the kind-agnostic header of the snapshot.
	Meta() Meta
}

// Meta is the kind-agnostic header every consumer reads without knowing the
// concrete snapshot type. A field a kind does not model stays zero — Superseded
// is false for kinds with no supersede edge, Attachments nil for kinds that
// carry none.
type Meta struct {
	Kind        Kind
	Title       string
	Head        SHA
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Deleted     bool
	Superseded  bool
	Attachments []Attachment
}

func metaTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

// EntityID returns the note's entity id.
func (n Note) EntityID() EntityID { return n.ID }

// EntityID returns the doc's entity id.
func (d Doc) EntityID() EntityID { return d.ID }

// EntityID returns the log's entity id.
func (l Log) EntityID() EntityID { return l.ID }

// EntityID returns the task's entity id.
func (t Task) EntityID() EntityID { return t.ID }

// EntityID returns the sprint's entity id.
func (s Sprint) EntityID() EntityID { return s.ID }

// EntityID returns the project's entity id.
func (p Project) EntityID() EntityID { return p.ID }

// EntityID returns the runbook's entity id.
func (r Runbook) EntityID() EntityID { return r.ID }

// EntityID returns the investigation's entity id.
func (i Investigation) EntityID() EntityID { return i.ID }

// Meta returns the note's header.
func (n Note) Meta() Meta {
	return Meta{
		Kind:        KindNote,
		Title:       n.Title,
		Head:        n.Head,
		CreatedAt:   metaTime(n.CreatedAt),
		UpdatedAt:   metaTime(n.UpdatedAt),
		Deleted:     n.Deleted,
		Superseded:  len(n.SupersededBy) > 0,
		Attachments: n.Attachments,
	}
}

// Meta returns the doc's header.
func (d Doc) Meta() Meta {
	return Meta{
		Kind:        KindDoc,
		Title:       d.Title,
		Head:        d.Head,
		CreatedAt:   metaTime(d.CreatedAt),
		UpdatedAt:   metaTime(d.UpdatedAt),
		Deleted:     d.Deleted,
		Superseded:  len(d.SupersededBy) > 0,
		Attachments: d.Attachments,
	}
}

// Meta returns the log's header.
func (l Log) Meta() Meta {
	return Meta{
		Kind:        KindLog,
		Title:       l.Title,
		Head:        l.Head,
		CreatedAt:   metaTime(l.CreatedAt),
		UpdatedAt:   metaTime(l.UpdatedAt),
		Deleted:     l.Deleted,
		Attachments: l.Attachments,
	}
}

// Meta returns the task's header.
func (t Task) Meta() Meta {
	return Meta{
		Kind:      KindTask,
		Title:     t.Title,
		Head:      t.Head,
		CreatedAt: metaTime(t.CreatedAt),
		UpdatedAt: metaTime(t.UpdatedAt),
		Deleted:   t.Deleted,
	}
}

// Meta returns the sprint's header.
func (s Sprint) Meta() Meta {
	return Meta{
		Kind:      KindSprint,
		Title:     s.Title,
		Head:      s.Head,
		CreatedAt: metaTime(s.CreatedAt),
		UpdatedAt: metaTime(s.UpdatedAt),
		Deleted:   s.Deleted,
	}
}

// Meta returns the project's header.
func (p Project) Meta() Meta {
	return Meta{
		Kind:      KindProject,
		Title:     p.Title,
		Head:      p.Head,
		CreatedAt: metaTime(p.CreatedAt),
		UpdatedAt: metaTime(p.UpdatedAt),
		Deleted:   p.Deleted,
	}
}

// Meta returns the runbook's header.
func (r Runbook) Meta() Meta {
	return Meta{
		Kind:      KindRunbook,
		Title:     r.Title,
		Head:      r.Head,
		CreatedAt: metaTime(r.CreatedAt),
		UpdatedAt: metaTime(r.UpdatedAt),
		Deleted:   r.Deleted,
	}
}

// Meta returns the investigation's header.
func (i Investigation) Meta() Meta {
	return Meta{
		Kind:        KindInvestigation,
		Title:       i.Title,
		Head:        i.Head,
		CreatedAt:   metaTime(i.CreatedAt),
		UpdatedAt:   metaTime(i.UpdatedAt),
		Deleted:     i.Deleted,
		Superseded:  len(i.SupersededBy) > 0,
		Attachments: i.Attachments,
	}
}
