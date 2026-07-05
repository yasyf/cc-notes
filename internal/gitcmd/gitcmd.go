// Package gitcmd execs the system git binary for the operations that need
// it: ref writes under real ref locks with reflog coverage (update-ref
// --stdin), fetch and push with the user's credential and SSH handling,
// config, identity, and the credential store (fill/approve/reject) for the
// LFS client. Object writes and all reads belong to internal/gitobj;
// neither package imports the other. Output parsing sticks to plumbing
// commands, with one exception: `git remote`, whose name-per-line listing
// and get-url output have no plumbing equivalent and have been stable
// since their introduction.
package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

var (
	// ErrCASMismatch reports a ref compare-and-swap failure: the ref moved
	// under us, or already exists on create.
	ErrCASMismatch = errors.New("ref compare-and-swap mismatch")
	// ErrNonFastForward reports a fetch or push update rejected because it
	// does not fast-forward.
	ErrNonFastForward = errors.New("non-fast-forward")
	// ErrDetachedHead reports that HEAD is not a symbolic ref.
	ErrDetachedHead = errors.New("detached HEAD")
	// ErrPathNotFound reports a path absent at the requested rev.
	ErrPathNotFound = errors.New("path not found at rev")
	// ErrRevNotFound reports a rev that names no commit.
	ErrRevNotFound = errors.New("rev not found")
	// ErrNoDefaultBranch reports that origin/HEAD is unset, so the remote
	// default branch cannot be resolved.
	ErrNoDefaultBranch = errors.New("no default branch")
	// ErrConfigNoMatch reports a config --unset-all whose fixed value matched
	// no line: the value was already absent.
	ErrConfigNoMatch = errors.New("config value not found")
)

// casPatterns match git's ref-transaction failures across the files and
// reftable backends: "is at <x> but expected <y>" (stale old), "reference
// already exists" (create on existing), "unable to resolve reference"
// (expected old, ref missing) — all prefixed "cannot lock ref".
var casPatterns = []string{"cannot lock ref", "is at", "but expected", "reference already exists"}

// nonFFPatterns match rejected ref updates in fetch and push output:
// " ! [rejected] ... (non-fast-forward)" and the "(fetch first)" variant
// when the remote tip is unknown locally.
var nonFFPatterns = []string{"non-fast-forward", "fetch first", "[rejected]"}

// Git runs the system git binary against one repository. Dir may be any
// path inside the repository or its worktree; every invocation passes it
// via -C.
type Git struct{ Dir string }

// commandError carries the trimmed stderr of a failed git invocation for
// sentinel classification.
type commandError struct {
	args   []string
	stderr string
	err    error
}

func (e *commandError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, e.stderr)
}

func (e *commandError) Unwrap() error { return e.err }

