package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// UnknownKindError is the concrete error DecodePack returns for an
// unregistered op kind. It carries the kind so the binary can tell the user
// which op a newer cc-notes wrote; errors.Is(err, ErrUnknownKind) matches it.
type UnknownKindError struct{ Kind string }

func (e *UnknownKindError) Error() string { return fmt.Sprintf("%s: %q", ErrUnknownKind, e.Kind) }

// Is reports whether target is the ErrUnknownKind sentinel.
func (e *UnknownKindError) Is(target error) bool { return target == ErrUnknownKind }

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

// marshalOp encodes op as {"kind":"<k>",<op fields>} — the discriminator first,
// then the op's own fields in declaration order. Checkpoint alone needs
// kind-tagged encoding of its interface State, so it routes to
// marshalCheckpoint; every other op splices through spliceKind. The kind comes
// from opKinds keyed by op's concrete type, so a pointer, foreign, or nil Op —
// none a registered value type — fails with ErrUnknownKind rather than emitting
// a malformed or mis-tagged pack into the content-addressed store.
func marshalOp(op Op) ([]byte, error) {
	if cp, ok := op.(Checkpoint); ok {
		return marshalCheckpoint(cp)
	}
	kind, ok := opKinds[reflect.TypeOf(op)]
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnknownKind, op)
	}
	return spliceKind(kind, op)
}

// spliceKind builds {"kind":"<k>",<fields>} by marshaling op and inserting the
// kind tag as the first key. This byte layout is part of the storage format —
// entity ids hash it. The splice reproduces the old per-op envelope only because
// every op is a plain struct with no custom JSON (TestNoOpOrSnapshotHasCustomJSON);
// a field-less op marshals to "{}" and becomes {"kind":"<k>"}.
func spliceKind(kind string, op Op) ([]byte, error) {
	body, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}
	tag, err := json.Marshal(kind)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(tag)+len(body)+8)
	out = append(out, `{"kind":`...)
	out = append(out, tag...)
	if len(body) == 2 {
		return append(out, '}'), nil
	}
	out = append(out, ',')
	return append(out, body[1:]...), nil
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

// marshalCheckpoint encodes a Checkpoint, deriving the state_kind tag from the
// snapshot's Meta. A nil State, or one whose kind is unregistered, fails with
// ErrUnknownKind — the pack would otherwise carry an empty or bogus state_kind
// into the content-addressed store.
func marshalCheckpoint(o Checkpoint) ([]byte, error) {
	if o.State == nil {
		return nil, fmt.Errorf("%w: checkpoint state %T", ErrUnknownKind, o.State)
	}
	kind := o.State.Meta().Kind
	if _, err := ParseKind(string(kind)); err != nil {
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
		StateKind:     string(kind),
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
	kind, err := ParseKind(wire.StateKind)
	if err != nil {
		return nil, err
	}
	state, err := kind.DecodeSnapshot(wire.State)
	if err != nil {
		return nil, err
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
		return nil, &UnknownKindError{Kind: disc.Kind}
	}
	return decode(raw)
}

// opDecoders maps each wire kind to its decoder; opKinds maps each op's concrete
// type to its wire kind, the discriminator marshalOp splices by. registerOp
// populates both from one per-op registration, so the two never drift; the
// round-trip test asserts the decoder registry covers every op struct, and
// marshaling every op sample exercises the type gate. Checkpoint decodes through
// decodeCheckpoint and marshals through marshalOp's special-case, so it carries a
// decoder entry but no type-gate entry.
var (
	opDecoders = map[string]func(json.RawMessage) (Op, error){}
	opKinds    = map[reflect.Type]string{}
)

// registerOp binds op type T to its wire kind in both registries. Keying opKinds
// by the concrete reflect.Type is what lets the marshal gate reject a pointer,
// foreign, or nil Op whose OpKind() string alone would otherwise pass.
func registerOp[T Op]() {
	var zero T
	kind := zero.OpKind()
	opDecoders[kind] = decodeAs[T]
	opKinds[reflect.TypeOf(zero)] = kind
}

func init() {
	registerOp[CreateNote]()
	registerOp[SetTitle]()
	registerOp[SetBody]()
	registerOp[SetWhen]()
	registerOp[AddTag]()
	registerOp[RemoveTag]()
	registerOp[AddAnchor]()
	registerOp[RemoveAnchor]()
	registerOp[DeleteNote]()
	registerOp[VerifyNote]()
	registerOp[AddSupersededBy]()
	registerOp[RemoveSupersededBy]()
	registerOp[MarkStale]()
	registerOp[ClearStale]()
	registerOp[CreateTask]()
	registerOp[CreateSprint]()
	registerOp[CreateProject]()
	registerOp[CreateDoc]()
	registerOp[CreateLog]()
	registerOp[AppendEntry]()
	registerOp[SetDescription]()
	registerOp[SetType]()
	registerOp[SetPriority]()
	registerOp[SetStatus]()
	registerOp[SetAssignee]()
	registerOp[Claim]()
	registerOp[Renew]()
	registerOp[Reclaim]()
	registerOp[AddLabel]()
	registerOp[RemoveLabel]()
	registerOp[AddDep]()
	registerOp[RemoveDep]()
	registerOp[LinkCommit]()
	registerOp[UnlinkCommit]()
	registerOp[SetParent]()
	registerOp[AddComment]()
	registerOp[SetBranch]()
	registerOp[SetSprint]()
	registerOp[SetProject]()
	registerOp[SetSprintStatus]()
	registerOp[SetProjectStatus]()
	registerOp[SetStartDate]()
	registerOp[SetEndDate]()
	registerOp[AddCriterion]()
	registerOp[RemoveCriterion]()
	registerOp[SetCriterionText]()
	registerOp[SetCriterionStatus]()
	registerOp[SetCriterionScript]()
	registerOp[AddAttachment]()
	registerOp[RemoveAttachment]()
	registerOp[CreateRunbook]()
	registerOp[AddStep]()
	registerOp[RemoveStep]()
	registerOp[SetStepText]()
	registerOp[SetStepCommand]()
	registerOp[SetStepPosition]()
	registerOp[StartRun]()
	registerOp[SetRunStepStatus]()
	registerOp[FinishRun]()
	registerOp[SetRunbookStatus]()
	opDecoders[Checkpoint{}.OpKind()] = decodeCheckpoint
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
