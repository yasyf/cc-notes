package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// packVersion is the only wire format version this codec reads and writes.
const packVersion = 1

var (
	// ErrUnsupportedVersion reports a pack whose "v" field this codec does
	// not speak.
	ErrUnsupportedVersion = errors.New("unsupported pack version")
	// ErrUnknownKind reports an op whose "kind" discriminator is not
	// registered with the codec.
	ErrUnknownKind = errors.New("unknown op kind")
	// ErrInvalidValue reports an op field outside its enum or range.
	ErrInvalidValue = errors.New("invalid field value")
)

// Pack is one operation pack: the ops carried by a single entity commit,
// stamped with the entity's lamport clock.
type Pack struct {
	Lamport Lamport
	Ops     []Op
}

// PackCommit is one decoded commit in an entity chain: the metadata the fold
// orders by plus the commit's operation pack. AuthorTime is unix seconds.
type PackCommit struct {
	SHA        SHA
	Parents    []SHA
	Author     Actor
	AuthorTime int64
	Pack       Pack
}

// packWire mirrors the v1 wire layout. Field order is part of the storage
// format: changing it changes commit hashes and therefore entity ids.
type packWire struct {
	V       int               `json:"v"`
	Lamport Lamport           `json:"lamport"`
	Ops     []json.RawMessage `json:"ops"`
}

// MarshalJSON emits the v1 wire format, {"v":1,"lamport":N,"ops":[...]},
// byte-stable for a given Pack: fixed struct field order, no map iteration.
func (p Pack) MarshalJSON() ([]byte, error) {
	ops := make([]json.RawMessage, len(p.Ops))
	for i, op := range p.Ops {
		raw, err := marshalOp(op)
		if err != nil {
			return nil, err
		}
		ops[i] = raw
	}
	return json.Marshal(packWire{V: packVersion, Lamport: p.Lamport, Ops: ops})
}

// DecodePack parses and validates a v1 wire pack. It fails with
// ErrUnsupportedVersion on any other version, ErrUnknownKind on an
// unregistered op kind, and ErrInvalidValue on an out-of-range enum or
// priority.
func DecodePack(data []byte) (Pack, error) {
	var wire packWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return Pack{}, fmt.Errorf("decode pack: %w", err)
	}
	if wire.V != packVersion {
		return Pack{}, fmt.Errorf("%w: v=%d", ErrUnsupportedVersion, wire.V)
	}
	ops := make([]Op, len(wire.Ops))
	for i, raw := range wire.Ops {
		op, err := decodeOp(raw)
		if err != nil {
			return Pack{}, fmt.Errorf("decode op %d: %w", i, err)
		}
		ops[i] = op
	}
	return Pack{Lamport: wire.Lamport, Ops: ops}, nil
}

// marshalOp wraps op in an envelope struct that puts the "kind" discriminator
// first, followed by the op's fields in declaration order. Every op struct
// must have a case here and an entry in opDecoders; the round-trip test
// enforces coverage.
func marshalOp(op Op) ([]byte, error) {
	switch o := op.(type) {
	case CreateNote:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			CreateNote
		}{o.OpKind(), o})
	case SetTitle:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetTitle
		}{o.OpKind(), o})
	case SetBody:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetBody
		}{o.OpKind(), o})
	case SetWhen:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetWhen
		}{o.OpKind(), o})
	case AddTag:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AddTag
		}{o.OpKind(), o})
	case RemoveTag:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			RemoveTag
		}{o.OpKind(), o})
	case AddAnchor:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AddAnchor
		}{o.OpKind(), o})
	case RemoveAnchor:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			RemoveAnchor
		}{o.OpKind(), o})
	case DeleteNote:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			DeleteNote
		}{o.OpKind(), o})
	case VerifyNote:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			VerifyNote
		}{o.OpKind(), o})
	case AddSupersededBy:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AddSupersededBy
		}{o.OpKind(), o})
	case RemoveSupersededBy:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			RemoveSupersededBy
		}{o.OpKind(), o})
	case MarkStale:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			MarkStale
		}{o.OpKind(), o})
	case ClearStale:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			ClearStale
		}{o.OpKind(), o})
	case CreateTask:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			CreateTask
		}{o.OpKind(), o})
	case CreateSprint:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			CreateSprint
		}{o.OpKind(), o})
	case CreateProject:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			CreateProject
		}{o.OpKind(), o})
	case CreateDoc:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			CreateDoc
		}{o.OpKind(), o})
	case CreateLog:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			CreateLog
		}{o.OpKind(), o})
	case AppendEntry:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AppendEntry
		}{o.OpKind(), o})
	case SetDescription:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetDescription
		}{o.OpKind(), o})
	case SetType:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetType
		}{o.OpKind(), o})
	case SetPriority:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetPriority
		}{o.OpKind(), o})
	case SetStatus:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetStatus
		}{o.OpKind(), o})
	case SetAssignee:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetAssignee
		}{o.OpKind(), o})
	case Claim:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			Claim
		}{o.OpKind(), o})
	case Renew:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			Renew
		}{o.OpKind(), o})
	case Reclaim:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			Reclaim
		}{o.OpKind(), o})
	case AddLabel:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AddLabel
		}{o.OpKind(), o})
	case RemoveLabel:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			RemoveLabel
		}{o.OpKind(), o})
	case AddDep:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AddDep
		}{o.OpKind(), o})
	case RemoveDep:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			RemoveDep
		}{o.OpKind(), o})
	case LinkCommit:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			LinkCommit
		}{o.OpKind(), o})
	case UnlinkCommit:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			UnlinkCommit
		}{o.OpKind(), o})
	case SetParent:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetParent
		}{o.OpKind(), o})
	case AddComment:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AddComment
		}{o.OpKind(), o})
	case SetBranch:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetBranch
		}{o.OpKind(), o})
	case SetSprint:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetSprint
		}{o.OpKind(), o})
	case SetProject:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetProject
		}{o.OpKind(), o})
	case SetSprintStatus:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetSprintStatus
		}{o.OpKind(), o})
	case SetProjectStatus:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetProjectStatus
		}{o.OpKind(), o})
	case SetStartDate:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetStartDate
		}{o.OpKind(), o})
	case SetEndDate:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetEndDate
		}{o.OpKind(), o})
	case AddCriterion:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			AddCriterion
		}{o.OpKind(), o})
	case RemoveCriterion:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			RemoveCriterion
		}{o.OpKind(), o})
	case SetCriterionText:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetCriterionText
		}{o.OpKind(), o})
	case SetCriterionStatus:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetCriterionStatus
		}{o.OpKind(), o})
	case SetCriterionScript:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			SetCriterionScript
		}{o.OpKind(), o})
	case Checkpoint:
		return marshalCheckpoint(o)
	}
	return nil, fmt.Errorf("%w: %T", ErrUnknownKind, op)
}

