package notes_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func BenchmarkRelevantCachedWarmHit(b *testing.B) {
	flat := []string{"alpha", "beta", "gamma"}
	nested := make([]string, 64)
	for i := range nested {
		nested[i] = fmt.Sprintf("p%02d/topic", i)
	}
	unrelated := make([]string, 1000)
	for i := range unrelated {
		unrelated[i] = fmt.Sprintf("unrelated-%04d", i)
	}
	layouts := []struct {
		name    string
		anchors []string
		extra   []string
	}{
		{name: "flat", anchors: flat},
		{name: "nested64", anchors: nested},
		{name: "unrelated1000", anchors: flat, extra: unrelated},
	}
	for _, n := range []int{8, 512, 4096} {
		for _, layout := range layouts {
			for _, detached := range []bool{false, true} {
				head := "attached"
				if detached {
					head = "detached"
				}
				b.Run(fmt.Sprintf("N=%d/refs=%s/head=%s", n, layout.name, head), func(b *testing.B) {
					benchWarmHit(b, n, layout.anchors, layout.extra, detached)
				})
			}
		}
	}
}

func benchWarmHit(b *testing.B, n int, anchors, extra []string, detached bool) {
	b.Cleanup(notes.SetRelevantRacyWindow(0))
	dir := gittest.InitRepo(b)
	b.Setenv("CC_NOTES_ACTOR", testActor)
	root := commitTB(b, dir, matrixTarget, "v1\n")
	createBranches(b, dir, root, append(slices.Clone(anchors), extra...))
	seedNotes(b, dir, n, anchors)
	if detached {
		gittest.Git(b, dir, "checkout", "-q", "--detach")
	}
	c, err := notes.Open(dir)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	var renders int
	render := func(entries []notes.RelevantEntry) ([]byte, error) {
		renders++
		return json.Marshal(entries)
	}
	filter := notes.RelevantFilter{}
	for range 2 {
		if _, err := c.RelevantCached(b.Context(), matrixTarget, filter, "json", render); err != nil {
			b.Fatalf("RelevantCached: %v", err)
		}
	}
	probe, ok, err := notes.RelevantCacheProbeOf(c, matrixTarget, filter, "json")
	if err != nil || !ok || probe.Racy || probe.Revalidate {
		b.Fatalf("warm entry = %+v, ok=%t, err=%v; want a persisted, settled entry", probe, ok, err)
	}
	warm := renders
	for b.Loop() {
		if _, err := c.RelevantCached(b.Context(), matrixTarget, filter, "json", render); err != nil {
			b.Fatalf("RelevantCached: %v", err)
		}
	}
	if renders != warm {
		b.Fatalf("warm loop re-rendered %d times", renders-warm)
	}
	b.ReportMetric(float64(len(probe.Stamps)), "stamps/op")
}

func commitTB(tb testing.TB, dir, path, content string) model.SHA {
	tb.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		tb.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		tb.Fatalf("write %s: %v", path, err)
	}
	gittest.Git(tb, dir, "add", "-A")
	gittest.Git(tb, dir, "commit", "-q", "-m", "commit "+path)
	return model.SHA(gittest.Git(tb, dir, "rev-parse", "HEAD"))
}
