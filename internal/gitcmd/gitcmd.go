// Package gitcmd execs the system git binary for the operations that need
// it: ref writes under real ref locks with reflog coverage (update-ref
// --stdin), fetch and push with the user's credential and SSH handling,
// repository-local config, and identity. Object writes and all reads belong
// to internal/gitobj; neither package imports the other. Output parsing
// sticks to plumbing commands, with one exception: `git remote`, whose
// name-per-line listing has no plumbing equivalent and has been stable
// since its introduction.
package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yasyf/cc-notes/internal/model"
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
// new. The unverified update form is never emitted. A CAS failure wraps
// ErrCASMismatch.
func (g Git) UpdateRef(ctx context.Context, ref string, new, old model.SHA) error {
	if new == "" {
		return fmt.Errorf("update ref %s: empty new sha", ref)
	}
	if isZero(old) {
		old = model.SHA(strings.Repeat("0", len(new)))
	}
	directive := fmt.Sprintf("update %s\x00%s\x00%s\x00", ref, new, old)
	_, err := g.run(ctx, directive, "update-ref", "--stdin", "-z")
	if err = classify(err, ErrCASMismatch, casPatterns); err != nil {
		return fmt.Errorf("update ref %s: %w", ref, err)
	}
	return nil
}

// DeleteRef removes ref, succeeding only if it currently equals old. old
// must be the ref's current value; the unverified delete form is never
// emitted. A CAS failure wraps ErrCASMismatch.
func (g Git) DeleteRef(ctx context.Context, ref string, old model.SHA) error {
	if isZero(old) {
		return fmt.Errorf("delete ref %s: old sha required for verified delete", ref)
	}
	directive := fmt.Sprintf("delete %s\x00%s\x00", ref, old)
	_, err := g.run(ctx, directive, "update-ref", "--stdin", "-z")
	if err = classify(err, ErrCASMismatch, casPatterns); err != nil {
		return fmt.Errorf("delete ref %s: %w", ref, err)
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

// Root returns the absolute path of the worktree root.
func (g Git) Root(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("worktree root: %w", err)
	}
	return strings.TrimSpace(out), nil
}
