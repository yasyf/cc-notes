// Package managed owns the local authority used by managed cc-notes writers.
package managed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Format identifies the managed authority schema and projection contract.
const Format = "cc-notes-managed-v4"

// PageSize keeps authority writes independent of the host memory page size.
const PageSize = 4096

// QueryGenerations identifies the required immutable metadata storage.
const QueryGenerations = "query-generations-v1"

// QueryObjects identifies immutable graph objects shared by metadata generations.
const QueryObjects = "objects"

// QueryObjectIDs maps content hashes to append-ordered storage locations.
const QueryObjectIDs = "object-ids"

// QueryObjectRefs counts immutable graph edges and retained generation roots.
const QueryObjectRefs = "object-refs"

// ErrReindex requires an explicit import before ordinary managed operations.
var ErrReindex = errors.New("managed authority unavailable; run cc-notes reindex")

// ErrBusy reports a lock held past lockWait, in practice by a full import.
var ErrBusy = errors.New("managed authority busy")

var lockWait = 30 * time.Second

// LockWaitEnv names a duration that shortens how long this process waits on the
// authority, for a caller such as a hook read that would rather hear "busy" now.
const LockWaitEnv = "CCN_LOCK_WAIT"

// Path names the shared records authority database.
func Path(commonDir string) string {
	return filepath.Join(commonDir, "cc-notes-managed", "authority-v2.db")
}

// Exists reports whether the authority database is present.
func Exists(commonDir string) bool {
	_, err := os.Stat(Path(commonDir))
	return err == nil
}

func openLock(commonDir string) (*os.File, error) {
	path := filepath.Join(commonDir, "cc-notes-managed", "authority-v1.db.lock")
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
	file, err := openLock(commonDir)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	err = syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, file.Close()
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
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
// failing with ErrBusy once another holder has kept it past Wait(lockWait).
// Its identity stays fixed across authority cache generations.
func Lock(ctx context.Context, commonDir string, exclusive bool) (*os.File, error) {
	wait, err := Wait(lockWait)
	if err != nil {
		return nil, err
	}
	file, err := openLock(commonDir)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, file.Close())
		}
		err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(err, file.Close())
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(fmt.Errorf("%w: %s held for %s", ErrBusy, file.Name(), wait), file.Close())
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
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
		generations, err := tx.CreateBucket([]byte(QueryGenerations))
		if err != nil {
			return err
		}
		if _, err = generations.CreateBucket([]byte(QueryObjects)); err != nil {
			return err
		}
		if _, err := generations.CreateBucket([]byte(QueryObjectIDs)); err != nil {
			return err
		}
		_, err = generations.CreateBucket([]byte(QueryObjectRefs))
		return err
	})
}