// checkpointWire is the deterministic wire layout for a Checkpoint op: the
// "kind" discriminator first, then the carried entity id, the snapshot kind
// tag, the snapshot itself as raw JSON, the covered lamport, and the sorted
// covered shas. CoversShas sorts ascending before marshaling — the order is
// part of the storage format, so two replicas compacting the same frontier
// encode identical bytes. Checkpoint cannot use the embedded-envelope pattern
// the other ops use: State is an interface that needs kind-tagged decoding.
type checkpointWire struct {
	Kind          string          `json:"kind"`
	EntityID      EntityID        `json:"entity_id"`
	StateKind     string          `json:"state_kind"`
	State         json.RawMessage `json:"state"`
	CoversLamport Lamport         `json:"covers_lamport"`
	CoversShas    []SHA           `json:"covers_shas"`
}

func marshalCheckpoint(o Checkpoint) ([]byte, error) {
	var stateKind string
	switch o.State.(type) {
	case Note:
		stateKind = "note"
	case Doc:
		stateKind = "doc"
	case Log:
		stateKind = "log"
	case Task:
		stateKind = "task"
	case Sprint:
		stateKind = "sprint"
	case Project:
		stateKind = "project"
	default:
		return nil, fmt.Errorf("%w: checkpoint state %T", ErrUnknownKind, o.State)
	}
	state, err := json.Marshal(o.State)
	if err != nil {
		return nil, err
	}
	shas := slices.Clone(o.CoversShas)
	slices.Sort(shas)
	return json.Marshal(checkpointWire{
		Kind:          o.OpKind(),
		EntityID:      o.EntityID,
		StateKind:     stateKind,
		State:         state,
		CoversLamport: o.CoversLamport,
		CoversShas:    shas,
	})
}

// decodeCheckpoint reverses marshalCheckpoint, rebuilding the Snapshot from its
// kind tag — decodeAs cannot, since a plain json.Unmarshal into Checkpoint
// leaves the State interface field nil. An empty State or an unknown state_kind
// fails with ErrInvalidValue (the op kind itself is known).
func decodeCheckpoint(raw json.RawMessage) (Op, error) {
	var wire checkpointWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint: %w", err)
	}
	if len(wire.State) == 0 {
		return nil, fmt.Errorf("%w: checkpoint state is empty", ErrInvalidValue)
	}
	var state Snapshot
	switch wire.StateKind {
	case "note":
		var n Note
		if err := json.Unmarshal(wire.State, &n); err != nil {
			return nil, fmt.Errorf("unmarshal checkpoint note state: %w", err)
		}
		state = n
	case "doc":
		var d Doc
		if err := json.Unmarshal(wire.State, &d); err != nil {
			return nil, fmt.Errorf("unmarshal checkpoint doc state: %w", err)
		}
		state = d
	case "log":
		var l Log
		if err := json.Unmarshal(wire.State, &l); err != nil {
			return nil, fmt.Errorf("unmarshal checkpoint log state: %w", err)
		}
		state = l
	case "task":
		var t Task
		if err := json.Unmarshal(wire.State, &t); err != nil {
			return nil, fmt.Errorf("unmarshal checkpoint task state: %w", err)
		}
		state = t
	case "sprint":
		var s Sprint
		if err := json.Unmarshal(wire.State, &s); err != nil {
			return nil, fmt.Errorf("unmarshal checkpoint sprint state: %w", err)
		}
		state = s
	case "project":
		var p Project
		if err := json.Unmarshal(wire.State, &p); err != nil {
			return nil, fmt.Errorf("unmarshal checkpoint project state: %w", err)
		}
		state = p
	default:
		return nil, fmt.Errorf("%w: checkpoint state_kind %q", ErrInvalidValue, wire.StateKind)
	}
	return Checkpoint{
		EntityID:      wire.EntityID,
		State:         state,
		CoversLamport: wire.CoversLamport,
		CoversShas:    wire.CoversShas,
	}, nil
}

