// Package ccnhome resolves cc-notes' per-user state root and the paths under
// it. The root holds machine-local state that is rebuilt rather than synced:
// the daemon socket and log, the service definitions, the shared model cache,
// and one derived-state directory per indexed repository. It is the same
// ~/.cc-notes the signed helper already uses, so nothing new appears in the
// user's home directory.
package ccnhome

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	// Env overrides the per-user state root. Tests set it so derived state
	// never lands in the real home directory.
	Env = "CC_NOTES_HOME"

	rootName = ".cc-notes"
	dirPerm  = 0o750
	filePerm = 0o600

	// keyDomain separates a repository key's preimage from every other sha256
	// over a path. It is part of the on-disk layout: changing it strands every
	// existing per-repository directory.
	keyDomain = "cc-notes.index.v1\x00"
	// keyLen is the hex width of a repository key. 128 bits, because the key
	// names the directory that holds one repository's index and a collision
	// would hand it another repository's data.
	keyLen = 32
)

// Root returns the per-user state root: CC_NOTES_HOME when set, otherwise
// ~/.cc-notes. A CC_NOTES_HOME that is not an exact absolute path is an error,
// never a fallback.
func Root() (string, error) {
	if value, ok := os.LookupEnv(Env); ok {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return "", fmt.Errorf("%s %q is not an exact absolute path", Env, value)
		}
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, rootName), nil
}

func sub(elem ...string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, elem...)...), nil
}

// SocketPath returns the daemon's unix socket path. It sits directly under the
// root rather than in service/ because darwin caps sun_path at 104 bytes.
func SocketPath() (string, error) { return sub("daemon.sock") }

// LogPath returns the daemon's log file path.
func LogPath() (string, error) { return sub("daemon.log") }

// ServiceDir returns the directory holding the daemon's service definitions.
func ServiceDir() (string, error) { return sub("service") }

// ModelsDir returns the model cache — weights and wasm, shared across every
// indexed repository.
func ModelsDir() (string, error) { return sub("models") }

// RepoKey derives the index key of the repository whose git common directory
// is commonDir: the first 32 hex characters of a domain-separated sha256 over
// the fully resolved path.
//
// Symlink resolution is load-bearing, not hygiene. On darwin git reports a
// linked worktree's common directory under /private/var while the same
// repository reached through its own checkout reports the lexical /var alias,
// so two views of one repository hash apart without it — with no deliberate
// symlink anywhere.
func RepoKey(commonDir string) (string, error) {
	abs, err := filepath.Abs(commonDir)
	if err != nil {
		return "", fmt.Errorf("absolute common dir %s: %w", commonDir, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve common dir %s: %w", abs, err)
	}
	sum := sha256.Sum256([]byte(keyDomain + resolved))
	return hex.EncodeToString(sum[:])[:keyLen], nil
}

// Repo is the derived-state directory set of one indexed repository. The key
// is derived from the git common directory, never a worktree root, so every
// linked worktree of one repository shares a single index — the same identity
// the fold cache, the LFS object store, and the MCP markers already use.
type Repo struct {
	Key string
	Dir string
}

// ForRepo resolves the derived-state directories of the repository whose git
// common directory is commonDir. It creates nothing.
func ForRepo(commonDir string) (Repo, error) {
	key, err := RepoKey(commonDir)
	if err != nil {
		return Repo{}, err
	}
	dir, err := sub("repos", key)
	if err != nil {
		return Repo{}, err
	}
	return Repo{Key: key, Dir: dir}, nil
}

// Graph returns the knowledge-graph index directory.
func (r Repo) Graph() string { return filepath.Join(r.Dir, "graph-v1") }

// Embed returns the dense-embedding directory.
func (r Repo) Embed() string { return filepath.Join(r.Dir, "embed-v1") }

// QueuePending returns the directory holding queued work not yet run.
func (r Repo) QueuePending() string { return filepath.Join(r.Dir, "queue-v1", "pending") }

// QueueFailed returns the directory holding queued work that failed.
func (r Repo) QueueFailed() string { return filepath.Join(r.Dir, "queue-v1", "failed") }

// InfoPath returns the path of the repository descriptor.
func (r Repo) InfoPath() string { return filepath.Join(r.Dir, "repo.json") }

// Info describes which repository a per-repository directory belongs to, so a
// process holding only a working directory can find the right index. CommonDir
// is the resolved git common directory the key was derived from; Worktrees are
// the resolved worktree roots seen for it, sorted and unique. A bare
// repository has no worktree, so Worktrees is empty for one.
type Info struct {
	CommonDir string   `json:"common_dir"`
	Worktrees []string `json:"worktrees"`
}

// ReadInfo reads the repository descriptor.
func (r Repo) ReadInfo() (Info, error) {
	//nolint:gosec // G304: the path is this package's own state root, derived from a hashed key.
	data, err := os.ReadFile(r.InfoPath())
	if err != nil {
		return Info{}, fmt.Errorf("read %s: %w", r.InfoPath(), err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("decode %s: %w", r.InfoPath(), err)
	}
	return info, nil
}

// RecordWorktree resolves the repository whose git common directory is
// commonDir, creates its derived-state directory, and records worktree in the
// descriptor alongside any worktree already known. An empty worktree records
// the repository alone, which is what a bare repository has.
func RecordWorktree(commonDir, worktree string) (Repo, error) {
	repo, err := ForRepo(commonDir)
	if err != nil {
		return Repo{}, err
	}
	resolvedCommon, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return Repo{}, fmt.Errorf("resolve common dir %s: %w", commonDir, err)
	}
	info := Info{CommonDir: resolvedCommon}
	if existing, err := repo.ReadInfo(); err == nil {
		info.Worktrees = existing.Worktrees
	}
	if worktree != "" {
		resolvedWorktree, err := filepath.EvalSymlinks(worktree)
		if err != nil {
			return Repo{}, fmt.Errorf("resolve worktree %s: %w", worktree, err)
		}
		if !slices.Contains(info.Worktrees, resolvedWorktree) {
			info.Worktrees = append(info.Worktrees, resolvedWorktree)
			slices.Sort(info.Worktrees)
		}
	}
	if err := writeInfo(repo, info); err != nil {
		return Repo{}, err
	}
	return repo, nil
}

func writeInfo(repo Repo, info Info) error {
	if err := os.MkdirAll(repo.Dir, dirPerm); err != nil {
		return fmt.Errorf("create %s: %w", repo.Dir, err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encode %s: %w", repo.InfoPath(), err)
	}
	tmp, err := os.CreateTemp(repo.Dir, "repo.json.*")
	if err != nil {
		return fmt.Errorf("create %s: %w", repo.InfoPath(), err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", repo.InfoPath(), err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", repo.InfoPath(), err)
	}
	if err := os.Chmod(name, filePerm); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", repo.InfoPath(), err)
	}
	if err := os.Rename(name, repo.InfoPath()); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write %s: %w", repo.InfoPath(), err)
	}
	return nil
}
