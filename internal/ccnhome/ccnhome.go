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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
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

// errCorruptInfo marks a descriptor that is present but does not decode. It is
// not an impossible state: writeInfo syncs before it renames, which rules out
// this package's own crashes, but the descriptor is a file in the user's home
// directory that outlives every process — an older binary that published one
// without that sync, a restore or filesystem repair that truncated one, and a
// hand edit each leave one behind. Failing the registry scan on one would
// disable every registry read at once — Reap included, and Reap is the only
// cleanup path there is, so the garbage would be permanent.
var errCorruptInfo = errors.New("descriptor does not decode")

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
	resolved, err := resolvePath(commonDir)
	if err != nil {
		return "", err
	}
	return hashKey(resolved), nil
}

func hashKey(resolvedCommonDir string) string {
	sum := sha256.Sum256([]byte(keyDomain + resolvedCommonDir))
	return hex.EncodeToString(sum[:])[:keyLen]
}

func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", abs, err)
	}
	return resolved, nil
}

// IsGitDir reports whether dir is a git repository directory, by git's own
// is_git_directory shape: a HEAD, an objects directory, and a refs directory.
// Three Lstats and no subprocess, so the daemon can call it per level of a
// working directory's upward walk without spending its query budget on git.
func IsGitDir(dir string) bool {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
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
	resolved, err := resolvePath(commonDir)
	if err != nil {
		return Repo{}, err
	}
	return repoFor(resolved)
}

func repoFor(resolvedCommonDir string) (Repo, error) {
	key := hashKey(resolvedCommonDir)
	dir, err := sub("repos", key)
	if err != nil {
		return Repo{}, err
	}
	return Repo{Key: key, Dir: dir}, nil
}

// Graph returns the knowledge-graph index directory.
func (r Repo) Graph() string { return filepath.Join(r.Dir, "graph-v1") }

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

// Stamp identifies the exact descriptor file a read came from, so a reader that
// cached that read can prove the cache still matches the registry with a single
// Lstat. Every write goes through writeInfo, which renames a freshly created
// temp file over the descriptor, so recording a worktree, releasing a claim to
// another repository, and reaping the repository all change the file a stamp
// names — a stale claim cannot survive the check.
type Stamp struct {
	path string
	file fs.FileInfo
}

// Current reports whether the descriptor is still the exact file this stamp was
// taken from.
func (s Stamp) Current() bool {
	info, err := os.Lstat(s.path)
	return err == nil && os.SameFile(s.file, info)
}

// ReadInfo reads the repository descriptor.
func (r Repo) ReadInfo() (Info, error) {
	info, _, err := r.read()
	return info, err
}

func (r Repo) read() (Info, Stamp, error) {
	//nolint:gosec // G304: the path is this package's own state root, derived from a hashed key.
	file, err := os.Open(r.InfoPath())
	if err != nil {
		return Info{}, Stamp{}, fmt.Errorf("read %s: %w", r.InfoPath(), err)
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return Info{}, Stamp{}, fmt.Errorf("stat %s: %w", r.InfoPath(), err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return Info{}, Stamp{}, fmt.Errorf("read %s: %w", r.InfoPath(), err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, Stamp{}, fmt.Errorf("decode %s: %w: %w", r.InfoPath(), errCorruptInfo, err)
	}
	return info, Stamp{path: r.InfoPath(), file: stat}, nil
}

// RecordWorktree resolves the repository whose git common directory is
// commonDir, creates its derived-state directory, and records worktree in the
// descriptor alongside any worktree already known. An empty worktree records
// the repository alone, which is what a bare repository has. A call that would
// write back the descriptor already on disk writes nothing, so re-running
// `cc-notes init` in a registered worktree leaves the file untouched.
//
// A descriptor that does not decode counts as none, so the write republishes
// over it rather than refusing until Reap collects the directory. writeInfo's
// sync rules out this package's own crashes as the cause, but a descriptor an
// older binary published without that sync, one a restore or filesystem repair
// truncated, and one edited by hand are all still there to be read.
//
// Recording a worktree also drops it from every other repository's descriptor,
// so exactly one entry ever claims a path.
func RecordWorktree(commonDir, worktree string) (Repo, error) {
	resolvedCommon, err := resolvePath(commonDir)
	if err != nil {
		return Repo{}, err
	}
	repo, err := repoFor(resolvedCommon)
	if err != nil {
		return Repo{}, err
	}
	existing, err := repo.ReadInfo()
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errCorruptInfo) {
		return Repo{}, err
	}
	info := Info{CommonDir: resolvedCommon, Worktrees: existing.Worktrees}
	if worktree != "" {
		resolvedWorktree, err := resolvePath(worktree)
		if err != nil {
			return Repo{}, err
		}
		if err := releaseWorktree(repo.Key, resolvedWorktree); err != nil {
			return Repo{}, err
		}
		if !slices.Contains(info.Worktrees, resolvedWorktree) {
			info.Worktrees = append(slices.Clone(info.Worktrees), resolvedWorktree)
			slices.Sort(info.Worktrees)
		}
	}
	if info.CommonDir == existing.CommonDir && slices.Equal(info.Worktrees, existing.Worktrees) {
		return repo, nil
	}
	if err := writeInfo(repo, info); err != nil {
		return Repo{}, err
	}
	return repo, nil
}

func releaseWorktree(keeper, worktree string) error {
	entries, err := List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Repo.Key == keeper {
			continue
		}
		at := slices.Index(entry.Info.Worktrees, worktree)
		if at < 0 {
			continue
		}
		entry.Info.Worktrees = slices.Delete(slices.Clone(entry.Info.Worktrees), at, at+1)
		if err := writeInfo(entry.Repo, entry.Info); err != nil {
			return err
		}
	}
	return nil
}

