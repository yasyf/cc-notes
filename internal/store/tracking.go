package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	trackingLockPoll   = 50 * time.Millisecond
	trackingLockDomain = "cc-notes.tracking-lock.v1\x00"
)

// lockWait bounds every wait on a records-repository file lock. A holder is a
// git transfer or a rewrite of one small file; one that keeps the lock longer
// is hung, and its waiters fail with ErrLockHeld instead of parking behind it.
var lockWait = 2 * time.Minute

// ErrLockHeld reports a file lock another process kept for all of lockWait.
var ErrLockHeld = errors.New("lock held by another process")

// TrackingLock blocks until this process holds the records repository's lock
// on remote's tracking refs and returns the locked file; closing it releases
// the hold. A git command that writes those refs runs through
// RecordsGit.Holding(lock), whose guardian process keeps the lock until that
// git exits, even past this process. A cancelled ctx fails the wait, and a
// holder that keeps the lock for lockWait fails it with ErrLockHeld.
func (s *Store) TrackingLock(ctx context.Context, remote string) (*os.File, error) {
	dir := filepath.Join(s.recordsCommonDir, filepath.Dir(LocalPushInclude))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("tracking lock %s: %w", remote, err)
	}
	//nolint:gosec // G304: the lock is this records repository's own file under its git common directory, named by a digest.
	lock, err := os.OpenFile(filepath.Join(dir, trackingLockName(remote)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("tracking lock %s: %w", remote, err)
	}
	if err := waitLock(ctx, lock); err != nil {
		return nil, errors.Join(fmt.Errorf("tracking lock %s: %w", remote, err), lock.Close())
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
		switch err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
		case err == nil:
			return nil
		case !errors.Is(err, syscall.EWOULDBLOCK):
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w for %s: %s", ErrLockHeld, lockWait, lock.Name())
		case <-poll.C:
		}
	}
}

func trackingLockName(remote string) string {
	sum := sha256.Sum256([]byte(trackingLockDomain + remote))
	return "tracking-" + hex.EncodeToString(sum[:]) + ".lock"
}