func decodeOp(raw json.RawMessage) (Op, error) {
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &disc); err != nil {
		return nil, err
	}
	decode, ok := opDecoders[disc.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, disc.Kind)
	}
	return decode(raw)
}

// opDecoders maps each wire kind to its decoder; the round-trip test asserts
// it covers every op struct.
var opDecoders = map[string]func(json.RawMessage) (Op, error){
	CreateNote{}.OpKind():         decodeAs[CreateNote],
	SetTitle{}.OpKind():           decodeAs[SetTitle],
	SetBody{}.OpKind():            decodeAs[SetBody],
	SetWhen{}.OpKind():            decodeAs[SetWhen],
	AddTag{}.OpKind():             decodeAs[AddTag],
	RemoveTag{}.OpKind():          decodeAs[RemoveTag],
	AddAnchor{}.OpKind():          decodeAs[AddAnchor],
	RemoveAnchor{}.OpKind():       decodeAs[RemoveAnchor],
	DeleteNote{}.OpKind():         decodeAs[DeleteNote],
	VerifyNote{}.OpKind():         decodeAs[VerifyNote],
	AddSupersededBy{}.OpKind():    decodeAs[AddSupersededBy],
	RemoveSupersededBy{}.OpKind(): decodeAs[RemoveSupersededBy],
	MarkStale{}.OpKind():          decodeAs[MarkStale],
	ClearStale{}.OpKind():         decodeAs[ClearStale],
	CreateTask{}.OpKind():         decodeAs[CreateTask],
	CreateSprint{}.OpKind():       decodeAs[CreateSprint],
	CreateProject{}.OpKind():      decodeAs[CreateProject],
	CreateDoc{}.OpKind():          decodeAs[CreateDoc],
	CreateLog{}.OpKind():          decodeAs[CreateLog],
	AppendEntry{}.OpKind():        decodeAs[AppendEntry],
	SetDescription{}.OpKind():     decodeAs[SetDescription],
	SetType{}.OpKind():            decodeAs[SetType],
	SetPriority{}.OpKind():        decodeAs[SetPriority],
	SetStatus{}.OpKind():          decodeAs[SetStatus],
	SetAssignee{}.OpKind():        decodeAs[SetAssignee],
	Claim{}.OpKind():              decodeAs[Claim],
	Renew{}.OpKind():              decodeAs[Renew],
	Reclaim{}.OpKind():            decodeAs[Reclaim],
	AddLabel{}.OpKind():           decodeAs[AddLabel],
	RemoveLabel{}.OpKind():        decodeAs[RemoveLabel],
	AddDep{}.OpKind():             decodeAs[AddDep],
	RemoveDep{}.OpKind():          decodeAs[RemoveDep],
	LinkCommit{}.OpKind():         decodeAs[LinkCommit],
	UnlinkCommit{}.OpKind():       decodeAs[UnlinkCommit],
	SetParent{}.OpKind():          decodeAs[SetParent],
	AddComment{}.OpKind():         decodeAs[AddComment],
	SetBranch{}.OpKind():          decodeAs[SetBranch],
	SetSprint{}.OpKind():          decodeAs[SetSprint],
	SetProject{}.OpKind():         decodeAs[SetProject],
	SetSprintStatus{}.OpKind():    decodeAs[SetSprintStatus],
	SetProjectStatus{}.OpKind():   decodeAs[SetProjectStatus],
	SetStartDate{}.OpKind():       decodeAs[SetStartDate],
	SetEndDate{}.OpKind():         decodeAs[SetEndDate],
	AddCriterion{}.OpKind():       decodeAs[AddCriterion],
	RemoveCriterion{}.OpKind():    decodeAs[RemoveCriterion],
	SetCriterionText{}.OpKind():   decodeAs[SetCriterionText],
	SetCriterionStatus{}.OpKind(): decodeAs[SetCriterionStatus],
	SetCriterionScript{}.OpKind(): decodeAs[SetCriterionScript],
	Checkpoint{}.OpKind():         decodeCheckpoint,
}

func decodeAs[T Op](raw json.RawMessage) (Op, error) {
	var op T
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", op.OpKind(), err)
	}
	if v, ok := any(op).(interface{ validate() error }); ok {
		if err := v.validate(); err != nil {
			return nil, err
		}
	}
	return op, nil
}
