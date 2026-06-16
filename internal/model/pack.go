package model

import (
	"encoding/json"
	"errors"
	"fmt"
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
	case CreateTask:
		return json.Marshal(struct {
			Kind string `json:"kind"`
			CreateTask
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
	}
	return nil, fmt.Errorf("%w: %T", ErrUnknownKind, op)
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
	AddTag{}.OpKind():             decodeAs[AddTag],
	RemoveTag{}.OpKind():          decodeAs[RemoveTag],
	AddAnchor{}.OpKind():          decodeAs[AddAnchor],
	RemoveAnchor{}.OpKind():       decodeAs[RemoveAnchor],
	DeleteNote{}.OpKind():         decodeAs[DeleteNote],
	VerifyNote{}.OpKind():         decodeAs[VerifyNote],
	AddSupersededBy{}.OpKind():    decodeAs[AddSupersededBy],
	RemoveSupersededBy{}.OpKind(): decodeAs[RemoveSupersededBy],
	CreateTask{}.OpKind():         decodeAs[CreateTask],
	SetDescription{}.OpKind():     decodeAs[SetDescription],
	SetType{}.OpKind():            decodeAs[SetType],
	SetPriority{}.OpKind():        decodeAs[SetPriority],
	SetStatus{}.OpKind():          decodeAs[SetStatus],
	SetAssignee{}.OpKind():        decodeAs[SetAssignee],
	Claim{}.OpKind():              decodeAs[Claim],
	AddLabel{}.OpKind():           decodeAs[AddLabel],
	RemoveLabel{}.OpKind():        decodeAs[RemoveLabel],
	AddDep{}.OpKind():             decodeAs[AddDep],
	RemoveDep{}.OpKind():          decodeAs[RemoveDep],
	SetParent{}.OpKind():          decodeAs[SetParent],
	AddComment{}.OpKind():         decodeAs[AddComment],
	SetBranch{}.OpKind():          decodeAs[SetBranch],
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
