package model

import (
	"encoding/json"
	"fmt"
)

// Kind names one entity kind. Its string value is a wire byte — it appears in
// checkpoint state tags, fold-cache headers, ref path segments, and viz URL
// segments — so the constant values are part of the storage format and must
// not change.
type Kind string

// The entity kinds, in canonical order.
const (
	KindNote    Kind = "note"
	KindDoc     Kind = "doc"
	KindLog     Kind = "log"
	KindTask    Kind = "task"
	KindSprint  Kind = "sprint"
	KindProject Kind = "project"
	KindRunbook Kind = "runbook"
)

// kindInfo binds a Kind to its zero snapshot and snapshot decoder. kindInfos is
// the single per-kind table in this file; every Kind method derives from it.
type kindInfo struct {
	kind   Kind
	zero   func() Snapshot
	decode func([]byte) (Snapshot, error)
}

var kindInfos = []kindInfo{
	{KindNote, zeroSnapshot[Note], decodeSnapshot[Note]},
	{KindDoc, zeroSnapshot[Doc], decodeSnapshot[Doc]},
	{KindLog, zeroSnapshot[Log], decodeSnapshot[Log]},
	{KindTask, zeroSnapshot[Task], decodeSnapshot[Task]},
	{KindSprint, zeroSnapshot[Sprint], decodeSnapshot[Sprint]},
	{KindProject, zeroSnapshot[Project], decodeSnapshot[Project]},
	{KindRunbook, zeroSnapshot[Runbook], decodeSnapshot[Runbook]},
}

func zeroSnapshot[T Snapshot]() Snapshot {
	var v T
	return v
}

func decodeSnapshot[T Snapshot](data []byte) (Snapshot, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Kinds returns the entity kinds in canonical order. The result is a fresh
// slice; mutating it does not corrupt the registry.
func Kinds() []Kind {
	ks := make([]Kind, len(kindInfos))
	for i, info := range kindInfos {
		ks[i] = info.kind
	}
	return ks
}

// ParseKind returns the Kind whose wire value is s, or ErrInvalidValue if s
// names no kind.
func ParseKind(s string) (Kind, error) {
	for _, info := range kindInfos {
		if string(info.kind) == s {
			return info.kind, nil
		}
	}
	return "", fmt.Errorf("%w: kind %q", ErrInvalidValue, s)
}

// Zero returns the zero-valued snapshot of k's kind (Note{}, Task{}, and so on).
func (k Kind) Zero() Snapshot { return k.info().zero() }

// DecodeSnapshot decodes data into k's snapshot type. It mirrors the checkpoint
// state decoder: a plain json.Unmarshal, with no unknown-field rejection.
func (k Kind) DecodeSnapshot(data []byte) (Snapshot, error) {
	snap, err := k.info().decode(data)
	if err != nil {
		return nil, fmt.Errorf("decode %s snapshot: %w", k, err)
	}
	return snap, nil
}

func (k Kind) info() kindInfo {
	for _, info := range kindInfos {
		if info.kind == k {
			return info
		}
	}
	panic(fmt.Sprintf("model: unregistered kind %q", k))
}