func (e *commandError) exitCode() int {
	var exit *exec.ExitError
	if errors.As(e.err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func (g Git) run(ctx context.Context, stdin string, args ...string) (string, error) {
	//nolint:gosec // G204: git is a fixed argv[0]; args are internal git subcommands, not user-shell input, in this CLI's own repo.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.Dir}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &commandError{args: args, stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return stdout.String(), nil
}

// classify wraps err with sentinel when the failed invocation's stderr
// contains any of the patterns; otherwise it returns err unchanged.
func classify(err, sentinel error, patterns []string) error {
	var cmdErr *commandError
	if !errors.As(err, &cmdErr) {
		return err
	}
	for _, p := range patterns {
		if strings.Contains(cmdErr.stderr, p) {
			return fmt.Errorf("%w: %w", sentinel, err)
		}
	}
	return err
}

func isZero(sha model.SHA) bool {
	for _, c := range sha {
		if c != '0' {
			return false
		}
	}
	return true
}

// UpdateRef atomically points ref at new under a real ref lock, succeeding
// only if the ref currently equals old. An empty or all-zero old means the
// ref must not exist yet (create); the zero id's length is derived from
// newRef. The unverified update form is never emitted. A CAS failure wraps
// ErrCASMismatch.
func (g Git) UpdateRef(ctx context.Context, ref string, newRef, old model.SHA) error {
	if newRef == "" {
		return fmt.Errorf("update ref %s: empty new sha", ref)
	}
	if isZero(old) {
		old = model.SHA(strings.Repeat("0", len(newRef)))
	}
	directive := fmt.Sprintf("update %s\x00%s\x00%s\x00", ref, newRef, old)
	_, err := g.run(ctx, directive, "update-ref", "--stdin", "-z")
	if err = classify(err, ErrCASMismatch, casPatterns); err != nil {
		return fmt.Errorf("update ref %s: %w", ref, err)
	}
	return nil
}

// DeleteRef atomically deletes ref locally under a real ref lock, succeeding
// only if the ref currently equals old; an empty old deletes unconditionally.
// This is the only ref delete in the system — physical prune calls it, outside
// the sync path. A CAS failure wraps ErrCASMismatch.
func (g Git) DeleteRef(ctx context.Context, ref string, old model.SHA) error {
	directive := fmt.Sprintf("delete %s\x00%s\x00", ref, old)
	_, err := g.run(ctx, directive, "update-ref", "--stdin", "-z")
	if err = classify(err, ErrCASMismatch, casPatterns); err != nil {
		return fmt.Errorf("delete ref %s: %w", ref, err)
	}
	return nil
}

// DeleteRemoteRef deletes ref on remote via `git push <remote> --delete <ref>`.
// It is best-effort and non-convergent: a stale clone that still holds the ref
// re-advertises it on its next push, so a delete never converges the way sync's
// union merge does — which is why physical prune calls it deliberately and
// outside the sync path. A rejected delete wraps ErrNonFastForward via the
// shared push classification; the caller continues past per-ref failures.
func (g Git) DeleteRemoteRef(ctx context.Context, remote, ref string) error {
	_, err := g.run(ctx, "", "push", remote, "--delete", ref)
	if err = classify(err, ErrNonFastForward, nonFFPatterns); err != nil {
		return fmt.Errorf("delete remote ref %s on %s: %w", ref, remote, err)
	}
	return nil
}

// PathOID returns the git object id of path's content at rev
// (git rev-parse rev:path), for witnesses and drift detection. A path absent
// at rev wraps ErrPathNotFound, which the reader treats as drift.
func (g Git) PathOID(ctx context.Context, rev, path string) (string, error) {
	out, err := g.run(ctx, "", "rev-parse", "--verify", "--quiet", rev+":"+path)
	if err != nil {
		var cmdErr *commandError
		if errors.As(err, &cmdErr) && cmdErr.exitCode() == 1 {
			// --quiet exits 1 with empty stdout when the object does not exist.
			return "", fmt.Errorf("path %s at %s: %w", path, rev, ErrPathNotFound)
		}
		return "", fmt.Errorf("path oid %s:%s: %w", rev, path, err)
	}
	return strings.TrimSpace(out), nil
}

// WorktreeBlobOID returns the git blob object id that hashing the on-disk
// working-tree file at path would yield (git hash-object), for checking drift
// against uncommitted edits. A missing or unreadable file wraps
// ErrPathNotFound.
func (g Git) WorktreeBlobOID(ctx context.Context, path string) (string, error) {
	out, err := g.run(ctx, "", "hash-object", "--", path)
	if err = classify(err, ErrPathNotFound, []string{"could not open"}); err != nil {
		return "", fmt.Errorf("worktree blob oid %s: %w", path, err)
	}
	return strings.TrimSpace(out), nil
}

// CommitSHA resolves rev to the full hex sha of the commit it names, for
// blame. A rev that names no commit wraps ErrRevNotFound.
func (g Git) CommitSHA(ctx context.Context, rev string) (model.SHA, error) {
	out, err := g.run(ctx, "", "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		var cmdErr *commandError
		if errors.As(err, &cmdErr) && cmdErr.exitCode() == 1 {
			// --quiet exits 1 with empty stdout when the rev names no commit.
			return "", fmt.Errorf("commit sha %s: %w", rev, ErrRevNotFound)
		}
		return "", fmt.Errorf("commit sha %s: %w", rev, err)
	}
	return model.SHA(strings.TrimSpace(out)), nil
}

// MergeBase returns the best common ancestor of a and b (git merge-base) as a
// full hex sha. When the two revs share no common ancestor — git exits 1 with
// empty stdout — it wraps ErrRevNotFound.
func (g Git) MergeBase(ctx context.Context, a, b string) (model.SHA, error) {
	out, err := g.run(ctx, "", "merge-base", a, b)
	if err != nil {
		var cmdErr *commandError
		if errors.As(err, &cmdErr) && cmdErr.exitCode() == 1 && strings.TrimSpace(out) == "" {
			return "", fmt.Errorf("merge base %s %s: %w", a, b, ErrRevNotFound)
		}
		return "", fmt.Errorf("merge base %s %s: %w", a, b, err)
	}
	return model.SHA(strings.TrimSpace(out)), nil
}

// RevRangeFileAuthors maps each file path touched in the commit range
// base..head to the distinct author emails who touched it, sorted for
// determinism. Merges and renames are excluded so paths and authorship stay
// unambiguous. An empty range returns an empty, non-nil map.
//
// The single `git log` invocation emits one record per commit, each beginning
// with a NUL byte followed by the author email on the same line; the blank
// line after the format separates that header from the commit's --name-only
// file list. Splitting on the NUL record separator and reading the first line
// of each record as the email and the remaining non-empty lines as paths
// avoids the brittle interleaving of -z with --name-only.
func (g Git) RevRangeFileAuthors(ctx context.Context, base, head string) (map[string][]string, error) {
	out, err := g.run(ctx, "", "log", base+".."+head, "--no-merges", "--no-renames", "--name-only", "--pretty=format:%x00%ae")
	if err != nil {
		return nil, fmt.Errorf("rev range file authors %s..%s: %w", base, head, err)
	}
	sets := make(map[string]map[string]struct{})
	for _, record := range strings.Split(out, "\x00") {
		lines := strings.Split(record, "\n")
		if len(lines) == 0 || lines[0] == "" {
			continue
		}
		email := lines[0]
		for _, path := range lines[1:] {
			if path == "" {
				continue
			}
			if sets[path] == nil {
				sets[path] = make(map[string]struct{})
			}
			sets[path][email] = struct{}{}
		}
	}
	authors := make(map[string][]string, len(sets))
	for path, set := range sets {
		emails := make([]string, 0, len(set))
		for email := range set {
			emails = append(emails, email)
		}
		slices.Sort(emails)
		authors[path] = emails
	}
	return authors, nil
}

// TaskTrailers returns the values of every cc-task: trailer on the commit at
// rev, in order, for blame. A commit with no such trailer returns an empty
// slice.
func (g Git) TaskTrailers(ctx context.Context, rev string) ([]string, error) {
	out, err := g.run(ctx, "", "show", "-s", "--format=%(trailers:key=cc-task,valueonly)", rev)
	if err != nil {
		return nil, fmt.Errorf("task trailers %s: %w", rev, err)
	}
	var values []string
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			values = append(values, line)
		}
	}
	return values, nil
}

