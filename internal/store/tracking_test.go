package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTrackingLockFailsBehindAHungHolder(t *testing.T) {
	s := initStore(t)
	saved := lockWait
	lockWait = 100 * time.Millisecond
	t.Cleanup(func() { lockWait = saved })

	held, err := s.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("TrackingLock with no holder: %v", err)
	}
	start := time.Now()
	if _, err := s.TrackingLock(t.Context(), "origin"); !errors.Is(err, ErrLockHeld) || !strings.Contains(err.Error(), fmt.Sprintf("by pid %d (", os.Getpid())) {
		t.Fatalf("TrackingLock behind a holder = %v, want ErrLockHeld naming pid %d", err, os.Getpid())
	}
	if waited := time.Since(start); waited < lockWait {
		t.Fatalf("TrackingLock gave up after %s, before lockWait %s", waited, lockWait)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := s.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("TrackingLock after the holder released: %v", err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSecludeFailsBehindAHungHolder(t *testing.T) {
	s := initStore(t)
	saved := lockWait
	lockWait = 100 * time.Millisecond
	t.Cleanup(func() { lockWait = saved })

	path := filepath.Join(s.recordsCommonDir, LocalPushInclude+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if err := s.Seclude(t.Context(), []string{"refs/cc-notes/notes/x"}, nil); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Seclude behind a holder = %v, want ErrLockHeld", err)
	}
}
