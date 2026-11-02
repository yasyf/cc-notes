package sourcegraph

import (
	"strings"

	"github.com/yasyf/cc-notes/model"
)

func (m *Map) diff(before, after model.SHA, visit func(Change) error) error {
	if before == after {
		return nil
	}
	m.work.DiffPairs++
	if m.shared != nil {
		m.shared.diffPairs.Add(1)
	}
	if before == "" {
		return m.emit(after, false, visit)
	}
	if after == "" {
		return m.emit(before, true, visit)
	}
	a, err := m.load(before)
	if err != nil {
		return err
	}
	b, err := m.load(after)
	if err != nil {
		return err
	}
	switch {
	case a.Prefix == b.Prefix:
		if a.value != b.value {
			if err := m.emitTerminal(a, b, visit); err != nil {
				return err
			}
		}
		for i := range a.Children {
			if a.Children[i].Hash == b.Children[i].Hash {
				continue
			}
			left, _, err := m.child(a, i)
			if err != nil {
				return err
			}
			right, _, err := m.child(b, i)
			if err != nil {
				return err
			}
			if err := m.diff(left, right, visit); err != nil {
				return err
			}
		}
		return nil
	case strings.HasPrefix(b.Prefix, a.Prefix):
		if a.value != "" {
			if err := m.emitTerminal(a, nil, visit); err != nil {
				return err
			}
		}
		selected := strings.IndexByte(nibbles, b.Prefix[len(a.Prefix)])
		for i := range a.Children {
			left, _, err := m.child(a, i)
			if err != nil {
				return err
			}
			right := model.SHA("")
			if i == selected {
				right = after
			}
			if err := m.diff(left, right, visit); err != nil {
				return err
			}
		}
		return nil
	case strings.HasPrefix(a.Prefix, b.Prefix):
		if b.value != "" {
			if err := m.emitTerminal(nil, b, visit); err != nil {
				return err
			}
		}
		selected := strings.IndexByte(nibbles, a.Prefix[len(b.Prefix)])
		for i := range b.Children {
			right, _, err := m.child(b, i)
			if err != nil {
				return err
			}
			left := model.SHA("")
			if i == selected {
				left = before
			}
			if err := m.diff(left, right, visit); err != nil {
				return err
			}
		}
		return nil
	case a.Prefix < b.Prefix:
		if err := m.emit(before, true, visit); err != nil {
			return err
		}
		return m.emit(after, false, visit)
	default:
		if err := m.emit(after, false, visit); err != nil {
			return err
		}
		return m.emit(before, true, visit)
	}
}

func (m *Map) emit(root model.SHA, before bool, visit func(Change) error) error {
	return m.prefix(root, "", func(entry Entry) error {
		change := Change{Key: entry.Key}
		if before {
			change.Before, change.BeforeExists = entry.Value, true
		} else {
			change.After, change.AfterExists = entry.Value, true
		}
		return visit(change)
	})
}

func (m *Map) emitTerminal(before, after *node, visit func(Change) error) error {
	change := Change{}
	if before != nil && before.value != "" {
		entry, err := m.entry(before)
		if err != nil {
			return err
		}
		change.Key, change.Before, change.BeforeExists = entry.Key, entry.Value, true
	}
	if after != nil && after.value != "" {
		entry, err := m.entry(after)
		if err != nil {
			return err
		}
		change.Key, change.After, change.AfterExists = entry.Key, entry.Value, true
	}
	return visit(change)
}