// CheckRefFormat validates branch as a branch name via
// `git check-ref-format --branch`, surfacing git's own message on failure.
func (g Git) CheckRefFormat(ctx context.Context, branch string) error {
	if _, err := g.run(ctx, "", "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid branch %q: %w", branch, err)
	}
	return nil
}

// Fetch downloads from remote using exactly the given refspecs, with the
// user's credential and SSH configuration. --refmap= keeps git from also
// mapping the fetched refs through the configured remote.<r>.fetch refspecs:
// that opportunistic update would force-clobber a diverged local entity ref
// in a repo wired by Install before sync could union-merge it. A rejected
// update — a non-forced refspec that does not fast-forward — wraps
// ErrNonFastForward.
func (g Git) Fetch(ctx context.Context, remote string, refspecs ...string) error {
	_, err := g.run(ctx, "", append([]string{"fetch", "--refmap=", remote}, refspecs...)...)
	if err = classify(err, ErrNonFastForward, nonFFPatterns); err != nil {
		return fmt.Errorf("fetch %s: %w", remote, err)
	}
	return nil
}

// Push uploads to remote using the given refspecs, never forced. A rejected
// update wraps ErrNonFastForward.
func (g Git) Push(ctx context.Context, remote string, refspecs ...string) error {
	_, err := g.run(ctx, "", append([]string{"push", remote}, refspecs...)...)
	if err = classify(err, ErrNonFastForward, nonFFPatterns); err != nil {
		return fmt.Errorf("push %s: %w", remote, err)
	}
	return nil
}

