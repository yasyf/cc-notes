package store

const (
	relevantCacheCap    = 2048
	relevantCacheSubdir = "cc-notes/relevant-v4"
)

// OpenRelevantCache returns a relevance stream whose bytes are verified as
// they are read. The caller validates semantic headers and closes the reader.
func (s *Store) OpenRelevantCache(name string) (*CacheReader, bool) {
	f, ok := s.relevant.open(name)
	if !ok {
		return nil, false
	}
	s.relevant.touch(name)
	return f, true
}

// WriteRelevantCache stores a relevance result under name, best-effort,
// evicting the least-recently used entries past the cache's bound.
func (s *Store) WriteRelevantCache(name string, data []byte) {
	s.relevant.write(name, data)
}

// EnsureCaches creates the fold and relevance cache directories when they are
// missing, best-effort like the cache writes, so a later write inside them
// moves no directory a relevance capture fingerprints.
func (s *Store) EnsureCaches() {
	s.cache.ensure()
	s.relevant.ensure()
}
