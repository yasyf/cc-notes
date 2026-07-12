package cli

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestAwaitContentdReadyAndTimeout pins Q4's dial-poll: awaitContentd returns once
// the socket binds and otherwise times out with a crisp error naming the
// LaunchAgent — so the first AddMount never precedes contentd's bind (a cold
// launchctl bootstrap returns before the agent's socket exists).
func TestAwaitContentdReadyAndTimeout(t *testing.T) {
	oldAvail, oldTimeout, oldPoll := contentdAvailable, contentdReadyTimeout, contentdPollInterval
	defer func() {
		contentdAvailable, contentdReadyTimeout, contentdPollInterval = oldAvail, oldTimeout, oldPoll
	}()
	contentdReadyTimeout = 200 * time.Millisecond
	contentdPollInterval = 2 * time.Millisecond

	// Comes up after a few polls: awaitContentd blocks, then succeeds.
	var calls int
	contentdAvailable = func(string) bool { calls++; return calls >= 3 }
	if err := awaitContentd("/tmp/cc-notes-await.sock"); err != nil {
		t.Fatalf("awaitContentd should succeed once the socket binds: %v", err)
	}
	if calls < 3 {
		t.Fatalf("awaitContentd polled %d times, want at least 3 (it must retry, not decide on the first miss)", calls)
	}

	// Never comes up: a crisp timeout error naming the LaunchAgent.
	contentdAvailable = func(string) bool { return false }
	err := awaitContentd("/tmp/cc-notes-await.sock")
	if err == nil {
		t.Fatal("awaitContentd returned nil for a socket that never binds, want a timeout error")
	}
	if !strings.Contains(err.Error(), contentdLabel) {
		t.Errorf("timeout err = %v, want it to name the %s LaunchAgent", err, contentdLabel)
	}
}

// TestContentdStampStaleTriggersRecycle pins Q5's boot stamp: startContentd skips
// the reinstall when the serving contentd's stamp matches the installed binary
// (fresh), and recycles the LaunchAgent when it does not (stale/missing) — so a
// brew upgrade that leaves the old renderer serving is displaced instead of
// serving stale content indefinitely.
func TestContentdStampStaleTriggersRecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldAvail, oldInstall, oldTimeout := contentdAvailable, contentdInstall, contentdReadyTimeout
	defer func() {
		contentdAvailable, contentdInstall, contentdReadyTimeout = oldAvail, oldInstall, oldTimeout
	}()
	contentdAvailable = func(string) bool { return true } // socket always answers
	contentdReadyTimeout = time.Second
	var installs int
	contentdInstall = func() error { installs++; return nil } // observe the recycle, no real launchctl

	if err := os.MkdirAll(fusekitSpoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	// Fresh stamp: no recycle.
	if err := writeContentdStamp(); err != nil {
		t.Fatalf("writeContentdStamp: %v", err)
	}
	if err := startContentd(); err != nil {
		t.Fatalf("startContentd (fresh): %v", err)
	}
	if installs != 0 {
		t.Fatalf("fresh stamp recycled contentd (%d installs), want 0", installs)
	}

	// Stale stamp (a pre-upgrade binary's identity): recycle.
	if err := os.WriteFile(contentdStampPath(), []byte("v=0.0.0-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := startContentd(); err != nil {
		t.Fatalf("startContentd (stale): %v", err)
	}
	if installs != 1 {
		t.Fatalf("stale stamp did not recycle (%d installs), want 1", installs)
	}

	// Missing stamp: recycle.
	if err := os.Remove(contentdStampPath()); err != nil {
		t.Fatal(err)
	}
	if err := startContentd(); err != nil {
		t.Fatalf("startContentd (missing): %v", err)
	}
	if installs != 2 {
		t.Fatalf("missing stamp did not recycle (%d installs), want 2", installs)
	}
}

// TestContentdStampRoundTrips pins that a stamp written by writeContentdStamp is
// read back as fresh, and a mismatched stamp reads as stale.
func TestContentdStampRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(fusekitSpoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeContentdStamp(); err != nil {
		t.Fatalf("writeContentdStamp: %v", err)
	}
	fresh, err := contentdStampFresh()
	if err != nil {
		t.Fatalf("contentdStampFresh: %v", err)
	}
	if !fresh {
		t.Fatal("a stamp written by this binary read back as stale")
	}
	if err := os.WriteFile(contentdStampPath(), []byte("size=1 mtime=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := contentdStampFresh()
	if err != nil {
		t.Fatalf("contentdStampFresh (mismatch): %v", err)
	}
	if stale {
		t.Fatal("a mismatched stamp read back as fresh")
	}
}
