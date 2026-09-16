package store

const (
	relevantCacheCap    = 2048
	relevantCacheSubdir = "cc-notes/relevant-v1"
)

// ReadRelevantCache returns the relevance result cached under name, or
// ok=false when none is. The caller validates the entry: the store only holds
// the bytes.
func (s *Store) ReadRelevantCache(name string) ([]byte, bool) {
	data, ok := s.relevant.read(name)
	if ok {
		s.relevant.touch(name)
	}
	return data, ok
}

// WriteRelevantCache stores a relevance result under name, best-effort,
// evicting the least-recently used entries past the cache's bound.
func (s *Store) WriteRelevantCache(name string, data []byte) {
	s.relevant.write(name, data)
}
