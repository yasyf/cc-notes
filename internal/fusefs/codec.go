//go:build fuse

package fusefs

import "github.com/yasyf/cc-notes/model"

// entityCodec is the per-kind behavior the mount dispatches on: the
// render/parse/diff/create hooks that translate an entity snapshot to and from
// its file bytes. codecOf(kind) returns one; the generic codec[S, P] adapter
// keeps each kind's implementation fully typed behind this type-erased surface.
// Render's snap and Diff's base are always of the codec's own kind.
type entityCodec interface {
	Kind() model.Kind
	// ReadOnly reports whether the kind's flat files reject writes (runbooks).
	ReadOnly() bool
	// Browsable reports whether the kind's directory also nests a browse tree
	// under each entity's short id (sprints and projects).
	Browsable() bool
	Render(snap model.Snapshot) []byte
	Diff(base model.Snapshot, data []byte) ([]model.Op, error)
	New(data []byte) ([]model.Op, error)
}

// codec adapts a kind's typed render/parse/diff/create funcs to entityCodec:
// S is the snapshot type, P the parsed document DTO.
type codec[S model.Snapshot, P any] struct {
	kind      model.Kind
	readOnly  bool
	browsable bool
	render    func(S) []byte
	parse     func([]byte) (P, error)
	diff      func(S, P) ([]model.Op, error)
	create    func(P) ([]model.Op, error)
}

func (c codec[S, P]) Kind() model.Kind                  { return c.kind }
func (c codec[S, P]) ReadOnly() bool                    { return c.readOnly }
func (c codec[S, P]) Browsable() bool                   { return c.browsable }
func (c codec[S, P]) Render(snap model.Snapshot) []byte { return c.render(snap.(S)) }

func (c codec[S, P]) Diff(base model.Snapshot, data []byte) ([]model.Op, error) {
	p, err := c.parse(data)
	if err != nil {
		return nil, err
	}
	return c.diff(base.(S), p)
}

func (c codec[S, P]) New(data []byte) ([]model.Op, error) {
	p, err := c.parse(data)
	if err != nil {
		return nil, err
	}
	return c.create(p)
}

// codecs maps every entity kind to its codec. Read-only codecs wire only
// render, so the mount rejects every write before a commit path reaches their
// parse/diff/create hooks.
var codecs = map[model.Kind]entityCodec{
	model.KindNote:          codec[model.Note, ParsedDoc]{kind: model.KindNote, render: RenderNote, parse: ParseNote, diff: DiffNote, create: NewNote},
	model.KindDoc:           codec[model.Doc, ParsedDoc]{kind: model.KindDoc, render: RenderDoc, parse: ParseDoc, diff: DiffDoc, create: NewDoc},
	model.KindLog:           codec[model.Log, ParsedLog]{kind: model.KindLog, render: RenderLog, parse: ParseLog, diff: DiffLog, create: NewLog},
	model.KindTask:          codec[model.Task, ParsedTask]{kind: model.KindTask, render: RenderTask, parse: ParseTask, diff: DiffTask, create: newTaskOps},
	model.KindSprint:        codec[model.Sprint, ParsedSprint]{kind: model.KindSprint, browsable: true, render: RenderSprint, parse: ParseSprint, diff: DiffSprint, create: NewSprint},
	model.KindProject:       codec[model.Project, ParsedProject]{kind: model.KindProject, browsable: true, render: RenderProject, parse: ParseProject, diff: DiffProject, create: NewProject},
	model.KindRunbook:       codec[model.Runbook, struct{}]{kind: model.KindRunbook, readOnly: true, render: RenderRunbook},
	model.KindInvestigation: codec[model.Investigation, struct{}]{kind: model.KindInvestigation, readOnly: true, render: RenderInvestigation},
}

// codecOf returns the codec for kind, panicking on an unregistered kind.
func codecOf(kind model.Kind) entityCodec {
	c, ok := codecs[kind]
	if !ok {
		panic("fusefs: no codec for kind " + string(kind))
	}
	return c
}

// newTaskOps creates a task, seeding its branch from the parsed document.
func newTaskOps(p ParsedTask) ([]model.Op, error) {
	return NewTask(p, model.Branch(stringValue(p.Branch)))
}
