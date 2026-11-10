package gitcmd

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// discoverDirs finds a working tree's git directories the way git's discovery
// does for the plain case: the nearest .git directory or gitdir file above
// g.Dir, owned by this user, on the same filesystem, in a repository whose
// config never sets core.bare. It declines anything else, which leaves bare
// repositories, routing variables, safe.directory, and unusual layouts to git.
func (g Git) discoverDirs() (gitDir, commonDir string, found bool) {
	if g.backend || g.held != nil || g.scratch != "" || g.refStore != "" || slices.ContainsFunc(os.Environ(), routesRepository) {
		return "", "", false
	}
	start, err := filepath.Abs(g.Dir)
	if err != nil {
		return "", "", false
	}
	if start, err = filepath.EvalSymlinks(start); err != nil {
		return "", "", false
	}
	device, owned := ownedDevice(start)
	if !owned {
		return "", "", false
	}
	for dir := start; ; {
		if looksLikeGitDir(dir) {
			return "", "", false
		}
		gitDir, ok, stop := dotGit(dir)
		if stop {
			return "", "", false
		}
		if ok {
			return gitDirs(dir, gitDir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		if next, owned := ownedDevice(parent); !owned || next != device {
			return "", "", false
		}
		dir = parent
	}
}

// dotGit resolves dir/.git to a git directory. stop reports a .git entry git
// would treat differently, such as a malformed gitdir file.
func dotGit(dir string) (gitDir string, ok, stop bool) {
	path := filepath.Join(dir, ".git")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, false
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", false, true
	}
	if info.IsDir() {
		return path, looksLikeGitDir(path), !looksLikeGitDir(path)
	}
	//nolint:gosec // G304: path is the .git file of a directory the caller is inside.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, true
	}
	target, isFile := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	if !isFile || target == "" || strings.ContainsAny(target, "\r\n") {
		return "", false, true
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return target, looksLikeGitDir(target), !looksLikeGitDir(target)
}

func gitDirs(worktree, gitDir string) (string, string, bool) {
	gitDir, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return "", "", false
	}
	commonDir := gitDir
	//nolint:gosec // G304: gitDir is the git directory discovery just validated.
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = strings.TrimRight(string(data), "\r\n")
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		if commonDir, err = filepath.EvalSymlinks(commonDir); err != nil {
			return "", "", false
		}
	} else if !os.IsNotExist(err) {
		return "", "", false
	}
	for _, dir := range []string{worktree, gitDir, commonDir} {
		if _, owned := ownedDevice(dir); !owned {
			return "", "", false
		}
	}
	if mayBeBare(filepath.Join(commonDir, "config")) || mayBeBare(filepath.Join(gitDir, "config.worktree")) {
		return "", "", false
	}
	return gitDir, commonDir, true
}

func looksLikeGitDir(dir string) bool {
	head, err := os.Stat(filepath.Join(dir, "HEAD"))
	if err != nil || !head.Mode().IsRegular() {
		return false
	}
	if refs, err := os.Stat(filepath.Join(dir, "refs")); err != nil || !refs.IsDir() {
		return false
	}
	if objects, err := os.Stat(filepath.Join(dir, "objects")); err == nil && objects.IsDir() {
		return true
	}
	_, err = os.Stat(filepath.Join(dir, "commondir"))
	return err == nil
}

func ownedDevice(dir string) (uint64, bool) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return 0, false
	}
	return uint64(stat.Dev), true //nolint:gosec // G115: device numbers are never negative.
}

// mayBeBare reports whether the config file at path sets core.bare to anything
// but false, or names it in a way a line scan cannot rule out.
func mayBeBare(path string) bool {
	//nolint:gosec // G304: path is a config file inside a validated git directory.
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if strings.Contains(line, "bare") && line != "bare = false" {
			return true
		}
	}
	return scanner.Err() != nil
}
