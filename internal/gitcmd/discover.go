package gitcmd

import (
	"bufio"
	"bytes"
	"encoding/hex"
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
	if g.backend || g.held != nil || g.scratch != "" || g.refStore != "" || slices.ContainsFunc(os.Environ(), routesDiscovery) {
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
	if stat, ok := info.Sys().(*syscall.Stat_t); !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Geteuid() {
		return "", false, true
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
	if configDeclines(filepath.Join(commonDir, "config")) || configDeclines(filepath.Join(gitDir, "config.worktree")) {
		return "", "", false
	}
	return gitDir, commonDir, true
}

// looksLikeGitDir mirrors git's is_git_directory: a HEAD that is a regular
// file holding a symbolic ref under refs/ or an object id, and objects and refs
// directories in the common directory.
func looksLikeGitDir(dir string) bool {
	//nolint:gosec // G703: dir is a candidate repository directory walked up from the caller's own checkout.
	head, err := os.Lstat(filepath.Join(dir, "HEAD"))
	if err != nil || !head.Mode().IsRegular() || head.Size() > 4096 {
		return false
	}
	//nolint:gosec // G304: as above.
	data, err := os.ReadFile(filepath.Join(dir, "HEAD"))
	if err != nil || !validHead(strings.TrimRight(string(data), "\n")) {
		return false
	}
	common := dir
	//nolint:gosec // G304: as above.
	if data, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
		common = strings.TrimRight(string(data), "\r\n")
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
	} else if !os.IsNotExist(err) {
		return false
	}
	for _, name := range []string{"objects", "refs"} {
		//nolint:gosec // G703: as above.
		if info, err := os.Stat(filepath.Join(common, name)); err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

func validHead(head string) bool {
	if target, symbolic := strings.CutPrefix(head, "ref:"); symbolic {
		return strings.HasPrefix(strings.TrimLeft(target, " \t"), "refs/")
	}
	if len(head) != 40 && len(head) != 64 {
		return false
	}
	_, err := hex.DecodeString(head)
	return err == nil
}

// routesDiscovery reports an environment variable that changes which
// repository git finds or how it reads its configuration.
func routesDiscovery(kv string) bool {
	name, _, _ := strings.Cut(kv, "=")
	return routesRepository(kv) || name == "GIT_CONFIG_PARAMETERS" || name == "GIT_CONFIG_COUNT"
}

func ownedDevice(dir string) (uint64, bool) {
	//nolint:gosec // G703: dir is the caller's checkout or a directory above it.
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

// knownExtensions are the extensions.* keys git accepts in a version 1
// repository; any other makes git refuse the repository.
var knownExtensions = []string{"noop", "noop-v1", "preciousobjects", "partialclone", "worktreeconfig", "objectformat", "compatobjectformat", "refstorage", "relativeworktrees"}

// configDeclines reports whether the config file at path, or a file it
// includes, holds anything that could make git answer differently: core.bare
// set to anything but false, a repository format or extension git may refuse,
// or a line it cannot unquote, such as an escape or an unbalanced quote. A
// line it cannot place counts as such.
func configDeclines(path string) bool {
	return configDeclinesAt(path, 0)
}

func configDeclinesAt(path string, depth int) bool {
	//nolint:gosec // G304: path is a config file inside a validated git directory, or one it includes.
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil || depth > 4 {
		return true
	}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			header, rest, closed := strings.Cut(line, "]")
			if !closed || strings.TrimSpace(rest) != "" && rest[0] != '#' && rest[0] != ';' {
				return true
			}
			name, _, _ := strings.Cut(strings.TrimPrefix(header, "["), " ")
			section = strings.ToLower(strings.Trim(name, "\t "))
			continue
		}
		if strings.Contains(line, "\\") || strings.Count(line, "\"")%2 != 0 {
			return true
		}
		key, value, _ := strings.Cut(line, "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch {
		case strings.Contains(value, "\"") && (section == "core" || section == "extensions" || section == "include" || section == "includeif"):
			return true
		case section == "core" && key == "bare" && value != "false":
			return true
		case section == "core" && key == "repositoryformatversion" && value != "0" && value != "1":
			return true
		case section == "extensions" && !slices.Contains(knownExtensions, key):
			return true
		case (section == "include" || section == "includeif") && key == "path":
			included := value
			if strings.HasPrefix(included, "~") {
				return true
			}
			if !filepath.IsAbs(included) {
				included = filepath.Join(filepath.Dir(path), included)
			}
			if configDeclinesAt(included, depth+1) {
				return true
			}
		}
	}
	return scanner.Err() != nil
}
