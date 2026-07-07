package viz

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/web"
)

// newVizServer opens a fresh store over r and serves it, so the server observes
// every entity and commit written before this call.
func newVizServer(t *testing.T, r *gitRepo) (*httptest.Server, *store.Store, *Builder) {
	t.Helper()
	s := r.openStore()
	b := NewBuilder(s)
	ts := httptest.NewServer(NewServer(s, b))
	t.Cleanup(ts.Close)
	return ts, s, b
}

// getBody issues a GET and returns the status code and raw body.
func getBody(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, body
}

func TestAPIRepo(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	r.commit("c2")
	r.git("checkout", "-q", "-b", "feature")
	r.commit("b1")
	r.git("checkout", "-q", "main")
	ts, _, _ := newVizServer(t, r)

	code, body := getBody(t, ts.URL+"/api/repo")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", code, body)
	}
	var info RepoInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if info.Trunk != "main" {
		t.Errorf("trunk = %q, want main", info.Trunk)
	}
	if info.Head != "main" {
		t.Errorf("head = %q, want main", info.Head)
	}
	if info.Truncated {
		t.Errorf("truncated = true, want false for the header-only endpoint")
	}
	wantRoot, err := filepath.EvalSymlinks(r.dir)
	if err != nil {
		t.Fatalf("eval %s: %v", r.dir, err)
	}
	gotRoot, err := filepath.EvalSymlinks(info.Root)
	if err != nil {
		t.Fatalf("eval %s: %v", info.Root, err)
	}
	if gotRoot != wantRoot {
		t.Errorf("root = %q, want %q", gotRoot, wantRoot)
	}
	if _, err := time.Parse(time.RFC3339, info.GeneratedAt); err != nil {
		t.Errorf("generated_at %q is not RFC3339: %v", info.GeneratedAt, err)
	}
}

// TestAPIGraphMatchesBuilder pins that the endpoint serializes exactly the
// Builder's Graph — the same cached value, byte for byte.
func TestAPIGraphMatchesBuilder(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")
	r.git("checkout", "-q", "-b", "feature")
	r.commit("b1")
	r.git("checkout", "-q", "main")
	r.mergeNoFF(c2.time+1000, "feature", "merge feature")
	ts, _, b := newVizServer(t, r)

	code, body := getBody(t, ts.URL+"/api/graph")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", code, body)
	}
	g, err := b.Graph(context.Background(), 0)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	want, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("graph body mismatch\n got: %s\nwant: %s", body, want)
	}
}

func TestAPIGraphBadSince(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	ts, _, _ := newVizServer(t, r)

	code, body := getBody(t, ts.URL+"/api/graph?since=abc")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", code, body)
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if !strings.Contains(errResp.Error, "since") {
		t.Errorf("error = %q, want it to name since", errResp.Error)
	}
}

// TestAPIEntityTaskWithCheckpoint drives the entity endpoint over a task whose
// op-log was compacted, so its trail carries a checkpoint entry followed by a
// live edit.
func TestAPIEntityTaskWithCheckpoint(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	ctx := t.Context()
	s := r.openStore()
	snap, err := s.Create(ctx, []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "Ship viz server", Type: model.TypeTask, Branch: "main"}})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	task := snap.(model.Task)
	ref := refs.Task(task.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.SetStatus{Status: model.StatusInProgress}}); err != nil {
		t.Fatalf("set in_progress: %v", err)
	}
	if _, err := s.Compact(ctx, ref); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if _, err := s.Append(ctx, ref, []model.Op{model.SetStatus{Status: model.StatusDone}}); err != nil {
		t.Fatalf("set done: %v", err)
	}

	ts, _, _ := newVizServer(t, r)
	code, body := getBody(t, ts.URL+"/api/entity/task/"+string(task.ID))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", code, body)
	}
	var resp entityResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if resp.Summary.Kind != entityTask || resp.Summary.ID != task.ID {
		t.Errorf("summary = %+v, want kind task id %s", resp.Summary, task.ID)
	}
	if resp.Summary.Status != string(model.StatusDone) {
		t.Errorf("summary status = %q, want done", resp.Summary.Status)
	}
	var checkpoint *trailEntry
	for i := range resp.Trail {
		if resp.Trail[i].Kind == "checkpoint" {
			checkpoint = &resp.Trail[i]
		}
	}
	if checkpoint == nil {
		t.Fatalf("trail %+v has no checkpoint entry", resp.Trail)
	}
	if checkpoint.Covers != 2 {
		t.Errorf("checkpoint covers = %d, want 2 (create + set in_progress)", checkpoint.Covers)
	}
	last := resp.Trail[len(resp.Trail)-1]
	if last.Kind != "edit" {
		t.Errorf("last entry kind = %q, want edit", last.Kind)
	}
}

func TestAPIEntityNotFound(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	ts, _, _ := newVizServer(t, r)

	code, body := getBody(t, ts.URL+"/api/entity/task/0000000")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", code, body)
	}
}

func TestAPIEntityBadKind(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	ts, _, _ := newVizServer(t, r)

	code, body := getBody(t, ts.URL+"/api/entity/widget/abc1234")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", code, body)
	}
}

// TestAPIStaticFallback proves the default build (web UI not embedded) serves
// the inline placeholder for a UI route while the JSON API stays live, and an
// unregistered /api path is a JSON 404 rather than the SPA page.
func TestAPIStaticFallback(t *testing.T) {
	if web.Embedded {
		t.Skip("web UI embedded; the inline placeholder is not served")
	}
	r := newGitRepo(t)
	r.commit("c1")
	ts, _, _ := newVizServer(t, r)

	code, body := getBody(t, ts.URL+"/")
	if code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200 (%s)", code, body)
	}
	if !strings.Contains(string(body), "built without the web UI") {
		t.Errorf("GET / body %q missing the placeholder text", body)
	}
	if code, _ := getBody(t, ts.URL+"/api/repo"); code != http.StatusOK {
		t.Errorf("GET /api/repo status = %d, want 200", code)
	}
	if code, _ := getBody(t, ts.URL+"/api/nope"); code != http.StatusNotFound {
		t.Errorf("GET /api/nope status = %d, want 404", code)
	}
}
