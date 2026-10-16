package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const trackingLockPoll = 50 * time.Millisecond

// TrackingLock blocks until this process holds the records repository's lock
// on remote's tracking refs and returns the locked file; closing it releases
// the hold. A git command that writes those refs runs through
// RecordsGit.Holding(lock), so a child outliving this process keeps the lock.
// A cancelled ctx fails the wait.
func (s *Store) TrackingLock(ctx context.Context, remote string) (*os.File, error) {
	dir := filepath.Join(s.recordsCommonDir, filepath.Dir(LocalPushInclude))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("tracking lock %s: %w", remote, err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "tracking-"+url.PathEscape(remote)+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("tracking lock %s: %w", remote, err)
	}
	poll := time.NewTicker(trackingLockPoll)
	defer poll.Stop()
	for {
		switch err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
		case err == nil:
			return lock, nil
		case !errors.Is(err, syscall.EWOULDBLOCK):
			return nil, errors.Join(fmt.Errorf("tracking lock %s: %w", remote, err), lock.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(fmt.Errorf("tracking lock %s: %w", remote, ctx.Err()), lock.Close())
		case <-poll.C:
		}
	}
}
