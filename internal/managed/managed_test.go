package managed

import (
	"errors"
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
