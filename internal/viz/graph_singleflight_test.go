package viz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bufLogger is a Logger that records every line and republishes it on lines, so
// a test waits for a specific line instead of sleeping. The mutex is what makes
// String safe to call while a build goroutine is still logging.
type bufLogger struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	lines chan string
}

func newBufLogger() *bufLogger { return &bufLogger{lines: make(chan string, 16)} }

func (l *bufLogger) Printf(format string, v ...any) {
	line := fmt.Sprintf(format, v...)
	l.mu.Lock()
	l.buf.WriteString(line)
	l.buf.WriteByte('\n')
	l.mu.Unlock()
	select {
	case l.lines <- line:
	default:
	}
}

func (l *bufLogger) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// awaitLine returns the first logged line containing want, failing the test if
// none arrives within 5s.
func (l *bufLogger) awaitLine(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line := <-l.lines:
			if strings.Contains(line, want) {
				return line
			}
		case <-deadline:
			t.Fatalf("no log line containing %q within 5s; logged:\n%s", want, l.String())
		}
	}
}

// newHookedBuilder opens a Builder over a two-lane repository with hook
// installed as the build seam, so a test decides when a build proceeds.
func newHookedBuilder(t *testing.T, hook func(ctx context.Context)) *Builder {
	t.Helper()
	r := newGitRepo(t)
	r.commit("c1")
	r.git("checkout", "-q", "-b", "feature")
	r.commit("c2")
	r.git("checkout", "-q", "main")
	b := NewBuilder(r.openStore())
	b.buildHook = hook
	return b
}

// TestGraphDedupsConcurrentBuilds pins that four concurrent Graph calls over one
// digest run exactly one build and every caller gets that build's result. Before
// the singleflight, each page reload stacked another full walk on the same
// repository.
func TestGraphDedupsConcurrentBuilds(t *testing.T) {
	var builds atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	b := newHookedBuilder(t, func(context.Context) {
		builds.Add(1)
		entered <- struct{}{}
		<-release
	})

	const callers = 4
	graphs := make([]*Graph, callers)
	errs := make([]error, callers)
	var arrived, done sync.WaitGroup
	arrived.Add(callers)
	done.Add(callers)
	for i := range callers {
		go func() {
			defer done.Done()
			arrived.Done()
			graphs[i], errs[i] = b.Graph(t.Context(), 0)
		}()
	}
	arrived.Wait()
	<-entered
	close(release)
	done.Wait()

	if got := builds.Load(); got != 1 {
		t.Fatalf("builds = %d, want 1 (the singleflight must collapse concurrent callers)", got)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if graphs[i] != graphs[0] {
			t.Fatalf("caller %d got %p, caller 0 got %p; want the one shared build's graph", i, graphs[i], graphs[0])
		}
	}
}

// TestGraphWaiterCancelKeepsSharedBuildAlive pins that a client navigating away
// takes down its own request only: the shared build runs to completion for the
// waiters still attached, and no second build starts.
func TestGraphWaiterCancelKeepsSharedBuildAlive(t *testing.T) {
	var builds atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	b := newHookedBuilder(t, func(context.Context) {
		builds.Add(1)
		entered <- struct{}{}
		<-release
	})

	ctx, cancel := context.WithCancel(t.Context())
	leaving := make(chan error, 1)
	go func() {
		_, err := b.Graph(ctx, 0)
		leaving <- err
	}()
	// The hook running proves the build goroutine started, so the caller is past
	// its digest and waiting on the result: the cancel below can only land in the
	// wait.
	<-entered

	type result struct {
		g   *Graph
		err error
	}
	staying := make(chan result, 1)
	go func() {
		g, err := b.Graph(t.Context(), 0)
		staying <- result{g, err}
	}()

	cancel()
	select {
	case err := <-leaving:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled waiter did not return within 5s of its context cancel")
	}

	close(release)
	got := <-staying
	if got.err != nil {
		t.Fatalf("attached waiter: %v", got.err)
	}
	if lane := laneByName(t, got.g, "main"); lane.Tip == nil {
		t.Fatal("attached waiter got a graph whose trunk lane has no tip")
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("builds = %d, want 1 (a cancelled waiter must not restart the build)", n)
	}
}

// TestGraphBuildTimeout pins the deadline on one build and the exact message the
// 504 body carries. The build is cut off by its context, so the error it reports
// is whatever the killed operation said — only the context tells the truth.
func TestGraphBuildTimeout(t *testing.T) {
	b := newHookedBuilder(t, func(ctx context.Context) { <-ctx.Done() })
	b.BuildTimeout = 50 * time.Millisecond

	_, err := b.Graph(t.Context(), 0)
	var timeout buildTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("err = %v (%T), want buildTimeoutError", err, err)
	}
	if got, want := err.Error(), "graph build exceeded 50ms; repo may be too large"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

// TestGraphSlowBuildLogs pins the slow-build narration: a line while the build is
// still running and a completion line carrying its cost, so a build that used to
// hang silently is visible in the operator's terminal.
func TestGraphSlowBuildLogs(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	b := newHookedBuilder(t, func(context.Context) {
		entered <- struct{}{}
		<-release
	})
	logger := newBufLogger()
	b.Log = logger
	b.slowThreshold = time.Millisecond

	type result struct {
		g   *Graph
		err error
	}
	built := make(chan result, 1)
	go func() {
		g, err := b.Graph(t.Context(), 0)
		built <- result{g, err}
	}()

	<-entered
	logger.awaitLine(t, "graph build still running after 1ms")
	close(release)

	got := <-built
	if got.err != nil {
		t.Fatalf("Graph: %v", got.err)
	}
	line := logger.awaitLine(t, "graph build took ")
	counts := fmt.Sprintf("%d lanes, %d events", len(got.g.Lanes), len(got.g.Events))
	if !strings.Contains(line, counts) {
		t.Fatalf("completion line %q, want it to carry %q", line, counts)
	}
	took := strings.TrimSuffix(strings.TrimPrefix(line, "graph build took "), ": "+counts)
	if _, err := time.ParseDuration(took); err != nil {
		t.Fatalf("completion line %q carries no parseable duration (%q): %v", line, took, err)
	}
}
