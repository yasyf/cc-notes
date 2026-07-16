package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// notFoundHintError enriches a store.ErrNotFound miss with the sibling kinds the
// same id prefix resolves to, so a wrong-kind lookup (e.g. `doc show` of a note
// id) points at the right kind and the kind-agnostic `cc-notes show`. It unwraps
// to the original miss, so it still classifies as not-found (exit 3).
type notFoundHintError struct {
	err    error
	prefix string
	kinds  []model.Kind
}

func (e *notFoundHintError) Error() string { return e.err.Error() }

func (e *notFoundHintError) Unwrap() error { return e.err }

// hintLine renders the remediation Hint appends: the kind(s) the missed prefix
// actually names, plus the kind-agnostic `cc-notes show`. It names no verb —
// load backs show/edit/rm/status alike — so it never assumes the caller's verb
// applies to the other kind.
func (e *notFoundHintError) hintLine() string {
	if len(e.kinds) == 1 {
		return fmt.Sprintf(`%q is a %s — "cc-notes show %s" resolves it (or use a %s-scoped command)`,
			e.prefix, e.kinds[0], e.prefix, e.kinds[0])
	}
	return fmt.Sprintf(`%q matches %s — "cc-notes show ID" resolves any kind`, e.prefix, joinKinds(e.kinds))
}

// crossKindHint wraps notFound in a hint naming the other kinds the missed
// prefix cleanly resolves to. A sibling ambiguity or read error (which would
// keep `cc-notes show <prefix>` from resolving), or zero matches, returns
// notFound unchanged.
func crossKindHint(ctx context.Context, s *store.Store, missed model.Kind, prefix string, notFound error) error {
	var kinds []model.Kind
	for _, kind := range model.Kinds() {
		if kind == missed {
			continue
		}
		_, err := s.Resolve(ctx, kind, prefix)
		switch {
		case err == nil:
			kinds = append(kinds, kind)
		case errors.Is(err, store.ErrNotFound):
			continue
		default:
			return notFound
		}
	}
	if len(kinds) == 0 {
		return notFound
	}
	return &notFoundHintError{err: notFound, prefix: prefix, kinds: kinds}
}

// joinKinds renders a list of two or more kinds as "a note and a task" or
// "a note, a doc, and a task".
func joinKinds(kinds []model.Kind) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = "a " + string(k)
	}
	if len(names) == 2 {
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
}
