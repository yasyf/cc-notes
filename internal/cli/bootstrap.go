package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// defaultRemote is the remote every mutating command best-effort wires
// before writing.
const defaultRemote = "origin"

// repoDir resolves the directory a command operates on: the --repo flag value
// when set, otherwise the working directory. A set-but-missing --repo is a usage
// error rather than a silent walk-up — go-git's DetectDotGit ascends from a
// nonexistent start path, so an unvalidated typo would open the ancestor repo.
func repoDir(cmd *cobra.Command) (string, error) {
	repo, err := cmd.Flags().GetString("repo")
	if err != nil {
		return "", err
	}
	if repo != "" {
		if !dirExists(repo) {
			return "", &UsageError{Err: fmt.Errorf("--repo %s: not a directory", repo)}
		}
		return repo, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	return dir, nil
}

// openStore opens the store for the repository containing the working
// directory, or the one named by --repo.
func openStore(cmd *cobra.Command) (*store.Store, error) {
	dir, err := repoDir(cmd)
	if err != nil {
		return nil, err
	}
	return store.OpenContext(cmd.Context(), dir)
}

// openClient opens the notes.Client for the repository containing the working
// directory, or the one named by --repo — the client-only opener for commands
// that need no *store.Store.
func openClient(cmd *cobra.Command) (*notes.Client, error) {
	dir, err := repoDir(cmd)
	if err != nil {
		return nil, err
	}
	return notes.Open(dir)
}

// openStoreClient opens both the store — the surface the print/DTO layer and
// autoInstall still drive — and the notes.Client that owns the task domain
// logic, over the repository containing the working directory, or the one named
// by --repo.
func openStoreClient(cmd *cobra.Command) (*store.Store, *notes.Client, error) {
	dir, err := repoDir(cmd)
	if err != nil {
		return nil, nil, err
	}
	s, err := store.OpenContext(cmd.Context(), dir)
	if err != nil {
		return nil, nil, err
	}
	c, err := notes.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	return s, c, nil
}

// resolveBranch returns value verbatim when provided — the flag was explicitly
// passed, so a name git's check-ref-format refuses, empty included, is a
// UsageError before anything is written — otherwise the branch the working copy
// is on, resolved jj-colocation aware. A detached HEAD with no resolvable branch
// is an error telling the caller to pass the named flag (e.g. "branch" or "into").
func resolveBranch(ctx context.Context, s *store.Store, flag, value string, provided bool) (model.Branch, error) {
	if provided {
		if err := s.Git.CheckRefFormat(ctx, value); err != nil {
			return "", &UsageError{Err: err}
		}
		return model.Branch(value), nil
	}
	branch, err := s.Git.CurrentBranch(ctx)
	if errors.Is(err, gitcmd.ErrDetachedHead) {
		return "", fmt.Errorf("detached HEAD; pass --%s", flag)
	}
	return branch, err
}

// resolveBranchOrBacklog resolves the branch a command scopes to when it degrades
// gracefully instead of erroring on an unresolvable HEAD: a provided value — the
// flag was explicitly passed, empty included — is validated and returned;
// otherwise the current branch is resolved, and a detached HEAD with no
// resolvable branch reports backlog=true — the empty-branch view — rather than
// an error.
func resolveBranchOrBacklog(ctx context.Context, s *store.Store, value string, provided bool) (branch model.Branch, backlog bool, err error) {
	if provided {
		if err := s.Git.CheckRefFormat(ctx, value); err != nil {
			return "", false, &UsageError{Err: err}
		}
		return model.Branch(value), false, nil
	}
	branch, err = s.Git.CurrentBranch(ctx)
	if errors.Is(err, gitcmd.ErrDetachedHead) {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	return branch, false, nil
}

// deriveRemote resolves the remote a best-effort sync or install targets when
// none is named: the sole cc-notes-wired remote when exactly one is wired, else
// the default remote. WiredRemotes failures propagate.
func deriveRemote(ctx context.Context, g gitcmd.Git) (string, error) {
	wired, err := ccsync.WiredRemotes(ctx, g)
	if err != nil {
		return "", err
	}
	if len(wired) == 1 {
		return wired[0], nil
	}
	return defaultRemote, nil
}

// autoInstall best-effort wires the derived remote's refspecs before a
// write: a repository without the remote is left alone, any other failure
// is loud. Config lines it actually added are announced once on stderr —
// including the push.default override when the HEAD push refspec is new —
// so the silent first mutating command never changes git push behavior
// invisibly.
func autoInstall(ctx context.Context, cmd *cobra.Command, g gitcmd.Git) error {
	remote, err := deriveRemote(ctx, g)
	if err != nil {
		return err
	}
	report, err := ccsync.Install(ctx, g, remote)
	switch {
	case errors.Is(err, ccsync.ErrRemoteNotFound):
		return nil
	case err != nil:
		return err
	case len(report.Added) == 0 && len(report.Removed) == 0:
		return nil
	}
	stderr := cmd.ErrOrStderr()
	if len(report.Added) > 0 {
		if _, err := fmt.Fprintf(stderr, "cc-notes: installed refspecs in .git/config for %q: %s\n",
			remote, strings.Join(report.Added, "; ")); err != nil {
			return err
		}
	}
	if len(report.Removed) > 0 {
		if _, err := fmt.Fprintf(stderr, "cc-notes: removed pre-fix fetch refspecs a plain \"git fetch --prune\" would use to delete unsynced refs: %s\n",
			strings.Join(report.Removed, "; ")); err != nil {
			return err
		}
	}
	if report.HeadPushAdded {
		if _, err := fmt.Fprintf(stderr, "cc-notes: note: \"git push\" now pushes the current branch to its same-named remote branch (remote.%s.push overrides push.default)\n",
			remote); err != nil {
			return err
		}
	}
	return nil
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
