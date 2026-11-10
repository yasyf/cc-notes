package store

import (
	"sync"

	bolt "go.etcd.io/bbolt"
)

// queryActivations holds the bindings whose current generation a writer read
// before publishing, guarded by mu. The publication marks those generations
// active in the transaction it commits anyway, sparing the read a durable
// transaction of its own.
type queryActivations struct {
	mu       sync.Mutex
	bindings map[string]struct{}
}

func newQueryActivations() *queryActivations {
	return &queryActivations{bindings: make(map[string]struct{})}
}

func (a *queryActivations) request(binding string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bindings[binding] = struct{}{}
}

// apply marks each requested binding's current generation active. A request
// the transaction fails to commit is lost, and the next read activates instead.
func (a *queryActivations) apply(tx *bolt.Tx) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for binding := range a.bindings {
		bucket := queryGenerationBinding(tx, binding)
		if bucket == nil || bucket.Get(queryCurrentRoot) == nil {
			continue
		}
		if err := bucket.Put(queryActiveRoot, bucket.Get(queryCurrentRoot)); err != nil {
			return err
		}
	}
	clear(a.bindings)
	return nil
}
