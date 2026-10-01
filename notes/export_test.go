package notes

import (
	"context"
	"encoding/json"
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
// assert on: the persisted stamp paths in capture order and the two flags
// that keep an entry off the warm path.
type RelevantCacheProbe struct {
	Stamps     []string
	Racy       bool
	Revalidate bool
}

// RelevantCacheProbeOf reads the cache entry RelevantCached keeps for target
// under filter and variant; ok is false when there is none.
func RelevantCacheProbeOf(c *Client, target string, filter RelevantFilter, variant string) (RelevantCacheProbe, bool, error) {
	p, err := c.relevantPath(context.Background(), target)
	if err != nil {
		return RelevantCacheProbe{}, false, err
	}
	data, ok := c.s.ReadRelevantCache(relevantCacheName(c.s.GitDir(), c.s.Git.Dir, p, filter, variant))
	if !ok {
		return RelevantCacheProbe{}, false, nil
	}
	var entry relevantCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return RelevantCacheProbe{}, false, err
	}
	probe := RelevantCacheProbe{Racy: entry.Racy, Revalidate: entry.Revalidate}
	for _, s := range entry.Stamps {
		probe.Stamps = append(probe.Stamps, s.Path)
	}
	return probe, true, nil
}
