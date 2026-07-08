package viz

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// hexOID returns a well-formed 64 lower-hex attachment oid seeded by c.
func hexOID(c byte) string { return strings.Repeat(string(c), 64) }

// blobGet issues a GET with the given headers and returns the response and its
// body; the caller inspects status and headers on resp.
func blobGet(t *testing.T, url string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp, body
}

// reference appends an add_attachment op referencing a fabricated oid — content
// presence is irrelevant to the referenced-set membership these tests exercise.
func reference(t *testing.T, s *store.Store, ref, name, oid string, size int64) {
	t.Helper()
	appendOps(t, s, ref, model.AddAttachment{Name: name, OID: oid, Size: size})
}

// attachContent hashes content into the store's LFS store and references it from
// ref under name, returning the resulting attachment.
func attachContent(t *testing.T, s *store.Store, ref, name string, content []byte) model.Attachment {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	att, _, err := s.AttachFile(t.Context(), path)
	if err != nil {
		t.Fatalf("AttachFile %s: %v", path, err)
	}
	appendOps(t, s, ref, model.AddAttachment{Name: att.Name, OID: att.OID, Size: att.Size})
	return att
}

// TestBlobServesReferencedContent covers the happy path: a referenced,
// locally-present attachment serves its bytes with the immutable caching
// headers, and a conditional re-request with the returned ETag gets a 304.
func TestBlobServesReferencedContent(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	ref := refs.Note(createNote(t, s, "note with png"))
	content := []byte("\x89PNG\r\n\x1a\nfake image payload for the viz blob test")
	att := attachContent(t, s, ref, "trace.png", content)

	ts, _, _ := newVizServer(t, r)
	resp, body := blobGet(t, ts.URL+"/api/blob/"+att.OID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, content) {
		t.Errorf("body = %q, want %q", body, content)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	wantETag := `"` + att.OID + `"`
	if got := resp.Header.Get("ETag"); got != wantETag {
		t.Errorf("ETag = %q, want %q", got, wantETag)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want the immutable directive", cc)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") || !strings.Contains(cd, "trace.png") {
		t.Errorf("Content-Disposition = %q, want inline with filename trace.png", cd)
	}
	if cl := resp.Header.Get("Content-Length"); cl == "" {
		t.Errorf("Content-Length missing; http.ServeContent should set it")
	}

	resp304, body304 := blobGet(t, ts.URL+"/api/blob/"+att.OID, map[string]string{"If-None-Match": wantETag})
	if resp304.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304 (%s)", resp304.StatusCode, body304)
	}
	if len(body304) != 0 {
		t.Errorf("304 body = %q, want empty", body304)
	}
}

func TestBlobMalformedOID(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	ts, _, _ := newVizServer(t, r)

	resp, body := blobGet(t, ts.URL+"/api/blob/not-a-valid-oid", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "invalid attachment oid") {
		t.Errorf("body = %q, want it to name the invalid oid", body)
	}
}

func TestBlobUnreferenced(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	// A referenced note attachment exists, but the requested oid is a different,
	// well-formed oid nothing references.
	reference(t, s, refs.Note(createNote(t, s, "unrelated")), "real.bin", hexOID('a'), 5)
	ts, _, _ := newVizServer(t, r)

	unref := hexOID('c')
	resp, body := blobGet(t, ts.URL+"/api/blob/"+unref, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "no entity references attachment "+unref) {
		t.Errorf("body = %q, want the unreferenced message naming %s", body, unref)
	}
}

// TestBlobReferencedButAbsent covers a referenced attachment whose content is
// not in the local LFS store: the 404 names the attachment, its owning entity,
// and the fetch hint.
func TestBlobReferencedButAbsent(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	id := createNote(t, s, "note missing content")
	oid := hexOID('d')
	reference(t, s, refs.Note(id), "absent.png", oid, 49152)
	ts, _, _ := newVizServer(t, r)

	resp, body := blobGet(t, ts.URL+"/api/blob/"+oid, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", resp.StatusCode, body)
	}
	msg := string(body)
	for _, want := range []string{"absent.png", "has not been fetched locally", "cc-notes sync", "note " + id.Short()} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing-content 404 = %q, want it to contain %q", msg, want)
		}
	}
}

