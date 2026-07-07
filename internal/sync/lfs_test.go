// LFS sync tests: two real clones and a bare file:// remote exchange
// attachment content through an httptest LFS server wired via the lfs.url
// config override, exactly how git-lfs itself consumes it.
package sync_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

// fakeLFS is an httptest LFS server speaking batch + basic content PUT/GET +
// verify, with induced-failure toggles and a request counter for the
// zero-requests assertion.
type fakeLFS struct {
	mu          stdsync.Mutex
	objects     map[string][]byte
	verified    map[string]bool
	requests    int
	failBatch   int    // when non-zero, batch answers this status
	failGet     int    // when non-zero, content GET answers this status
	requireAuth string // exact batch Authorization demanded when set
}

func (f *fakeLFS) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakeLFS) setFailBatch(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failBatch = status
}

func (f *fakeLFS) setFailGet(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failGet = status
}

func (f *fakeLFS) remove(oid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, oid)
}

func (f *fakeLFS) isVerified(oid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verified[oid]
}

// newFakeLFS starts the server and returns it with the endpoint URL to set
// as lfs.url.
func newFakeLFS(t *testing.T) (*fakeLFS, string) {
	t.Helper()
	f := &fakeLFS{objects: map[string][]byte{}, verified: map[string]bool{}}
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	mux.HandleFunc("POST /lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		if f.requireAuth != "" && r.Header.Get("Authorization") != f.requireAuth {
			w.Header().Set("LFS-Authenticate", `Basic realm="Git LFS"`)
			w.Header().Set("Content-Type", lfs.MediaType)
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Credentials needed"})
			return
		}
		var req struct {
			Operation string       `json:"operation"`
			Objects   []lfs.Object `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", lfs.MediaType)
		if f.failBatch != 0 {
			w.WriteHeader(f.failBatch)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "induced batch failure"})
			return
		}
		var out []map[string]any
		for _, obj := range req.Objects {
			res := map[string]any{"oid": obj.OID, "size": obj.Size}
			_, have := f.objects[obj.OID]
			switch req.Operation {
			case "upload":
				if !have {
					res["actions"] = map[string]any{
						"upload": map[string]any{"href": srv.URL + "/data/" + obj.OID},
						"verify": map[string]any{"href": srv.URL + "/verify"},
					}
				}
			case "download":
				if have {
					res["actions"] = map[string]any{
						"download": map[string]any{"href": srv.URL + "/data/" + obj.OID},
					}
				} else {
					res["error"] = map[string]any{"code": 404, "message": "Object does not exist"}
				}
			}
			out = append(out, res)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"transfer": "basic", "objects": out, "hash_algo": "sha256"})
	})

	mux.HandleFunc("PUT /data/{oid}", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.objects[r.PathValue("oid")] = data
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /data/{oid}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		failGet := f.failGet
		data, ok := f.objects[r.PathValue("oid")]
		f.mu.Unlock()
		if failGet != 0 {
			http.Error(w, "induced content failure", failGet)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})

	mux.HandleFunc("POST /verify", func(w http.ResponseWriter, r *http.Request) {
		var obj lfs.Object
		if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if data, ok := f.objects[obj.OID]; !ok || int64(len(data)) != obj.Size {
			http.Error(w, "verify failed", http.StatusNotFound)
			return
		}
		f.verified[obj.OID] = true
		w.WriteHeader(http.StatusOK)
	})

	return f, srv.URL + "/lfs"
}

// attachFile ingests content into s's LFS store and appends the
// add_attachment op to ref.
func attachFile(t *testing.T, s *store.Store, ref, name string, content []byte) model.Attachment {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	att, _, err := s.AttachFile(t.Context(), path)
	if err != nil {
		t.Fatalf("AttachFile(%s): %v", path, err)
	}
	appendOps(t, s, ref, model.AddAttachment(att))
	return att
}

// readObject returns the local LFS store's bytes for oid.
func readObject(t *testing.T, s *store.Store, oid string) []byte {
	t.Helper()
	content, err := s.LFS(t.Context())
	if err != nil {
		t.Fatalf("LFS: %v", err)
	}
	data, err := os.ReadFile(content.Path(oid))
	if err != nil {
		t.Fatalf("read object %s: %v", oid, err)
	}
	return data
}

func hasObject(t *testing.T, s *store.Store, oid string) bool {
	t.Helper()
	content, err := s.LFS(t.Context())
	if err != nil {
		t.Fatalf("LFS: %v", err)
	}
	return content.Has(oid)
}

// TestSyncAttachmentRoundTrip is the end-to-end path: attach in A, sync A
// (upload before push), sync a fresh clone B (download after convergence),
// and the content is byte-identical.
func TestSyncAttachmentRoundTrip(t *testing.T) {
	bare := initBare(t)
	f, endpoint := newFakeLFS(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	mustGit(t, a.Git.Dir, "config", "lfs.url", endpoint)
	mustGit(t, b.Git.Dir, "config", "lfs.url", endpoint)

	content := make([]byte, 1<<20)
	for i := range content {
		content[i] = byte(i*31 + 7)
	}
	note := createNote(t, a, "with attachment")
	noteRef := refs.Note(note.ID)
	att := attachFile(t, a, noteRef, "trace.bin", content)

	if got, want := sync(t, a), (ccsync.Report{Pushed: 1, Uploaded: 1, Rounds: 1}); got != want {
		t.Fatalf("A sync report = %+v, want %+v", got, want)
	}
	if !f.isVerified(att.OID) {
		t.Errorf("server never verified %s after upload", att.OID)
	}

	if got, want := sync(t, b), (ccsync.Report{Created: 1, Reconciled: 1, Downloaded: 1, Rounds: 1}); got != want {
		t.Fatalf("B sync report = %+v, want %+v", got, want)
	}
	if got := readObject(t, b, att.OID); !bytes.Equal(got, content) {
		t.Fatalf("B content differs: got %d bytes, want %d byte-identical", len(got), len(content))
	}
	loaded, err := b.Load(t.Context(), noteRef)
	if err != nil {
		t.Fatalf("B Load(%s): %v", noteRef, err)
	}
	if got, want := loaded.(model.Note).Attachments, []model.Attachment{att}; !reflect.DeepEqual(got, want) {
		t.Errorf("B folded attachments = %+v, want %+v", got, want)
	}

	// A further sync uploads nothing: the batch reply says the server
	// already has the object. The one reconcile is A's first fetch of its
	// own pushed ref into tracking, a no-op fold.
	if got, want := sync(t, a), (ccsync.Report{Reconciled: 1, Rounds: 1}); got != want {
		t.Errorf("A quiescent report = %+v, want %+v", got, want)
	}
}

// TestSyncAttachmentExtraHeaderAuth drives the CI shape end-to-end: each
// clone's local http.<url>.extraheader carries Basic auth for the batch
// endpoint with no credential helper, so clone A's sync uploads and clone B's
// sync downloads byte-identical content.
func TestSyncAttachmentExtraHeaderAuth(t *testing.T) {
	bare := initBare(t)
	f, endpoint := newFakeLFS(t)
	authValue := "basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:tok"))
	f.requireAuth = authValue
	base := strings.TrimSuffix(endpoint, "/lfs")
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	for _, s := range []*store.Store{a, b} {
		mustGit(t, s.Git.Dir, "config", "lfs.url", endpoint)
		mustGit(t, s.Git.Dir, "config", "http."+base+"/.extraheader", "AUTHORIZATION: "+authValue)
	}

	content := make([]byte, 1<<20)
	for i := range content {
		content[i] = byte(i*17 + 3)
	}
	note := createNote(t, a, "with authed attachment")
	noteRef := refs.Note(note.ID)
	att := attachFile(t, a, noteRef, "authed.bin", content)

	if got, want := sync(t, a), (ccsync.Report{Pushed: 1, Uploaded: 1, Rounds: 1}); got != want {
		t.Fatalf("A sync report = %+v, want %+v", got, want)
	}
	if !f.isVerified(att.OID) {
		t.Errorf("server never verified %s after upload", att.OID)
	}

	if got, want := sync(t, b), (ccsync.Report{Created: 1, Reconciled: 1, Downloaded: 1, Rounds: 1}); got != want {
		t.Fatalf("B sync report = %+v, want %+v", got, want)
	}
	if got := readObject(t, b, att.OID); !bytes.Equal(got, content) {
		t.Fatalf("B content differs: got %d bytes, want %d byte-identical", len(got), len(content))
	}
}

// TestSyncUploadFailureBlocksPush pins the objects-before-refs invariant: a
// failing LFS server leaves the remote's ref namespace untouched, and the
// next sync against a healed server publishes both content and refs.
func TestSyncUploadFailureBlocksPush(t *testing.T) {
	bare := initBare(t)
	f, endpoint := newFakeLFS(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	mustGit(t, a.Git.Dir, "config", "lfs.url", endpoint)

	note := createNote(t, a, "blocked")
	att := attachFile(t, a, refs.Note(note.ID), "blocked.bin", []byte("payload"))

	f.setFailBatch(http.StatusServiceUnavailable)
	report, err := ccsync.Sync(t.Context(), a.Git.Dir, "origin", false)
	if err == nil {
		t.Fatal("sync with failing LFS server succeeded, want upload error")
	}
	if !strings.Contains(err.Error(), "induced batch failure") {
		t.Errorf("error %q does not carry the server's message", err)
	}
	if report.Pushed != 0 || report.Uploaded != 0 {
		t.Errorf("failed-upload report = %+v, want nothing pushed or uploaded", report)
	}
	if tips := ccRefs(t, bare); len(tips) != 0 {
		t.Fatalf("remote refs after blocked push = %v, want none", tips)
	}

	f.setFailBatch(0)
	if got, want := sync(t, a), (ccsync.Report{Pushed: 1, Uploaded: 1, Rounds: 1}); got != want {
		t.Fatalf("healed sync report = %+v, want %+v", got, want)
	}
	if got := mustGit(t, bare, "rev-parse", refs.Note(note.ID)); got == "" {
		t.Errorf("remote missing %s after healed sync", refs.Note(note.ID))
	}
	if !f.isVerified(att.OID) {
		t.Errorf("server never verified %s after the healed upload", att.OID)
	}
}

// TestSyncZeroAttachmentsZeroLFSRequests pins that a repository referencing
// no attachments never touches the LFS endpoint: no discovery, no batch, no
// transfers.
func TestSyncZeroAttachmentsZeroLFSRequests(t *testing.T) {
	bare := initBare(t)
	f, endpoint := newFakeLFS(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	mustGit(t, a.Git.Dir, "config", "lfs.url", endpoint)
	mustGit(t, b.Git.Dir, "config", "lfs.url", endpoint)

	createNote(t, a, "plain note")
	createTask(t, a, "plain task", "main")
	sync(t, a)
	sync(t, b)
	sync(t, a)

	if got := f.requestCount(); got != 0 {
		t.Fatalf("LFS server saw %d requests, want 0", got)
	}
}

// TestSyncInterruptedDownloadHealsNextSync pins self-healing: a download
// that dies mid-sync leaves no partial object, and the next sync's full
// referenced scan fetches it with no flag.
func TestSyncInterruptedDownloadHealsNextSync(t *testing.T) {
	bare := initBare(t)
	f, endpoint := newFakeLFS(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	mustGit(t, a.Git.Dir, "config", "lfs.url", endpoint)
	mustGit(t, b.Git.Dir, "config", "lfs.url", endpoint)

	content := []byte("heals on the next run")
	note := createNote(t, a, "interrupted")
	att := attachFile(t, a, refs.Note(note.ID), "heal.bin", content)
	sync(t, a)

	f.setFailGet(http.StatusServiceUnavailable)
	report, err := ccsync.Sync(t.Context(), b.Git.Dir, "origin", false)
	if err == nil {
		t.Fatal("sync with failing content GET succeeded, want download error")
	}
	if want := (ccsync.Report{Created: 1, Reconciled: 1, Rounds: 1}); report != want {
		t.Fatalf("interrupted report = %+v, want %+v (ref landed, content did not)", report, want)
	}
	if hasObject(t, b, att.OID) {
		t.Fatal("partial download landed in the store")
	}

	f.setFailGet(0)
	if got, want := sync(t, b), (ccsync.Report{Downloaded: 1, Rounds: 1}); got != want {
		t.Fatalf("healing sync report = %+v, want %+v", got, want)
	}
	if got := readObject(t, b, att.OID); !bytes.Equal(got, content) {
		t.Fatalf("healed content = %q, want %q", got, content)
	}
}

// TestSyncRemoveLastAttachmentUnbricksLFSlessRemote pins the live-set scan's
// un-brick: an attachment referenced against a remote with no LFS endpoint
// fails the sync loudly with per-entity remediation, and removing the
// attachment empties the transfer set so the same remote syncs clean.
func TestSyncRemoveLastAttachmentUnbricksLFSlessRemote(t *testing.T) {
	bare := initBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")

	note := createNote(t, a, "bricked")
	noteRef := refs.Note(note.ID)
	att := attachFile(t, a, noteRef, "brick.bin", []byte("no endpoint will take this"))

	_, err := ccsync.Sync(t.Context(), a.Git.Dir, "origin", false)
	if !errors.Is(err, lfs.ErrUnsupported) {
		t.Fatalf("sync against LFS-less remote: err = %v, want lfs.ErrUnsupported", err)
	}
	for _, fragment := range []string{
		att.OID,
		note.ID.Short(),
		"`cc-notes note edit " + note.ID.Short() + " --rm-attachment \"brick.bin\"`",
		"cc-notes sync",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q missing %q", err, fragment)
		}
	}
	if tips := ccRefs(t, bare); len(tips) != 0 {
		t.Fatalf("remote refs after blocked push = %v, want none", tips)
	}

	appendOps(t, a, noteRef, model.RemoveAttachment{Name: att.Name})
	if got, want := sync(t, a), (ccsync.Report{Pushed: 1, Rounds: 1}); got != want {
		t.Fatalf("unbricked sync report = %+v, want %+v", got, want)
	}
	if got := mustGit(t, bare, "rev-parse", noteRef); got == "" {
		t.Errorf("remote missing %s after unbrick", noteRef)
	}
}

