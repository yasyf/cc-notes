package viz

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// getCommits decodes the /api/commits payload at the given query.
func getCommits(t *testing.T, base, query string) commitsResponse {
	t.Helper()
	code, body := getBody(t, base+"/api/commits"+query)
	if code != http.StatusOK {
		t.Fatalf("GET /api/commits%s status = %d (%s)", query, code, body)
	}
	var resp commitsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return resp
}

// shas is the sha sequence of a page, for exact-order assertions.
func shas(page []commitPage) []model.SHA {
	out := make([]model.SHA, len(page))
	for i, c := range page {
		out[i] = c.SHA
	}
	return out
}

// TestCommitsPaging walks a linear trunk newest-first and pages through it with
// the next_before cursor, pinning the exact sha sequence across pages.
func TestCommitsPaging(t *testing.T) {
	r := newGitRepo(t)
	c1 := r.commit("c1")
	c2 := r.commit("c2")
	c3 := r.commit("c3")
	c4 := r.commit("c4")
	c5 := r.commit("c5")
	ts, _, _ := newVizServer(t, r)

	full := getCommits(t, ts.URL, "")
	wantAll := []model.SHA{c5.sha, c4.sha, c3.sha, c2.sha, c1.sha}
	if got := shas(full.Commits); !equalSHAs(got, wantAll) {
		t.Fatalf("full page = %v, want %v", got, wantAll)
	}
	if full.NextBefore != nil {
		t.Errorf("full page next_before = %q, want null", *full.NextBefore)
	}
	if full.Truncated {
		t.Errorf("truncated = true, want false")
	}

	p1 := getCommits(t, ts.URL, "?limit=2")
	if got := shas(p1.Commits); !equalSHAs(got, []model.SHA{c5.sha, c4.sha}) {
		t.Fatalf("page 1 = %v, want [c5 c4]", got)
	}
	if p1.NextBefore == nil || *p1.NextBefore != string(c4.sha) {
		t.Fatalf("page 1 next_before = %v, want %s", p1.NextBefore, c4.sha)
	}

	p2 := getCommits(t, ts.URL, "?limit=2&before="+string(c4.sha))
	if got := shas(p2.Commits); !equalSHAs(got, []model.SHA{c3.sha, c2.sha}) {
		t.Fatalf("page 2 = %v, want [c3 c2]", got)
	}
	if p2.NextBefore == nil || *p2.NextBefore != string(c2.sha) {
		t.Fatalf("page 2 next_before = %v, want %s", p2.NextBefore, c2.sha)
	}

	p3 := getCommits(t, ts.URL, "?limit=2&before="+string(c2.sha))
	if got := shas(p3.Commits); !equalSHAs(got, []model.SHA{c1.sha}) {
		t.Fatalf("page 3 = %v, want [c1]", got)
	}
	if p3.NextBefore != nil {
		t.Errorf("page 3 next_before = %q, want null", *p3.NextBefore)
	}
}

func TestCommitsUnknownBefore(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	ts, _, _ := newVizServer(t, r)

	code, body := getBody(t, ts.URL+"/api/commits?before=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", code, body)
	}
}

