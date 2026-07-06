package viz

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	// subBuffer is each subscriber channel's depth. A publish that would exceed
	// it is dropped (see Publish): events only tell a client to refetch, and the
	// payload's monotonic gen lets the client notice the gap.
	subBuffer = 8
	// keepaliveInterval is the comment-ping period that keeps an idle SSE
	// connection open through intermediary timeouts.
	keepaliveInterval = 15 * time.Second
)

// Hub is the SSE fan-out registry: the Watcher publishes ref-change payloads and
// every attached /api/stream handler relays them to its client. It is the sole
// owner of the subscriber channels — it creates them in Subscribe and closes
// them in the cancel func or Close, all guarded by mu — so it is always the
// sender and the sole closer, and no receiver ever closes a channel.
type Hub struct {
	mu     sync.Mutex
	subs   map[int]chan []byte
	nextID int
	closed bool
}

// NewHub returns an empty Hub ready for subscribers.
func NewHub() *Hub {
	return &Hub{subs: make(map[int]chan []byte)}
}

// Subscribe registers a subscriber and returns its receive channel plus a cancel
// func that removes it and closes its channel exactly once. Calling cancel more
// than once, or after Close already reclaimed the subscriber, is a no-op: the
// map membership checked under mu is the guard against a double close. A
// subscribe against an already-closed hub hands back a closed channel so the
// caller's stream ends at once.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan []byte, subBuffer)
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	id := h.nextID
	h.nextID++
	h.subs[id] = ch
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
	return ch, cancel
}

// Publish delivers payload to every subscriber with a non-blocking send: a
// subscriber whose buffer is full drops this event rather than blocking the
// watcher or its peers. The drop is safe by design — each event only prompts a
// client refetch, and the gen field in the payload lets the client detect it
// missed one — so a slow reader never stalls the fan-out. The sends run under mu
// but cannot block (select default), so the lock is never held across I/O.
func (h *Hub) Publish(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- payload:
		default:
		}
	}
}

// Close closes every subscriber channel exactly once and marks the hub closed,
// so every attached handler unblocks and returns. The hub is the sole sender,
// so it is the sole closer; callers must stop every publisher (the Watcher)
// before calling Close so no send races the close. Idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}

// subscriberCount reports the number of attached subscribers, for tests.
func (h *Hub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// handleStream is the GET /api/stream Server-Sent Events endpoint: it subscribes
// to the hub, then relays each ref-change payload as an "event: refs" message,
// flushing after every write, and pings a ": keepalive" comment every
// keepaliveInterval. It returns when the client disconnects (r.Context().Done())
// or the hub closes the subscriber channel.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch, cancel := s.hub.Subscribe()
	defer cancel()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: refs\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
