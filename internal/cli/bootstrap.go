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
)

// defaultRemote is the remote every mutating command best-effort wires
// before writing.
const defaultRemote = "origin"

// openStore opens the store for the repository containing the working
// directory.
func openStore() (*store.Store, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	return store.Open(dir)
}

// resolveBranch returns value verbatim when set — a name git's
// check-ref-format refuses is a UsageError before anything is written —
// otherwise the branch HEAD points at. A detached HEAD is an error telling
// the caller to pass the named flag (e.g. "branch" or "into").
func resolveBranch(ctx context.Context, s *store.Store, flag, value string) (model.Branch, error) {
	if value != "" {
		if err := s.Git.CheckRefFormat(ctx, value); err != nil {
			return "", &UsageError{Err: err}
		}
		return model.Branch(value), nil
	}
	branch, err := s.Git.HeadBranch(ctx)
	if errors.Is(err, gitcmd.ErrDetachedHead) {
		return "", fmt.Errorf("detached HEAD; pass --%s", flag)
	}
	return branch, err
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
