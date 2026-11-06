package sync

import (
	"context"
	"fmt"
	"io"
	stdsync "sync"
	"time"
)

var slowPhaseAfter = 10 * time.Second

type phaseKey struct{}

type phaseWriter struct {
	mu stdsync.Mutex
	w  io.Writer
}

func (p *phaseWriter) printf(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.w, format, args...)
}

// WithSlowPhases makes every sync phase under ctx that runs past 10 seconds
// write one line to w when it passes 10 seconds and one when it ends, so a
// long sync is never silent.
func WithSlowPhases(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, phaseKey{}, &phaseWriter{w: w})
}

// Phase starts the sync phase named name and returns the func that ends it.
func Phase(ctx context.Context, name string) (end func()) {
	p, ok := ctx.Value(phaseKey{}).(*phaseWriter)
	if !ok {
		return func() {}
	}
	start := time.Now()
	slow := time.AfterFunc(slowPhaseAfter, func() { p.printf("ccn sync: still %s after %s\n", name, slowPhaseAfter) })
	return func() {
		if !slow.Stop() {
			p.printf("ccn sync: %s took %s\n", name, time.Since(start).Round(time.Second))
		}
	}
}