// Entry is one registered repository: its derived-state directories, the
// descriptor recorded for it, and the stamp of the file that descriptor was
// read from.
type Entry struct {
	Repo  Repo
	Info  Info
	Stamp Stamp
}

// List returns every registered repository, ordered by key. Only directories
// count, and only those carrying a descriptor that decodes: a directory with no
// descriptor is a RecordWorktree that has created it but not yet renamed
// repo.json into place, and one whose descriptor does not decode is a
// descriptor an older binary, a truncating restore, or a hand edit left behind,
// which Reap collects. Neither is a registered repository.
func List() ([]Entry, error) {
	records, err := scan()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(records))
	for _, rec := range records {
		if rec.corrupt {
			continue
		}
		entries = append(entries, Entry{Repo: rec.repo, Info: rec.info, Stamp: rec.stamp})
	}
	return entries, nil
}

// record is one directory under repos/ as the scan found it. A corrupt record
// carries no descriptor, only the directory Reap deletes.
type record struct {
	repo    Repo
	info    Info
	stamp   Stamp
	corrupt bool
}

func scan() ([]record, error) {
	dir, err := sub("repos")
	if err != nil {
		return nil, err
	}
	names, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	records := make([]record, 0, len(names))
	for _, name := range names {
		if !name.IsDir() {
			continue
		}
		repo := Repo{Key: name.Name(), Dir: filepath.Join(dir, name.Name())}
		info, stamp, err := repo.read()
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case errors.Is(err, errCorruptInfo):
			records = append(records, record{repo: repo, corrupt: true})
		case err != nil:
			return nil, err
		default:
			records = append(records, record{repo: repo, info: info, stamp: stamp})
		}
	}
	return records, nil
}

// Reap deletes the derived state of every registered repository whose recorded
// git common directory is absent — a clone that was moved or removed — and of
// every directory whose descriptor no longer decodes, and returns the keys it
// deleted, ordered by key. Nothing else sweeps repos/: the entity garbage
// collector only ever sees the repository it runs in, so a deleted clone's
// directory would otherwise be permanent.
//
// Absent is the whole bar, and it does not distinguish a deleted clone from an
// unmounted volume: darwin removes the mount point on eject, so a path on an
// ejected volume yields the same ENOENT a deleted path does. Reap deletes the
// index in both cases. What that costs is a rebuild — the index is derived
// state, and the next `cc-notes kg build` in the clone registers the repository
// again and rebuilds it. A common directory that cannot be stat'd for some
// other reason, a permission wall above it being the reachable one, is evidence
// of nothing and aborts the sweep instead.
func Reap() ([]string, error) {
	records, err := scan()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, rec := range records {
		if !rec.corrupt {
			gone, err := absent(rec.info.CommonDir)
			if err != nil {
				return removed, err
			}
			if !gone {
				continue
			}
		}
		if err := os.RemoveAll(rec.repo.Dir); err != nil {
			return removed, fmt.Errorf("remove %s: %w", rec.repo.Dir, err)
		}
		removed = append(removed, rec.repo.Key)
	}
	return removed, nil
}

func absent(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return false, nil
	// os maps only ENOENT to ErrNotExist, and a path whose parent is a regular
	// file cannot exist either.
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return true, nil
	default:
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
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
	// The rename publishes the name atomically but not the bytes behind it:
	// without this, a crash can leave repo.json naming unwritten blocks.
	if err := tmp.Sync(); err != nil {
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