// TestBlobDeletedLocalObject reaches the same fetch-hint 404 through content that
// really landed in the LFS store and no longer resolves there, matching a
// synced-away attachment.
func TestBlobDeletedLocalObject(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	att := attachContent(t, s, refs.Note(createNote(t, s, "deleted object")), "gone.txt", []byte("about to be deleted"))

	content, err := s.LFS(t.Context())
	if err != nil {
		t.Fatalf("LFS: %v", err)
	}
	if err := os.Remove(content.Path(att.OID)); err != nil {
		t.Fatalf("remove object: %v", err)
	}

	ts, _, _ := newVizServer(t, r)
	resp, body := blobGet(t, ts.URL+"/api/blob/"+att.OID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "cc-notes sync") {
		t.Errorf("body = %q, want the fetch hint", body)
	}
}

// TestBlobNameContentTypeRule pins that only a recorded attachment name can
// drive the served Content-Type and Content-Disposition: the default is the
// first recorded use, a ?name= matching a second recorded use is honored, and an
// unrecorded ?name= (here evil.html) falls back rather than forcing text/html.
// Every response carries the nosniff and sandbox hardening headers.
func TestBlobNameContentTypeRule(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	ref := refs.Note(createNote(t, s, "note with two-named png"))
	content := []byte("\x89PNG\r\n\x1a\nfake image payload for the name rule test")
	att := attachContent(t, s, ref, "aaa.png", content)
	// A second live use of the same object, recorded under a distinct name and
	// extension; folded attachments key by name, so both uses survive.
	reference(t, s, ref, "zzz.jpg", att.OID, att.Size)

	ts, _, _ := newVizServer(t, r)

	cases := []struct {
		name         string
		query        string
		wantCTPrefix string
		wantDispName string
	}{
		{"no param uses first recorded name", "", "image/png", "aaa.png"},
		{"param matching second recorded name is honored", "?name=zzz.jpg", "image/jpeg", "zzz.jpg"},
		{"unrecorded param falls back to first name", "?name=evil.html", "image/png", "aaa.png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := blobGet(t, ts.URL+"/api/blob/"+att.OID+tc.query, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", resp.StatusCode, body)
			}
			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, tc.wantCTPrefix) {
				t.Errorf("Content-Type = %q, want prefix %q", ct, tc.wantCTPrefix)
			}
			if strings.Contains(ct, "text/html") {
				t.Errorf("Content-Type = %q, must never be text/html for an unrecorded name", ct)
			}
			cd := resp.Header.Get("Content-Disposition")
			if !strings.HasPrefix(cd, "inline") || !strings.Contains(cd, tc.wantDispName) {
				t.Errorf("Content-Disposition = %q, want inline with filename %q", cd, tc.wantDispName)
			}
			if tc.query == "?name=evil.html" && strings.Contains(cd, "evil.html") {
				t.Errorf("Content-Disposition = %q, must not echo the unrecorded name", cd)
			}
			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := resp.Header.Get("Content-Security-Policy"); got != "sandbox" {
				t.Errorf("Content-Security-Policy = %q, want sandbox", got)
			}
		})
	}
}

// TestReferencedAttachmentRebuildsOnNewEntity pins that the memoized index
// rebuilds when a new attachment lands on a new entity tip, without any explicit
// invalidation: the entity-ref digest changes, so the next lookup sees it.
func TestReferencedAttachmentRebuildsOnNewEntity(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	b := NewBuilder(s)
	ctx := t.Context()

	first := hexOID('a')
	reference(t, s, refs.Note(createNote(t, s, "first")), "first.bin", first, 5)
	if _, ok, err := b.ReferencedAttachment(ctx, first); err != nil || !ok {
		t.Fatalf("ReferencedAttachment(first) = ok %v err %v, want true nil", ok, err)
	}

	second := hexOID('b')
	if _, ok, err := b.ReferencedAttachment(ctx, second); err != nil || ok {
		t.Fatalf("ReferencedAttachment(second) before it exists = ok %v err %v, want false nil", ok, err)
	}

	reference(t, s, refs.Note(createNote(t, s, "second")), "second.bin", second, 9)
	obj, ok, err := b.ReferencedAttachment(ctx, second)
	if err != nil {
		t.Fatalf("ReferencedAttachment(second): %v", err)
	}
	if !ok {
		t.Fatalf("index did not rebuild after a new entity tip; second oid unseen")
	}
	if obj.OID != second || obj.Size != 9 {
		t.Errorf("obj = %+v, want oid %s size 9", obj, second)
	}
}
