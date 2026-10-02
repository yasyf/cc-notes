package gitobj

import (
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
	//nolint:gosec // G304: path is a git config file the caller located inside a repository's git directories.
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	cfg := formatcfg.New()
	if err := formatcfg.NewDecoder(f).Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
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