// ConfigGet returns the value of key from the full config scope — system,
// global, local, worktree, later scopes winning — or the empty string when
// the key is unset everywhere.
func (g Git) ConfigGet(ctx context.Context, key string) (string, error) {
	out, err := g.run(ctx, "", "config", "--get", "-z", key)
	var cmdErr *commandError
	if errors.As(err, &cmdErr) && cmdErr.exitCode() == 1 && cmdErr.stderr == "" {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("config get %s: %w", key, err)
	}
	return strings.TrimSuffix(out, "\x00"), nil
}

// ConfigGetAll returns every value of key in the repository-local config,
// in order, or an empty slice when the key is unset.
func (g Git) ConfigGetAll(ctx context.Context, key string) ([]string, error) {
	out, err := g.run(ctx, "", "config", "--local", "--get-all", "-z", key)
	var cmdErr *commandError
	if errors.As(err, &cmdErr) && cmdErr.exitCode() == 1 && cmdErr.stderr == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config get-all %s: %w", key, err)
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00"), nil
}

// ConfigAdd appends value as a new line for key in the repository-local
// config, keeping any existing values.
func (g Git) ConfigAdd(ctx context.Context, key, value string) error {
	if _, err := g.run(ctx, "", "config", "--local", "--add", key, value); err != nil {
		return fmt.Errorf("config add %s: %w", key, err)
	}
	return nil
}

// ConfigSet sets key to value in the repository-local config, replacing a
// single existing value. Setting a multi-valued key fails.
func (g Git) ConfigSet(ctx context.Context, key, value string) error {
	if _, err := g.run(ctx, "", "config", "--local", key, value); err != nil {
		return fmt.Errorf("config set %s: %w", key, err)
	}
	return nil
}

// ConfigReplaceValue replaces every repository-local line of key equal to
// oldValue with a single line set to newValue, leaving other values of key in
// place. oldValue is matched literally (--fixed-value), so refspec
// metacharacters are not interpreted as a regexp.
func (g Git) ConfigReplaceValue(ctx context.Context, key, oldValue, newValue string) error {
	if _, err := g.run(ctx, "", "config", "--local", "--replace-all", "--fixed-value", key, newValue, oldValue); err != nil {
		return fmt.Errorf("config replace %s value %q: %w", key, oldValue, err)
	}
	return nil
}

// ConfigUnsetValue removes every repository-local line of key equal to value,
// matched literally (--fixed-value), leaving other values of key in place. When
// no line matches — the value was already unset — it wraps ErrConfigNoMatch, so
// a caller racing a concurrent unset can treat it as already done.
func (g Git) ConfigUnsetValue(ctx context.Context, key, value string) error {
	_, err := g.run(ctx, "", "config", "--local", "--unset-all", "--fixed-value", key, value)
	var cmdErr *commandError
	if errors.As(err, &cmdErr) && cmdErr.exitCode() == 5 {
		return fmt.Errorf("config unset %s value %q: %w", key, value, ErrConfigNoMatch)
	}
	if err != nil {
		return fmt.Errorf("config unset %s value %q: %w", key, value, err)
	}
	return nil
}

