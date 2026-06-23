package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

// notesLinkName is the in-repo symlink the managed-default mount is presented
// at, and the anchored pattern excluded from git. It lives at the worktree
// root and points into the holder-managed mountpoint under ~/.cc-notes/mnt.
const notesLinkName = ".notes"

// notesLinkPath is the absolute path of the .notes symlink for repoRoot.
func notesLinkPath(repoRoot string) string {
	return filepath.Join(repoRoot, notesLinkName)
}

// presentNotes excludes .notes from git and points repoRoot/.notes at the
// managed-default mountpoint, returning the symlink path to advertise as the
// mount's in-repo face. It is a no-op-safe pairing of addNotesExclude and
// linkNotes for the no-argument mount path.
func presentNotes(ctx context.Context, g gitcmd.Git, repoRoot, mountpoint string) (string, error) {
	if err := addNotesExclude(ctx, g); err != nil {
		return "", err
	}
	if err := linkNotes(repoRoot, mountpoint); err != nil {
		return "", err
	}
	return notesLinkPath(repoRoot), nil
}

// absSymlinkTarget resolves a symlink's target to an absolute path, joining a
// relative target onto the link's directory. cc-notes writes absolute targets;
// the join covers a link a user created by hand.
func absSymlinkTarget(linkPath, target string) string {
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(filepath.Dir(linkPath), target)
}

// notesLinkBlocked reports whether repoRoot/.notes is occupied by something the
// symlink must not clobber — a real file or directory. The caller checks it
// before serving the mount so a conflict fails fast, rather than after Setup
// has already brought a mount live. A missing path or an existing symlink (to
// be repointed) is not blocked.
func notesLinkBlocked(repoRoot string) error {
	link := notesLinkPath(repoRoot)
	info, err := os.Lstat(link)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink == 0:
		return fmt.Errorf("%s exists and is not a symlink; remove it or pass an explicit MOUNTPOINT", link)
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("inspect %s: %w", link, err)
	}
	return nil
}

// linkNotes points repoRoot/.notes at mountpoint, the in-repo presentation of
// the holder-managed default mount. An existing symlink (ours, stale, or
// dangling) is repointed so the call is idempotent; a real file or directory
// is left untouched and reported, since clobbering it could destroy data —
// the caller passes an explicit MOUNTPOINT to opt out.
func linkNotes(repoRoot, mountpoint string) error {
	if err := notesLinkBlocked(repoRoot); err != nil {
		return err
	}
	link := notesLinkPath(repoRoot)
	// Any leftover here is a symlink (notesLinkBlocked rejected a real file or
	// directory); remove it to repoint at the current mountpoint.
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace %s: %w", link, err)
	}
	if err := os.Symlink(mountpoint, link); err != nil {
		return fmt.Errorf("link %s -> %s: %w", link, mountpoint, err)
	}
	return nil
}

// unlinkNotes removes repoRoot/.notes only when it is a symlink pointing at
// mountpoint, so a teardown never deletes a real file or a symlink the user
// repurposed. A missing link or a real file/directory at the path is a no-op;
// an unexpected stat or readlink error is surfaced rather than swallowed.
func unlinkNotes(repoRoot, mountpoint string) error {
	link := notesLinkPath(repoRoot)
	info, err := os.Lstat(link)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", link, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := os.Readlink(link)
	if err != nil {
		return fmt.Errorf("read %s: %w", link, err)
	}
	if target != mountpoint {
		return nil
	}
	if err := os.Remove(link); err != nil {
		return fmt.Errorf("remove %s: %w", link, err)
	}
	return nil
}

// addNotesExclude ensures the anchored "/.notes" pattern is present in the
// repository's .git/info/exclude, keeping the symlink out of git status
// without touching the tracked .gitignore. The common git dir resolves linked
// worktrees to the shared .git. The write is idempotent: an already-listed
// pattern is a no-op, and a missing trailing newline is normalized before the
// append so the entry lands on its own line.
func addNotesExclude(ctx context.Context, g gitcmd.Git) error {
	commonDir, err := g.CommonDir(ctx)
	if err != nil {
		return err
	}
	infoDir := filepath.Join(commonDir, "info")
	if err := os.MkdirAll(infoDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", infoDir, err)
	}
	exclude := filepath.Join(infoDir, "exclude")
	pattern := "/" + notesLinkName

	//nolint:gosec // G304: exclude is .git/info/exclude under the git common dir this command manages.
	data, err := os.ReadFile(exclude)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", exclude, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}

	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(pattern + "\n")
	if err := os.WriteFile(exclude, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", exclude, err)
	}
	return nil
}
