// Package managed owns the local authority used by managed cc-notes writers.
package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Format identifies the managed authority schema and projection contract.
const Format = "cc-notes-managed-v4"

// PageSize keeps authority writes independent of the host memory page size.
const PageSize = 4096

// QueryGenerations identifies the required versioned metadata storage.
const QueryGenerations = "query-generations"

// LegacyQueryGenerations identifies the hash-addressed graph LegacyPath holds.
const LegacyQueryGenerations = "query-generations-v1"

// LegacyQueryObjectRefs names the LegacyQueryGenerations bucket every older
// binary requires before it serves LegacyPath.
const LegacyQueryObjectRefs = "object-refs"

// ErrReindex requires an explicit import before ordinary managed operations.
var ErrReindex = errors.New("managed authority unavailable; run cc-notes reindex")

// ErrBusy reports a lock held past lockWait, naming its holder.
var ErrBusy = errors.New("managed authority busy")

var lockWait = 30 * time.Second

// Unbounded is a wait that lasts as long as the holder takes.
const Unbounded = time.Duration(math.MaxInt64)

// LockWaitEnv names a duration that shortens how long this process waits on the
// authority, for a caller such as a hook read that would rather hear "busy" now.
const LockWaitEnv = "CCN_LOCK_WAIT"

// Path names the shared records authority database.
func Path(commonDir string) string {
	return filepath.Join(commonDir, "cc-notes-managed", "authority-v3.db")
}

// LegacyPath names the authority database Path replaced, which an import
// copies without its query graph.
// TODO: remove once every machine has imported it.
func LegacyPath(commonDir string) string {
	return filepath.Join(commonDir, "cc-notes-managed", "authority-v2.db")
}

// Exists reports whether the authority database is present.
func Exists(commonDir string) bool {
	_, err := os.Stat(Path(commonDir))
	return err == nil
}

func authorityLockPath(commonDir string) string {
	return filepath.Join(commonDir, "cc-notes-managed", "authority-v1.db.lock")
}

func rebuildLockPath(commonDir string) string {
	return filepath.Join(commonDir, "cc-notes-managed", "rebuild.lock")
}

func openLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, errors.Join(ErrReindex, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: The lock path is a fixed child of the bound records common directory.
	if err != nil {
		return nil, errors.Join(ErrReindex, err)
	}
	return file, nil
}

// TryLock takes the authority lock without waiting, returning a nil file when
// another process holds it in a conflicting mode.
func TryLock(commonDir string, exclusive bool) (*os.File, error) {
	return tryLockAt(authorityLockPath(commonDir), exclusive)
}

func tryLockAt(path string, exclusive bool) (*os.File, error) {
	file, err := openLock(path)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(file.Fd()), lockMode(exclusive)|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, file.Close()
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, recordHolder(file, exclusive)
}

// Wait returns the time to wait on the authority: within, or less when
// LockWaitEnv asks for less.
func Wait(within time.Duration) (time.Duration, error) {
	raw := os.Getenv(LockWaitEnv)
	if raw == "" {
		return within, nil
	}
	wait, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", LockWaitEnv, err)
	}
	return min(wait, within), nil
}

// Lock retains the stable authority lock until its returned file is closed,
// failing with ErrBusy once another holder has kept it past Wait(lockWait), or
// twice that for a shared lock, so a read outlasts a sync's long write.
// Its identity stays fixed across authority cache generations.
func Lock(ctx context.Context, commonDir string, exclusive bool) (*os.File, error) {
	if exclusive {
		return lockAt(ctx, authorityLockPath(commonDir), true, lockWait)
	}
	return lockAt(ctx, authorityLockPath(commonDir), false, 2*lockWait)
}

// LockRebuild serializes authority rebuilds, which build without the authority
// lock, failing with ErrBusy once another rebuild has kept it past Wait(lockWait).
func LockRebuild(ctx context.Context, commonDir string) (*os.File, error) {
	return lockAt(ctx, rebuildLockPath(commonDir), true, lockWait)
}

// AwaitRebuild takes the rebuild lock once any rebuild holding it finishes,
// however long that rebuild runs, unless LockWaitEnv asks for less.
func AwaitRebuild(ctx context.Context, commonDir string) (*os.File, error) {
	return lockAt(ctx, rebuildLockPath(commonDir), true, Unbounded)
}

func lockAt(ctx context.Context, path string, exclusive bool, within time.Duration) (*os.File, error) {
	wait, err := Wait(within)
	if err != nil {
		return nil, err
	}
	file, err := openLock(path)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, file.Close())
		}
		err := syscall.Flock(int(file.Fd()), lockMode(exclusive)|syscall.LOCK_NB)
		if err == nil {
			return file, recordHolder(file, exclusive)
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(err, file.Close())
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(fmt.Errorf("%w: %s held for %s %s", ErrBusy, file.Name(), wait, describeHolder(file, exclusive)), file.Close())
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func lockMode(exclusive bool) int {
	if exclusive {
		return syscall.LOCK_EX
	}
	return syscall.LOCK_SH
}

type holder struct {
	PID     int       `json:"pid"`
	Command string    `json:"command"`
	Since   time.Time `json:"since"`
}

func recordHolder(file *os.File, exclusive bool) error {
	if !exclusive {
		return nil
	}
	data, err := json.Marshal(holder{PID: os.Getpid(), Command: strings.Join(os.Args, " "), Since: time.Now().UTC()})
	if err == nil {
		err = file.Truncate(0)
	}
	if err == nil {
		_, err = file.WriteAt(data, 0)
	}
	if err != nil {
		return errors.Join(err, file.Close())
	}
	return nil
}

// RecordHolder names this process in a lock file it holds exclusively, for
// DescribeHolder to read back to a waiter.
func RecordHolder(file *os.File) error { return recordHolder(file, true) }

// DescribeHolder names the process RecordHolder last recorded in file.
func DescribeHolder(file *os.File) string { return describeHolder(file, false) }

func describeHolder(file *os.File, exclusive bool) string {
	if exclusive && syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return "by readers"
	}
	data, err := os.ReadFile(file.Name())
	var held holder
	if err != nil || json.Unmarshal(data, &held) != nil {
		return "by a holder that left no record"
	}
	command := held.Command
	if len(command) > 160 {
		command = command[:160] + "…"
	}
	return fmt.Sprintf("by pid %d (%s) since %s", held.PID, command, held.Since.Format(time.RFC3339))
}

// Initialize creates the authority for a repository known to contain no records.
func Initialize(ctx context.Context, commonDir string) (err error) {
	lock, err := Lock(ctx, commonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if _, err := os.Stat(Path(commonDir)); err == nil {
		return fmt.Errorf("authority already exists: %s", Path(commonDir))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := bolt.Open(Path(commonDir), 0o600, &bolt.Options{PageSize: PageSize})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucket([]byte(Format))
		if err != nil {
			return err
		}
		if _, err := root.CreateBucket([]byte("refs")); err != nil {
			return err
		}
		_, err = tx.CreateBucket([]byte(QueryGenerations))
		return err
	})
}
