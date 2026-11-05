package managed

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLockFailsBehindAHeldLock(t *testing.T) {
	previous := lockWait
	lockWait = 100 * time.Millisecond
	t.Cleanup(func() { lockWait = previous })
	dir := t.TempDir()
	held, err := TryLock(dir, true)
	if err != nil || held == nil {
		t.Fatalf("TryLock on a free lock=%v, %v", held, err)
	}
	t.Cleanup(func() { _ = held.Close() })
	for _, exclusive := range []bool{false, true} {
		if other, err := TryLock(dir, exclusive); err != nil || other != nil {
			t.Fatalf("TryLock(exclusive=%t) on a held lock=%v, %v; want nil, nil", exclusive, other, err)
		}
	}
	for _, exclusive := range []bool{false, true} {
		if file, err := Lock(t.Context(), dir, exclusive); !errors.Is(err, ErrBusy) {
			t.Fatalf("Lock(exclusive=%t) behind a holder=%v, %v; want ErrBusy", exclusive, file, err)
		}
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := Lock(t.Context(), dir, true)
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLockWaitEnvShortensTheWait(t *testing.T) {
	dir := t.TempDir()
	held, err := TryLock(dir, true)
	if err != nil || held == nil {
		t.Fatalf("TryLock on a free lock=%v, %v", held, err)
	}
	t.Cleanup(func() { _ = held.Close() })
	t.Setenv(LockWaitEnv, "50ms")
	started := time.Now()
	if file, err := Lock(t.Context(), dir, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("Lock behind a holder=%v, %v; want ErrBusy", file, err)
	}
	if elapsed := time.Since(started); elapsed >= lockWait/2 {
		t.Fatalf("Lock waited %s with %s=50ms", elapsed, LockWaitEnv)
	}
	if got, err := Wait(time.Millisecond); err != nil || got != time.Millisecond {
		t.Fatalf("Wait(1ms) with a longer env = %s, %v; want the shorter 1ms", got, err)
	}
	t.Setenv(LockWaitEnv, "soon")
	if file, err := Lock(t.Context(), dir, false); err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("Lock with an invalid wait=%v, %v; want a parse error", file, err)
	}
}

func TestBusyNamesTheHolder(t *testing.T) {
	previous := lockWait
	lockWait = 50 * time.Millisecond
	t.Cleanup(func() { lockWait = previous })
	dir := t.TempDir()
	writer, err := Lock(t.Context(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(t.Context(), dir, true); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), fmt.Sprintf("by pid %d (", os.Getpid())) {
		t.Fatalf("Lock behind a writer=%v; want ErrBusy naming pid %d", err, os.Getpid())
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := Lock(t.Context(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := Lock(t.Context(), dir, true); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "by readers") {
		t.Fatalf("Lock behind a reader=%v; want ErrBusy naming readers", err)
	}
	rebuild, err := LockRebuild(t.Context(), dir)
	if err != nil {
		t.Fatalf("LockRebuild beside a reader: %v", err)
	}
	t.Cleanup(func() { _ = rebuild.Close() })
	if other, err := TryLockRebuild(dir); err != nil || other != nil {
		t.Fatalf("TryLockRebuild behind a rebuild=%v, %v; want nil, nil", other, err)
	}
}
