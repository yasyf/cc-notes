package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ccNotesMarketplaceJSON is the extraKnownMarketplaces entry pointing Claude
// Code at the cc-notes plugin marketplace on GitHub.
var ccNotesMarketplaceJSON = json.RawMessage(`{"source":{"source":"github","repo":"yasyf/cc-notes"}}`)

// registerPlugin enables the cc-notes Claude Code plugin in
// <root>/.claude/settings.json by shallow-merging an extraKnownMarketplaces
// entry and an enabledPlugins flag into the committed settings, preserving every
// other key and the existing key order. The skill then loads from the plugin
// (tracking the repository) instead of being copied into .claude/skills.
func registerPlugin(root string) error {
	path := filepath.Join(root, ".claude", "settings.json")
	top := orderedObject{vals: map[string]json.RawMessage{}}
	switch data, err := os.ReadFile(path); {
	case err == nil:
		if err := json.Unmarshal(data, &top); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read %s: %w", path, err)
	}

	for _, section := range []struct {
		key   string
		entry string
		value json.RawMessage
	}{
		{"extraKnownMarketplaces", "cc-notes", ccNotesMarketplaceJSON},
		{"enabledPlugins", "cc-notes@cc-notes", json.RawMessage("true")},
	} {
		sub, err := top.sub(section.key)
		if err != nil {
			return err
		}
		sub.set(section.entry, section.value)
		raw, err := json.Marshal(sub)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", section.key, err)
		}
		top.set(section.key, raw)
	}
	return writeSettings(path, top)
}

// writeSettings atomically writes top to path as 2-space-indented JSON with a
// trailing newline. HTML escaping is disabled so &, <, and > in carried-over
// values (e.g. chained hook commands) stay literal, matching capt-hook's
// diff-friendly output and avoiding escape ping-pong on the shared settings.json.
func writeSettings(path string, top orderedObject) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(top); err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	data := buf.Bytes()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// orderedObject is a JSON object that round-trips its keys in source order, so
// merging into an existing settings.json yields a minimal diff. Untouched
// values are carried verbatim as raw bytes; only modified sections are re-encoded.
type orderedObject struct {
	keys []string
	vals map[string]json.RawMessage
}

// sub decodes the named child object, or an empty object when the key is absent.
func (o orderedObject) sub(key string) (orderedObject, error) {
	sub := orderedObject{vals: map[string]json.RawMessage{}}
	raw, ok := o.vals[key]
	if !ok {
		return sub, nil
	}
	if err := json.Unmarshal(raw, &sub); err != nil {
		return orderedObject{}, fmt.Errorf("settings key %q is not an object: %w", key, err)
	}
	return sub, nil
}

// set assigns value to key, appending the key when new and keeping its position
// when it already exists.
func (o *orderedObject) set(key string, value json.RawMessage) {
	if o.vals == nil {
		o.vals = map[string]json.RawMessage{}
	}
	if _, seen := o.vals[key]; !seen {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

func (o *orderedObject) UnmarshalJSON(b []byte) error {
	o.keys = nil
	o.vals = map[string]json.RawMessage{}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected JSON object, got %v", tok)
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		o.set(key, raw)
	}
	_, err = dec.Token()
	return err
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(o.vals[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
