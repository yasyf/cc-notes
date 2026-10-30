package store

import (
	"context"
	"errors"
	"io/fs"
	"sync"
)

type storeLease struct {
	once    sync.Once
	release func() error
	err     error
}

// Scope owns the stores opened or borrowed by one command invocation.
type Scope struct {
	mu     sync.Mutex
	stores []*Store
	closed bool
	once   sync.Once
	err    error
}

type scopeKey struct{}

// WithScope gives a command an explicit owner for all stores it acquires.
func WithScope(ctx context.Context) (context.Context, *Scope) {
	scope := &Scope{}
	return context.WithValue(ctx, scopeKey{}, scope), scope
}

// Own registers s with the command scope carried by ctx. Without a scope, the
// caller retains responsibility for closing s.
func Own(ctx context.Context, s *Store) error {
	scope, ok := ctx.Value(scopeKey{}).(*Scope)
	if !ok {
		return nil
	}
	scope.mu.Lock()
	if scope.closed {
		scope.mu.Unlock()
		return errors.Join(fs.ErrClosed, s.Close())
	}
	scope.stores = append(scope.stores, s)
	scope.mu.Unlock()
	return nil
}

// Close releases this scope's stores after its command operations finish,
// including leases borrowed from Retained.
func (s *Scope) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		stores := s.stores
		s.stores = nil
		s.closed = true
		s.mu.Unlock()
		for _, store := range stores {
			s.err = errors.Join(s.err, store.Close())
		}
	})
	return s.err
}

// Close releases this store's cache handles. Operation views release their
// lease; closing a pinned borrower leaves its parent view open.
func (s *Store) Close() error {
	if s.borrowed {
		return nil
	}
	if s.lease != nil {
		s.lease.once.Do(func() { s.lease.err = s.lease.release() })
		return s.lease.err
	}
	var err error
	if s.cache != nil {
		err = s.cache.close()
	}
	if s.relevant != nil {
		err = errors.Join(err, s.relevant.close())
	}
	return err
}
