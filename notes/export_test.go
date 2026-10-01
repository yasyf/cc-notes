package notes

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"time"
)

// SetRelevantRacyWindow replaces the racy window every relevance capture
// applies and returns the call that restores the previous one.
func SetRelevantRacyWindow(d time.Duration) (restore func()) {
	previous := relevantRacyWindow
	relevantRacyWindow = d
	return func() { relevantRacyWindow = previous }
}

// RelevantCacheProbe is the shape of a relevance cache entry a test can
// assert on: the persisted stamp paths in capture order, the drift-file
// count, the two flags that keep an entry off the warm path, the byte length
// of the entry and of each section (header line with its newline, raw
// output, trailing deps), and the deps cardinalities.
type RelevantCacheProbe struct {
	Stamps      []string
	Files       int
	Racy        bool
	Revalidate  bool
	EntryBytes  int
	HeaderBytes int
	OutputBytes int
	DepsBytes   int
	DepBranches int
	DepCommits  int
	DepConfig   int
}

// RelevantCacheProbeOf reads the cache entry RelevantCached keeps for target
// under filter and variant; ok is false when there is none.
func RelevantCacheProbeOf(c *Client, target string, filter RelevantFilter, variant string) (RelevantCacheProbe, bool, error) {
	_, data, ok, err := relevantCacheBytes(c, target, filter, variant)
	if err != nil || !ok {
		return RelevantCacheProbe{}, false, err
	}
	entry, ok := parseRelevantCacheEntry(data)
	if !ok {
		return RelevantCacheProbe{}, false, fmt.Errorf("undecodable relevance cache entry %q", data)
	}
	header := bytes.IndexByte(data, '\n') + 1
	probe := RelevantCacheProbe{
		Files:       len(entry.Files),
		Racy:        entry.Racy,
		Revalidate:  entry.Revalidate,
		EntryBytes:  len(data),
		HeaderBytes: header,
		OutputBytes: len(entry.Output),
		DepsBytes:   len(data) - header - len(entry.Output),
		DepBranches: len(entry.Deps.Branches),
		DepCommits:  len(entry.Deps.Commits),
		DepConfig:   len(entry.Deps.Config),
	}
	for _, s := range entry.Stamps {
		probe.Stamps = append(probe.Stamps, s.Path)
	}
	return probe, true, nil
}

// CorruptRelevantCacheDeps atomically rewrites the entry RelevantCached keeps
// for target under filter and variant with its deps section replaced by
// garbage bytes that are not JSON, header and output intact.
func CorruptRelevantCacheDeps(c *Client, target string, filter RelevantFilter, variant string, garbage int) error {
	name, data, ok, err := relevantCacheBytes(c, target, filter, variant)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no relevance cache entry for %s", target)
	}
	entry, ok := parseRelevantCacheEntry(data)
	if !ok {
		return fmt.Errorf("undecodable relevance cache entry %q", data)
	}
	header := bytes.IndexByte(data, '\n') + 1
	c.s.WriteRelevantCache(name, slices.Concat(data[:header+len(entry.Output)], bytes.Repeat([]byte{'!'}, garbage)))
	return nil
}

func relevantCacheBytes(c *Client, target string, filter RelevantFilter, variant string) (name string, data []byte, ok bool, err error) {
	p, err := c.relevantPath(context.Background(), target)
	if err != nil {
		return "", nil, false, err
	}
	name = relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, variant)
	f, ok := c.s.OpenRelevantCache(name)
	if !ok {
		return name, nil, false, nil
	}
	defer func() { _ = f.Close() }()
	data, err = io.ReadAll(f)
	if err != nil {
		return name, nil, false, err
	}
	return name, data, true, nil
}

func parseRelevantCacheEntry(data []byte) (relevantCacheEntry, bool) {
	r := bufio.NewReader(bytes.NewReader(data))
	h, n, ok := readRelevantCacheHeader(r)
	if !ok || !h.readOutput(r, int64(len(data)-n)) || !h.readDeps(r) {
		return relevantCacheEntry{}, false
	}
	return h.relevantCacheEntry, true
}