// TestCommitsAttribution pins per-commit lane attribution, task resolution, and
// event landing over a squash-merged branch: a feature commit is claimed by its
// lane, the squash commit stays on the trunk and carries the resolved task id,
// and a linked commit surfaces its commit_linked event.
func TestCommitsAttribution(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	r.commit("c2")
	r.git("checkout", "-q", "-b", "feature")
	b1 := r.commit("b1")
	r.git("checkout", "-q", "main")

	s := r.openStore()
	taskID := r.doneTask(s, "ship feature", model.Branch("feature"))
	c3 := r.commitMsg("squash feature", "cc-task: "+taskID.Short())
	if _, err := s.Append(t.Context(), refs.For(model.KindTask, taskID), []model.Op{model.LinkCommit{SHA: c3.sha}}); err != nil {
		t.Fatalf("link commit: %v", err)
	}

	ts, _, _ := newVizServer(t, r)
	resp := getCommits(t, ts.URL, "")

	byName := map[model.SHA]commitPage{}
	for _, c := range resp.Commits {
		byName[c.SHA] = c
	}
	feature, ok := byName[b1.sha]
	if !ok {
		t.Fatalf("commit b1 %s absent from page %v", b1.sha, shas(resp.Commits))
	}
	if feature.Branch == nil || *feature.Branch != "feature" {
		t.Errorf("b1 branch = %v, want feature", feature.Branch)
	}

	squash, ok := byName[c3.sha]
	if !ok {
		t.Fatalf("commit c3 %s absent from page", c3.sha)
	}
	if squash.Branch == nil || *squash.Branch != "main" {
		t.Errorf("c3 branch = %v, want main", squash.Branch)
	}
	if len(squash.Tasks) != 1 || squash.Tasks[0] != string(taskID) {
		t.Errorf("c3 tasks = %v, want [%s]", squash.Tasks, taskID)
	}
	if !hasEventType(squash.Events, evCommitLinked) {
		t.Errorf("c3 events = %+v, want a commit_linked event", squash.Events)
	}
}

// TestCommitsRootTrailer pins FIX D: a cc-task trailer on the root (parentless)
// commit reaches its Tasks field. The trunk lane collects trailers over root..tip,
// which by range semantics excludes the root, so without folding in the root's own
// trailers the root commit is listed but its task is dropped.
func TestCommitsRootTrailer(t *testing.T) {
	r := newGitRepo(t)
	s := r.openStore()
	taskID := r.doneTask(s, "root task", model.Branch("main"))
	root := r.commitMsg("root commit", "cc-task: "+taskID.Short())
	r.commit("c2")

	ts, _, _ := newVizServer(t, r)
	resp := getCommits(t, ts.URL, "")

	byName := map[model.SHA]commitPage{}
	for _, c := range resp.Commits {
		byName[c.SHA] = c
	}
	rootPage, ok := byName[root.sha]
	if !ok {
		t.Fatalf("root commit %s absent from page %v", root.sha, shas(resp.Commits))
	}
	if len(rootPage.Tasks) != 1 || rootPage.Tasks[0] != string(taskID) {
		t.Errorf("root tasks = %v, want [%s]", rootPage.Tasks, taskID)
	}
}

// TestCommitsDeletedBranchAttribution pins that a merged-then-deleted branch's
// exclusive commits claim its mined lane in /api/commits — the second-parent
// commits that carried no branch before the DAG was mined now attribute to the
// reconstructed lane, while the merge commit stays on the trunk.
func TestCommitsDeletedBranchAttribution(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")
	r.git("checkout", "-q", "-b", "gone")
	g1 := r.commit("g1")
	g2 := r.commit("g2")
	r.git("checkout", "-q", "main")
	m := r.mergeNoFF(c2.time+1000, "gone", "Merge branch 'gone'")
	r.git("branch", "-D", "gone")

	ts, _, _ := newVizServer(t, r)
	resp := getCommits(t, ts.URL, "")

	byName := map[model.SHA]commitPage{}
	for _, c := range resp.Commits {
		byName[c.SHA] = c
	}
	for _, sha := range []model.SHA{g1.sha, g2.sha} {
		c, ok := byName[sha]
		if !ok {
			t.Fatalf("commit %s absent from page %v", sha, shas(resp.Commits))
		}
		if c.Branch == nil || *c.Branch != "gone" {
			t.Errorf("commit %s branch = %v, want gone", sha, c.Branch)
		}
	}
	merge, ok := byName[m.sha]
	if !ok {
		t.Fatalf("merge commit %s absent from page %v", m.sha, shas(resp.Commits))
	}
	if merge.Branch == nil || *merge.Branch != "main" {
		t.Errorf("merge commit branch = %v, want main", merge.Branch)
	}
}

func equalSHAs(a, b []model.SHA) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasEventType(events []Event, typ string) bool {
	for _, e := range events {
		if e.Type == typ {
			return true
		}
	}
	return false
}
