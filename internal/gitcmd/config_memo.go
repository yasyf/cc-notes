package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yasyf/cc-notes/model"
)

// configMemoMaxAge bounds how long one read answers, so a command that runs for
// a long time still sees an edit made outside it.
const configMemoMaxAge = time.Second

type commandMemoKey struct{}

// commandMemo holds what one command learned about each repository it touched,
// guarded by mu. One config listing answers every config question the command
// asks, so a command that also writes config pays for a single read.
type commandMemo struct {
	mu    sync.Mutex
	repos map[repoKey]*repoMemo
}

type repoKey struct {
	dir     string
	workDir string
	backend bool
}

type repoMemo struct {
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
	bare  bool
}

// WithCommandMemo returns ctx carrying a memo for one command's git reads:
// config, the author identity, and HEAD. A long-lived caller makes one per
// operation rather than holding one across operations.
func WithCommandMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, commandMemoKey{}, &commandMemo{repos: make(map[repoKey]*repoMemo)})
}

func memoOf(ctx context.Context) *commandMemo {
	memo, _ := ctx.Value(commandMemoKey{}).(*commandMemo)
	return memo
}

func (g Git) repo(memo *commandMemo) *repoMemo {
	key := repoKey{dir: g.Dir, workDir: g.workDir, backend: g.backend}
	repo := memo.repos[key]
	if repo == nil {
		repo = &repoMemo{}
		memo.repos[key] = repo
	}
	return repo
}

func (g Git) memoizes(ctx context.Context) bool {
	return memoOf(ctx) != nil && g.held == nil && os.Getenv("GIT_CONFIG") == ""
}

func forgetConfig(ctx context.Context) {
	memo := memoOf(ctx)
	if memo == nil {
		return
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	for _, repo := range memo.repos {
		repo.read = time.Time{}
	}
}

func (g Git) configEntries(ctx context.Context) ([]scopedConfigEntry, error) {
	memo := memoOf(ctx)
	memo.mu.Lock()
	defer memo.mu.Unlock()
	repo := g.repo(memo)
	if time.Since(repo.read) < configMemoMaxAge {
		return repo.entries, nil
	}
	out, err := g.run(ctx, "", "config", "--list", "--show-scope", "--show-origin", "--includes", "-z")
	if err != nil {
		return nil, fmt.Errorf("config list: %w", err)
	}
	entries, err := parseScopedConfig(out)
	if err != nil {
		return nil, err
	}
	repo.entries, repo.read = entries, time.Now()
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
		key, value, hasValue := strings.Cut(tokens[i+2], "\n")
		entries = append(entries, scopedConfigEntry{scope: tokens[i], path: strings.TrimPrefix(tokens[i+1], "file:"), key: key, value: value, bare: !hasValue})
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
	for _, entry := range entries {
		ident := strings.HasPrefix(entry.key, "user.") || strings.HasPrefix(entry.key, "author.") || strings.HasPrefix(entry.key, "committer.")
		if ident && (entry.bare || entry.key == "user.useconfigonly") {
			return "", "", false, nil
		}
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
// declines anything but a symbolic ref naming a branch that is not itself
// symbolic, which leaves a detached HEAD, a chain of symbolic refs, and the
// reftable backend's placeholder branch to git.
func (g Git) memoizedHead(ctx context.Context) (model.Branch, bool) {
	memo := memoOf(ctx)
	if memo == nil {
		return "", false
	}
	memo.mu.Lock()
	repo := g.repo(memo)
	gitDir, commonDir, located := repo.gitDir, repo.commonDir, repo.located
	memo.mu.Unlock()
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
	branch, isBranch := strings.CutPrefix(target, "refs/heads/")
	if !symbolic || !isBranch || branch == ".invalid" {
		return "", false
	}
	//nolint:gosec // G304: commonDir is the directory git itself reported, and target came from its HEAD.
	ref, err := os.ReadFile(filepath.Join(commonDir, target))
	if err == nil && bytes.HasPrefix(ref, []byte("ref:")) {
		return "", false
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	return model.Branch(branch), true
}
