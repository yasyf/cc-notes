package store

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestRetainedLeasesPreserveOldViewsUntilFinalClose(t *testing.T) {
	f := newSharedFixture(t)
	r, err := Retain(t.Context(), f.thin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	first, err := r.Store(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	entry := r.current
	first.cache.write("held", []byte("old view"))
	if err := first.Pinned(map[string]model.SHA{}).Close(); err != nil {
		t.Fatal(err)
	}
	if entry.views != 1 {
		t.Fatalf("pinned borrower released parent lease: %d", entry.views)
	}
	gittest.Git(t, f.thin, "commit", "-q", "--allow-empty", "-m", "move HEAD")
	second, err := r.Store(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if second.cache == first.cache {
		t.Fatal("reopen reused old cache owner")
	}
	if got, ok := first.cache.read("held"); !ok || string(got) != "old view" {
		t.Fatalf("old view closed during reopen: %q, %v", got, ok)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if entry.views != 0 || !first.cache.closed {
		t.Fatalf("retired owner not closed exactly once: views=%d closed=%v", entry.views, first.cache.closed)
	}
	second.cache.write("current", []byte("current view"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got, ok := second.cache.read("current"); !ok || string(got) != "current view" {
		t.Fatalf("owner close interrupted outstanding operation: %q, %v", got, ok)
	}
	if _, err := r.Store(t.Context()); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("acquire after owner close = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if !second.cache.closed {
		t.Fatal("last operation did not close current cache owner")
	}
}

func TestScopeClosesOnCancellationWithoutInterruptingOpenedStream(t *testing.T) {
	s := initStore(t)
	s.relevant.write("stream", []byte("stream remains owned by its reader"))
	f, ok := s.relevant.open("stream")
	if !ok {
		t.Fatal("stream missing")
	}
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	ctx, scope := WithScope(ctx)
	if err := Own(ctx, s); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	start := make(chan struct{})
	readResult := make(chan error, 1)
	go func() {
		prefix := make([]byte, 1)
		if _, err := io.ReadFull(f, prefix); err != nil {
			readResult <- err
			close(ready)
			return
		}
		close(ready)
		<-start
		rest, err := io.ReadAll(f)
		if err == nil && string(append(prefix, rest...)) != "stream remains owned by its reader" {
			err = errors.New("stream bytes changed during scope close")
		}
		readResult <- err
	}()
	<-ready
	closeResult := make(chan error, 1)
	go func() { <-start; closeResult <- scope.Close() }()
	cancel()
	close(start)
	if err := <-readResult; err != nil {
		t.Fatal(err)
	}
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	if !s.cache.closed || !s.relevant.closed {
		t.Fatal("scope left cache owners open")
	}
	late := initStore(t)
	if err := Own(ctx, late); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("late ownership registration = %v", err)
	}
	if !late.cache.closed || !late.relevant.closed {
		t.Fatal("rejected registration leaked a cache owner")
	}
}

func TestScopeConcurrentRegistrationAndCloseReleaseEachLeaseOnce(t *testing.T) {
	ctx, scope := WithScope(t.Context())
	var released atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			<-start
			s := &Store{lease: &storeLease{release: func() error {
				released.Add(1)
				return nil
			}}}
			if err := Own(ctx, s); err != nil && !errors.Is(err, fs.ErrClosed) {
				t.Errorf("register: %v", err)
			}
		})
	}
	wg.Go(func() {
		<-start
		if err := scope.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	close(start)
	wg.Wait()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if got := released.Load(); got != 32 {
		t.Fatalf("released %d leases, want 32", got)
	}
}

func TestRetainedCloseRejectsCandidateOpenedDuringReopen(t *testing.T) {
	f := newSharedFixture(t)
	r, err := Retain(t.Context(), f.thin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	view, err := r.Store(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = view.Close() })
	view.cache.write("held", []byte("held"))
	gittest.Git(t, f.thin, "commit", "-q", "--allow-empty", "-m", "move HEAD")
	opened := make(chan *Store)
	publish := make(chan struct{})
	r.open = func(ctx context.Context, dir string) (*Store, error) {
		s, err := OpenContext(ctx, dir)
		if err != nil {
			return nil, err
		}
		opened <- s
		<-publish
		return s, nil
	}
	result := make(chan error, 1)
	go func() {
		s, err := r.Store(t.Context())
		if s != nil {
			_ = s.Close()
		}
		result <- err
	}()
	var candidate *Store
	select {
	case candidate = <-opened:
	case err := <-result:
		t.Fatalf("reopen failed before publication: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	close(publish)
	if err := <-result; !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("reopen published after close: %v", err)
	}
	if !candidate.cache.closed || !candidate.relevant.closed {
		t.Fatal("rejected candidate leaked cache owners")
	}
	if data, ok := view.cache.read("held"); !ok || string(data) != "held" {
		t.Fatalf("close interrupted prior lease: %q, %v", data, ok)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	if !view.cache.closed {
		t.Fatal("retired prior owner remained open")
	}
}
