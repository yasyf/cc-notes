package store

import (
	"bytes"
	"sync"

	bolt "go.etcd.io/bbolt"
)

// queryActivations holds, per binding, the current generation root a writer
// read before publishing, guarded by mu. The publication marks that generation
// active in the transaction it commits anyway, sparing the read a durable
// transaction of its own.
type queryActivations struct {
	mu       sync.Mutex
	bindings map[string][]byte
}

func newQueryActivations() *queryActivations {
	return &queryActivations{bindings: make(map[string][]byte)}
}

func (a *queryActivations) request(binding string, current []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bindings[binding] = bytes.Clone(current)
}

// apply marks each requested generation active while it is still the binding's
// current one. A generation another writer replaced since the read, or a request
// the transaction fails to commit, is left to the next read to activate.
func (a *queryActivations) apply(tx *bolt.Tx) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for binding, read := range a.bindings {
		bucket := queryGenerationBinding(tx, binding)
		if bucket == nil || read == nil || !bytes.Equal(bucket.Get(queryCurrentRoot), read) {
			continue
		}
		if err := bucket.Put(queryActiveRoot, read); err != nil {
			return err
		}
	}
	clear(a.bindings)
	return nil
}
