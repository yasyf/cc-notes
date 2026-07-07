package viz

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newStreamServer builds a viz Server over a minimal repo and returns it with
// its hub, for the SSE handler and hub tests.
func newStreamServer(t *testing.T) (*Server, *Hub) {
	t.Helper()
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	srv := NewServer(s, NewBuilder(s))
	return srv, srv.Hub()
}

// readFrame reads one SSE message off r: every line up to the blank-line
// terminator, joined, with the terminator dropped.
func readFrame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v (got %q)", err, b.String())
		}
		if line == "\n" {
			return b.String()
		}
		b.WriteString(line)
	}
}

// TestStreamFramingAndCancel drives GET /api/stream over a cancellable request:
// two publishes arrive as exact "event: refs" frames in order, and cancelling
// the request makes the handler return and drop its subscriber, after which
// Close does not panic.
func TestStreamFramingAndCancel(t *testing.T) {
	srv, hub := newStreamServer(t)
	exit := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.handleStream(w, r)
		close(exit)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}

	// The client received headers, which the handler flushes only after it
	// subscribes, so these publishes reach the subscriber.
	hub.Publish([]byte(`{"gen":1}`))
	hub.Publish([]byte(`{"gen":2}`))

	reader := bufio.NewReader(resp.Body)
	if got, want := readFrame(t, reader), "event: refs\ndata: {\"gen\":1}\n"; got != want {
		t.Fatalf("frame 1 = %q, want %q", got, want)
	}
	if got, want := readFrame(t, reader), "event: refs\ndata: {\"gen\":2}\n"; got != want {
		t.Fatalf("frame 2 = %q, want %q", got, want)
	}

	cancel()
	select {
	case <-exit:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return within 5s of request cancel")
	}
	if n := hub.subscriberCount(); n != 0 {
		t.Fatalf("subscriber count = %d after cancel, want 0", n)
	}
	hub.Close()
}

// TestStreamClosedByHub pins that closing the hub with a subscriber attached
// ends the stream cleanly: the handler returns and the client reads a clean EOF.
func TestStreamClosedByHub(t *testing.T) {
	srv, hub := newStreamServer(t)
	ts := httptest.NewServer(http.HandlerFunc(srv.handleStream))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The headers are flushed after Subscribe, so the hub has the subscriber.
	hub.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body after Close: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty (hub closed before any publish)", body)
	}
}

// TestHubDropOnFull fills a subscriber past its buffer without reading: the
// overflowing publish is dropped rather than blocking, and Close still closes
// the channel over the surviving buffered payloads.
func TestHubDropOnFull(t *testing.T) {
	hub := NewHub()
	ch, _ := hub.Subscribe()

	for range subBuffer + 1 {
		hub.Publish([]byte("x"))
	}
	if got := len(ch); got != subBuffer {
		t.Fatalf("buffered = %d, want %d (drop-on-full)", got, subBuffer)
	}

	hub.Close()
	drained := 0
	for range ch {
		drained++
	}
	if drained != subBuffer {
		t.Fatalf("drained %d, want %d then a closed channel", drained, subBuffer)
	}
}
