package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yasyf/cc-notes/internal/managed"
)

const (
	trackingLockPoll   = 50 * time.Millisecond
	trackingLockDomain = "cc-notes.tracking-lock.v1\x00"
	syncTurnDomain     = "cc-notes.sync-turn.v1\x00"
)

// lockWait bounds every wait on a records-repository file lock. A holder is a
// git transfer or a rewrite of one small file; one that keeps the lock longer
// is hung, and its waiters fail with ErrLockHeld instead of parking behind it.
var lockWait = 2 * time.Minute

// ErrLockHeld reports a file lock another process kept for all of lockWait.
var ErrLockHeld = errors.New("lock held by another process")

// TrackingLock blocks until this process holds the records repository's lock
// named key and returns the locked file, released by Close. A git command
// writing the tracking refs key guards runs through RecordsGit.Holding(lock),
// whose guardian keeps the lock until that git exits, even past this process.
// A cancelled ctx fails the wait; a holder that keeps the lock for lockWait
// fails it with ErrLockHeld.
func (s *Store) TrackingLock(ctx context.Context, key string) (*os.File, error) {
	dir := filepath.Join(s.recordsCommonDir, filepath.Dir(LocalPushInclude))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("tracking lock %s: %w", key, err)
	}
	//nolint:gosec // G304: the lock is this records repository's own file under its git common directory, named by a digest.
	lock, err := os.OpenFile(filepath.Join(dir, trackingLockName(key)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("tracking lock %s: %w", key, err)
	}
	if err := waitLock(ctx, lock); err != nil {
		return nil, errors.Join(fmt.Errorf("tracking lock %s: %w", key, err), lock.Close())
	}
	if err := managed.RecordHolder(lock); err != nil {
		return nil, fmt.Errorf("tracking lock %s: %w", key, err)
	}
	return lock, nil
}

// waitLock takes an exclusive flock on lock, polling so that ctx and lockWait
// both bound the wait.
func waitLock(ctx context.Context, lock *os.File) error {
	deadline := time.NewTimer(lockWait)
	defer deadline.Stop()
	poll := time.NewTicker(trackingLockPoll)
	defer poll.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
		case err == nil:
			return nil
		case !errors.Is(err, syscall.EWOULDBLOCK):
			return err
		}
		select {
		case <-ctx.Done():
		case <-deadline.C:
			return fmt.Errorf("%w for %s: %s held %s", ErrLockHeld, lockWait, lock.Name(), managed.DescribeHolder(lock))
		case <-poll.C:
		}
	}
}

func trackingLockName(key string) string {
	sum := sha256.Sum256([]byte(trackingLockDomain + key))
	return "tracking-" + hex.EncodeToString(sum[:]) + ".lock"
}

// SyncTurn is one process's turn to run the sync named by its key; at most one
// process holds a key's turn.
type SyncTurn struct {
	lock *os.File
	path string
}

// TakeSyncTurn records that key's sync is wanted and returns the turn to run
// it, or nil when another process holds the turn: that process runs the sync
// again once its current run ends.
func (s *Store) TakeSyncTurn(key string) (*SyncTurn, error) {
	dir := filepath.Join(s.recordsCommonDir, filepath.Dir(LocalPushInclude))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("sync turn %s: %w", key, err)
	}
	sum := sha256.Sum256([]byte(syncTurnDomain + key))
	path := filepath.Join(dir, "sync-"+hex.EncodeToString(sum[:]))
	if err := os.WriteFile(path+".wanted", nil, 0o600); err != nil {
		return nil, fmt.Errorf("sync turn %s: %w", key, err)
	}
	return takeSyncTurn(path)
}

// Next ends the turn and returns the next one when the sync was wanted again
// since this turn began, or nil when it was not or another process took it.
func (t *SyncTurn) Next() (*SyncTurn, error) {
	if err := t.lock.Close(); err != nil {
		return nil, err
	}
	switch _, err := os.Stat(t.path + ".wanted"); {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return takeSyncTurn(t.path)
}

// Close ends the turn without running the sync again.
func (t *SyncTurn) Close() error { return t.lock.Close() }

func takeSyncTurn(path string) (*SyncTurn, error) {
	//nolint:gosec // G304: the lock is this records repository's own file under its git common directory, named by a digest.
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	switch err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case errors.Is(err, syscall.EWOULDBLOCK):
		return nil, lock.Close()
	case err != nil:
		return nil, errors.Join(err, lock.Close())
	}
	if err := os.Remove(path + ".wanted"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.Join(err, lock.Close())
	}
	if err := managed.RecordHolder(lock); err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	return &SyncTurn{lock: lock, path: path}, nil
}