// TestSyncDownloadMissingObjectNamesEntity pins the pushed-but-download-
// failed contract: the run's own refs publish (the report says so), the
// error is non-nil, and it names the entity, the oid, the remediation, and
// the plain-git-push hole that strands content.
func TestSyncDownloadMissingObjectNamesEntity(t *testing.T) {
	bare := initBare(t)
	f, endpoint := newFakeLFS(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	mustGit(t, a.Git.Dir, "config", "lfs.url", endpoint)
	mustGit(t, b.Git.Dir, "config", "lfs.url", endpoint)

	note := createNote(t, a, "stranded content")
	att := attachFile(t, a, refs.Note(note.ID), "gone.bin", []byte("uploaded then lost"))
	sync(t, a)
	f.remove(att.OID)

	own := createNote(t, b, "b's own note")
	report, err := ccsync.Sync(t.Context(), b.Git.Dir, "origin", false)
	if err == nil {
		t.Fatal("sync with server-lost object succeeded, want download error")
	}
	if want := (ccsync.Report{Created: 1, Pushed: 1, Reconciled: 1, Rounds: 1}); report != want {
		t.Fatalf("report = %+v, want %+v (pushes still reported alongside the error)", report, want)
	}
	if got := mustGit(t, bare, "rev-parse", refs.Note(own.ID)); got == "" {
		t.Errorf("B's own ref never published despite the download failure")
	}
	for _, fragment := range []string{
		att.OID,
		note.ID.Short(),
		"--rm-attachment \"gone.bin\"",
		"git push",
		"cc-notes sync",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q missing %q", err, fragment)
		}
	}
}
