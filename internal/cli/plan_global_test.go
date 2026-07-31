package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

// TestPlanReachesGlobalVerbs pins the plan arms of the kind-agnostic commands.
// search merges the plan into its corpus, and show and compact each dispatch on
// the resolved kind through a switch that panics on an unregistered one.
func TestPlanReachesGlobalVerbs(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "plan", "add", "buffer the results channel",
		"--body", "## Approach\n\nbuffer the send before the collector returns", "--json"))

	hits := spJSON[[]searchDTO](t, spMust(t, dir, "search", "buffer the send", "--json"))
	if len(hits) != 1 || hits[0].Kind != "plan" || hits[0].Plan == nil || hits[0].Plan.ID != id {
		t.Fatalf("search = %+v, want the plan %s matched on its body", hits, id)
	}
	wantLean := "plan\t" + id[:7] + "\tdraft\tbuffer the results channel\n"
	if got := spMust(t, dir, "search", "buffer the send"); got != wantLean {
		t.Errorf("lean search = %q, want %q", got, wantLean)
	}

	shown := spJSON[planDTO](t, spMust(t, dir, "show", id, "--json"))
	if shown.ID != id || shown.Status != "draft" {
		t.Fatalf("show = %+v, want the draft plan %s", shown, id)
	}

	compacted := spJSON[planSummaryDTO](t, spMust(t, dir, "compact", id, "--json"))
	if compacted.ID != id || compacted.Status != "draft" {
		t.Errorf("compact = %+v, want the plan %s still draft", compacted, id)
	}
	if after := spJSON[planDTO](t, spMust(t, dir, "show", id, "--json")); after.Body != shown.Body {
		t.Errorf("compact changed the folded body: %q, want %q", after.Body, shown.Body)
	}
}

// TestPlanReachesRelevant pins the plan arms of relevant. The executing plan
// anchored at the target outranks the draft, and both carry an empty verdict
// column: the note fallback would render a plan permanently UNVERIFIED, since a
// plan holds no VerifiedAt.
func TestPlanReachesRelevant(t *testing.T) {
	dir := spInitRepo(t)
	path := filepath.Join(dir, "internal", "fold", "fold.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir fold: %v", err)
	}
	if err := os.WriteFile(path, []byte("package fold\n"), 0o600); err != nil {
		t.Fatalf("write fold.go: %v", err)
	}
	gittest.Git(t, dir, "add", "internal/fold/fold.go")
	gittest.Git(t, dir, "commit", "-q", "-m", "add fold")

	executing := spID(t, spMust(t, dir, "plan", "add", "chain the bundles first",
		"--body", "the fold drops ops", "--approved", "--path", "internal/fold/fold.go", "--json"))
	spMust(t, dir, "plan", "start", executing)
	draft := spID(t, spMust(t, dir, "plan", "add", "a second cut",
		"--body", "another approach", "--path", "internal/fold/fold.go", "--json"))

	dtos := spJSON[[]relevantDTO](t, spMust(t, dir, "relevant", "internal/fold/fold.go", "--json"))
	if len(dtos) != 2 {
		t.Fatalf("relevant --json = %+v, want both plans", dtos)
	}
	top := dtos[0]
	if top.Kind != "plan" || top.Plan == nil || top.Plan.ID != executing || top.Plan.Status != "executing" {
		t.Fatalf("relevant[0] = %+v, want the executing plan %s", top, executing)
	}
	if top.Note != nil || top.Doc != nil || top.Log != nil || top.Runbook != nil || top.Investigation != nil {
		t.Errorf("relevant plan entry populates a foreign entity field: %+v", top)
	}
	if top.Score != 150 || !slices.Equal(top.Reasons, []string{"plan-executing", "path"}) {
		t.Errorf("relevant score/reasons = %d/%v, want 150/[plan-executing path]", top.Score, top.Reasons)
	}
	if dtos[1].Plan == nil || dtos[1].Plan.ID != draft || dtos[1].Score != 100 {
		t.Errorf("relevant[1] = %+v, want the unboosted draft %s at 100", dtos[1], draft)
	}

	lean := spMust(t, dir, "relevant", "internal/fold/fold.go")
	wantTop := executing[:7] + "\texecuting\tchain the bundles first\tplan-executing,path\tplan show " + executing[:7]
	if first := strings.SplitN(lean, "\n", 2)[0]; first != wantTop {
		t.Errorf("relevant lean[0] = %q, want %q", first, wantTop)
	}
	// An empty verdict must not leak a trailing column into the lean line.
	if strings.Contains(lean, "UNVERIFIED") {
		t.Errorf("relevant lean renders a verdict for a plan:\n%s", lean)
	}
}
