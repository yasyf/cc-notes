package notes_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	prefixes := make([]string, 64)
	for i := range nested {
		prefixes[i] = fmt.Sprintf("p%02d", i)
		nested[i] = prefixes[i] + "/topic"
	}
	unrelated := make([]string, 1000)
	for i := range unrelated {
		unrelated[i] = fmt.Sprintf("unrelated-%04d", i)
	}
	layouts := []struct {
		name   string
		layout warmLayout
	}{
		{name: "flat", layout: cyclingLayout(flat, nil)},
		{name: "nested64", layout: cyclingLayout(nested, nil)},
		{name: "unrelated1000", layout: cyclingLayout(flat, unrelated)},
		{name: "diverse", layout: diverseLayout(nil)},
		{name: "diverse-nested64", layout: diverseLayout(prefixes)},
	}
	const relevant = 8
	for _, n := range []int{8, 512, 4096} {
		for _, layout := range layouts {
			for _, detached := range []bool{false, true} {
				head := "attached"
				if detached {
					head = "detached"
				}
				b.Run(fmt.Sprintf("N=%d/relevant=%d/refs=%s/head=%s", n, relevant, layout.name, head), func(b *testing.B) {
					benchWarmHit(b, n, relevant, layout.layout, detached)
				})
			}
		}
		if n > relevant {
			b.Run(fmt.Sprintf("N=%d/relevant=%d/refs=flat/head=attached", n, n), func(b *testing.B) {
				benchWarmHit(b, n, n, cyclingLayout(flat, nil), false)
			})
		}
	}
}

func benchWarmHit(b *testing.B, n, relevant int, layout warmLayout, detached bool) {
	b.Cleanup(notes.SetRelevantRacyWindow(0))
	dir := gittest.InitRepo(b)
	b.Setenv("CC_NOTES_ACTOR", testActor)
	root := commitTB(b, dir, matrixTarget, "v1\n")
	layout(b, dir, root, n, relevant)
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
	pathProbes := gitPathProbes(b)
	warm := renders
	var out []byte
	for b.Loop() {
		if out, err = c.RelevantCached(b.Context(), matrixTarget, filter, "json", render); err != nil {
			b.Fatalf("RelevantCached: %v", err)
		}
	}
	if renders != warm {
		b.Fatalf("warm loop re-rendered %d times", renders-warm)
	}
	b.ReportMetric(float64(len(probe.Stamps)), "stamps/op")
	b.ReportMetric(float64(probe.Files), "files/op")
	b.ReportMetric(float64(probe.HeaderBytes), "headerbytes/op")
	b.ReportMetric(float64(probe.HeaderBytes+probe.OutputBytes), "readbytes/op")
	b.ReportMetric(float64(len(out)), "outbytes/op")
	b.ReportMetric(float64(probe.DepsBytes), "depsbytes")
	b.ReportMetric(float64(probe.DepBranches+probe.DepCommits), "deps")
	b.ReportMetric(float64(probe.EntryBytes), "entrybytes")
	b.ReportMetric(float64(pathProbes), "pathprobes/op")
}

func gitPathProbes(tb testing.TB) int {
	tb.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		tb.Fatalf("LookPath git: %v", err)
	}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	i := slices.IndexFunc(dirs, func(dir string) bool { return filepath.Join(dir, "git") == git })
	if i < 0 {
		tb.Fatalf("git %s is outside PATH %q", git, dirs)
	}
	return i + 1
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
