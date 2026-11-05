package gitcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yasyf/cc-notes/model"
)

// configMemoMaxAge bounds how long a memoized handle trusts its config read, so
// a retained store still sees an edit made outside this process.
const configMemoMaxAge = time.Second

// configMemo holds what one handle read about its repository: one listing
// answers every config question a command asks. It is shared by copies of the
// handle, guarded by mu.
type configMemo struct {
	mu        sync.Mutex
	read      time.Time
	entries   []scopedConfigEntry
	gitDir    string
	commonDir string
	bare      bool
	located   bool
}

type scopedConfigEntry struct {
	scope string
	path  string
	key   string
	value string
}

// Memoized returns g answering config reads, the author identity, and HEAD
// from one read of the repository's whole config. Config g writes itself shows
// in its next read.
func (g Git) Memoized() Git {
	g.memo = &configMemo{}
	return g
}

func (g Git) memoized() bool {
	return g.memo != nil && g.held == nil && os.Getenv("GIT_CONFIG") == ""
}

func (g Git) forgetConfig() {
	if g.memo == nil {
		return
	}
	g.memo.mu.Lock()
	defer g.memo.mu.Unlock()
	g.memo.read = time.Time{}
}

func (g Git) configEntries(ctx context.Context) ([]scopedConfigEntry, error) {
	g.memo.mu.Lock()
	defer g.memo.mu.Unlock()
	if time.Since(g.memo.read) < configMemoMaxAge {
		return g.memo.entries, nil
	}
	out, err := g.run(ctx, "", "config", "--list", "--show-scope", "--show-origin", "--includes", "-z")
	if err != nil {
		return nil, fmt.Errorf("config list: %w", err)
	}
	entries, err := parseScopedConfig(out)
	if err != nil {
		return nil, err
	}
	g.memo.entries, g.memo.read = entries, time.Now()
	return entries, nil
}

func parseScopedConfig(out string) ([]scopedConfigEntry, error) {
	if out == "" {
		return nil, nil
	}
	tokens := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(tokens)%3 != 0 {
		return nil, fmt.Errorf("config list: malformed output %q", out)
	}
	entries := make([]scopedConfigEntry, 0, len(tokens)/3)
	for i := 0; i < len(tokens); i += 3 {
		key, value, _ := strings.Cut(tokens[i+2], "\n")
		entries = append(entries, scopedConfigEntry{scope: tokens[i], path: strings.TrimPrefix(tokens[i+1], "file:"), key: key, value: value})
	}
	return entries, nil
}

func (g Git) memoizedConfigRegexp(ctx context.Context, pattern string) ([][2]string, error) {
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("config get-regexp %s: %w", pattern, err)
	}
	entries, err := g.configEntries(ctx)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	for _, entry := range entries {
		if matcher.MatchString(entry.key) {
			pairs = append(pairs, [2]string{entry.key, entry.value})
		}
	}
	return pairs, nil
}

// memoizedConfigLocal returns the entries of exactly the repository's config
// file, or false when the listing's paths cannot show which entries those are.
// Includes share the file's scope, so only the path tells them apart.
func (g Git) memoizedConfigLocal(ctx context.Context) ([][2]string, bool, error) {
	entries, err := g.configEntries(ctx)
	if err != nil {
		return nil, false, err
	}
	commonDir, err := g.memoCommonDir(ctx)
	if err != nil {
		return nil, false, err
	}
	want, err := os.Stat(filepath.Join(commonDir, "config"))
	if err != nil {
		return nil, false, fmt.Errorf("config list --local: %w", err)
	}
	same := make(map[string]bool)
	var pairs [][2]string
	for _, entry := range entries {
		if entry.scope != "local" {
			continue
		}
		local, found := same[entry.path]
		if !found {
			switch {
			case entry.path == ".git/config":
				local = true
			case !filepath.IsAbs(entry.path):
				return nil, false, nil
			default:
				info, err := os.Stat(entry.path)
				if err != nil {
					return nil, false, fmt.Errorf("config list --local: %w", err)
				}
				local = os.SameFile(want, info)
			}
			same[entry.path] = local
		}
		if local {
			pairs = append(pairs, [2]string{entry.key, entry.value})
		}
	}
	return pairs, true, nil
}

func (g Git) memoCommonDir(ctx context.Context) (string, error) {
	if g.backend {
		return g.Dir, nil
	}
	_, commonDir, _, err := g.Dirs(ctx)
	return commonDir, err
}

func lastConfigValue(entries []scopedConfigEntry, key string) string {
	value := ""
	for _, entry := range entries {
		if entry.key == key {
			value = entry.value
		}
	}
	return value
}

// memoizedAuthorIdent resolves name and email in git's order of precedence, or
// returns false for anything git would default, clean up, or refuse.
func (g Git) memoizedAuthorIdent(ctx context.Context) (name, email string, ok bool, err error) {
	if os.Getenv("GIT_AUTHOR_DATE") != "" {
		return "", "", false, nil
	}
	entries, err := g.configEntries(ctx)
	if err != nil {
		return "", "", false, err
	}
	pick := func(env, authorKey, userKey string) string {
		if value, set := os.LookupEnv(env); set {
			return value
		}
		if value := lastConfigValue(entries, authorKey); value != "" {
			return value
		}
		return lastConfigValue(entries, userKey)
	}
	name = pick("GIT_AUTHOR_NAME", "author.name", "user.name")
	email = pick("GIT_AUTHOR_EMAIL", "author.email", "user.email")
	return name, email, plainIdentPart(name) && plainIdentPart(email), nil
}

// plainIdentPart reports whether git's ident cleanup would leave value as is.
func plainIdentPart(value string) bool {
	const crud = ".,:;<>\"\\'"
	if value == "" || strings.ContainsAny(value, "<>\n") {
		return false
	}
	first, last := value[0], value[len(value)-1]
	return first > ' ' && last > ' ' && !strings.ContainsRune(crud, rune(first)) && !strings.ContainsRune(crud, rune(last))
}

// memoizedHead reads the branch from the HEAD file of a located repository. It
// declines anything but a plain symbolic ref, which leaves a detached HEAD and
// the reftable backend's placeholder branch to git.
func (g Git) memoizedHead() (model.Branch, bool) {
	if g.memo == nil {
		return "", false
	}
	g.memo.mu.Lock()
	gitDir, located := g.memo.gitDir, g.memo.located
	g.memo.mu.Unlock()
	if !located {
		return "", false
	}
	//nolint:gosec // G304: gitDir is the directory git itself reported.
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	target, symbolic := strings.CutPrefix(string(data), "ref:")
	target = strings.TrimSpace(target)
	if !symbolic || !strings.HasPrefix(target, "refs/") || target == "refs/heads/.invalid" {
		return "", false
	}
	return model.Branch(strings.TrimPrefix(target, "refs/heads/")), true
}