// HeadBranch returns the branch HEAD points at, including an unborn branch
// in a freshly initialized repository. A detached HEAD wraps
// ErrDetachedHead.
func (g Git) HeadBranch(ctx context.Context) (model.Branch, error) {
	out, err := g.run(ctx, "", "symbolic-ref", "--short", "HEAD")
	var cmdErr *commandError
	if errors.As(err, &cmdErr) && strings.Contains(cmdErr.stderr, "not a symbolic ref") {
		return "", fmt.Errorf("head branch: %w", ErrDetachedHead)
	}
	if err != nil {
		return "", fmt.Errorf("head branch: %w", err)
	}
	return model.Branch(strings.TrimSpace(out)), nil
}

// DefaultBranch returns the remote default branch — the branch origin/HEAD
// points at — with the leading "origin/" stripped, e.g. "main". It assumes the
// remote is named origin. When origin/HEAD is unset (git exits non-zero) it
// wraps ErrNoDefaultBranch so the caller can fall back.
func (g Git) DefaultBranch(ctx context.Context) (model.Branch, error) {
	out, err := g.run(ctx, "", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		var cmdErr *commandError
		if errors.As(err, &cmdErr) {
			return "", fmt.Errorf("default branch: %w", ErrNoDefaultBranch)
		}
		return "", fmt.Errorf("default branch: %w", err)
	}
	return model.Branch(strings.TrimPrefix(strings.TrimSpace(out), "origin/")), nil
}

// AuthorIdent returns the author name and email git would use for a new
// commit, honoring GIT_AUTHOR_* overrides, by parsing
// `git var GIT_AUTHOR_IDENT`. A missing identity surfaces git's own error
// plus a hint to set user.name and user.email.
func (g Git) AuthorIdent(ctx context.Context) (name, email string, err error) {
	out, err := g.run(ctx, "", "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		return "", "", fmt.Errorf("author identity (set user.name and user.email via git config): %w", err)
	}
	ident := strings.TrimSpace(out)
	i := strings.LastIndexByte(ident, '<')
	j := strings.LastIndexByte(ident, '>')
	if i < 0 || j < i {
		return "", "", fmt.Errorf("author identity: malformed ident %q", ident)
	}
	return strings.TrimSpace(ident[:i]), ident[i+1 : j], nil
}

// Remotes returns the names of the configured remotes.
func (g Git) Remotes(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "", "remote")
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	if out = strings.TrimSpace(out); out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// RemoteURL returns remote's fetch URL (git remote get-url), with any
// url.<base>.insteadOf rewrites applied — the URL git itself would dial.
func (g Git) RemoteURL(ctx context.Context, remote string) (string, error) {
	out, err := g.run(ctx, "", "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("remote url %s: %w", remote, err)
	}
	return strings.TrimSpace(out), nil
}

// Root returns the absolute path of the worktree root.
func (g Git) Root(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("worktree root: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// CommonDir returns the absolute shared git directory — the main repository's
// .git — so linked worktrees resolve to one location. git answers relative to
// the -C directory, so a relative path is joined back onto Dir.
func (g Git) CommonDir(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("common dir: %w", err)
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.Dir, path)
	}
	return path, nil
}

// HooksDir returns the absolute path of the repository's hooks directory,
// honoring a configured core.hooksPath. git resolves the path relative to
// the -C directory, so a relative answer is joined back onto Dir.
func (g Git) HooksDir(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "", "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", fmt.Errorf("hooks dir: %w", err)
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.Dir, path)
	}
	return path, nil
}
