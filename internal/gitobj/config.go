package gitobj

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	formatcfg "github.com/go-git/go-git/v5/plumbing/format/config"
)

// ConfigValues returns every value of section.key (no subsection) in the
// config file at path, in file order, decoding exactly that file: no
// includes, no other scope, no environment. A missing file holds no values.
func ConfigValues(path, section, key string) ([]string, error) {
	cfg, err := readConfig(path)
	if err != nil {
		return nil, err
	}
	var values []string
	for _, s := range cfg.Sections {
		if !strings.EqualFold(s.Name, section) {
			continue
		}
		for _, option := range s.Options {
			if strings.EqualFold(option.Key, key) {
				values = append(values, option.Value)
			}
		}
	}
	return values, nil
}

func readConfig(path string) (*formatcfg.Config, error) {
	cfg := formatcfg.New()
	//nolint:gosec // G304: path is a git config file the caller located inside a repository's git directories.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := formatcfg.NewDecoder(bytes.NewReader(WithoutBranchSections(data))).Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	return cfg, nil
}

// WithoutBranchSections drops the well-formed [branch "..."] sections of a git
// config, which a repository with many branches is mostly made of and cc-notes
// never reads. A line ending in a backslash may continue a value or sit in a
// comment, and telling them apart takes git's full parser, so such a file comes
// back whole.
func WithoutBranchSections(data []byte) []byte {
	kept := make([]byte, 0, len(data))
	inBranch := false
	for line := range bytes.Lines(data) {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasSuffix(trimmed, []byte(`\`)) {
			return data
		}
		if bytes.HasPrefix(trimmed, []byte("[")) {
			inBranch = bytes.HasPrefix(bytes.ToLower(trimmed), []byte(`[branch "`)) && bytes.Contains(trimmed, []byte(`"]`))
		}
		if !inBranch {
			kept = append(kept, line...)
		}
	}
	return kept
}
