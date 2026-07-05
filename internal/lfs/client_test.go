package lfs_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/lfs"
)

func basicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBatchRequestGolden pins the exact batch request JSON against the LFS
// spec's layout, for both operations, and pins that zero objects mean zero
// requests.
func TestBatchRequestGolden(t *testing.T) {
	f, srv := newFakeLFS(t)
	c := &lfs.Client{Endpoint: srv.URL + "/lfs"}
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	ctx := t.Context()

	content := []byte("golden payload")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	f.put(oid, content)
	objs := []lfs.Object{{OID: oid, Size: int64(len(content))}}

	uploaded, err := c.Upload(ctx, store, objs)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if uploaded != 0 {
		t.Fatalf("uploaded = %d, want 0 (server already has it)", uploaded)
	}
	want := fmt.Sprintf(`{"operation":"upload","transfers":["basic"],"objects":[{"oid":"%s","size":%d}],"hash_algo":"sha256"}`, oid, len(content))
	if got := f.lastBatchBody(); got != want {
		t.Fatalf("upload batch body:\n got %s\nwant %s", got, want)
	}

	if _, err := c.Download(ctx, store, objs); err != nil {
		t.Fatalf("Download: %v", err)
	}
	want = fmt.Sprintf(`{"operation":"download","transfers":["basic"],"objects":[{"oid":"%s","size":%d}],"hash_algo":"sha256"}`, oid, len(content))
	if got := f.lastBatchBody(); got != want {
		t.Fatalf("download batch body:\n got %s\nwant %s", got, want)
	}

	before := f.batchCount()
	if _, err := c.Upload(ctx, store, nil); err != nil {
		t.Fatalf("empty Upload: %v", err)
	}
	if _, err := c.Download(ctx, store, nil); err != nil {
		t.Fatalf("empty Download: %v", err)
	}
	if f.batchCount() != before {
		t.Fatalf("zero objects made %d batch requests, want 0", f.batchCount()-before)
	}
}

// TestDownloadMissingObject: per the spec a missing object on download is
// HTTP 200 with a per-object 404, surfaced as *ObjectError naming the oid.
func TestDownloadMissingObject(t *testing.T) {
	_, srv := newFakeLFS(t)
	c := &lfs.Client{Endpoint: srv.URL + "/lfs"}
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	oid := strings.Repeat("ab", 32)

	downloaded, err := c.Download(t.Context(), store, []lfs.Object{{OID: oid, Size: 123}})
	if downloaded != 0 {
		t.Fatalf("downloaded = %d, want 0", downloaded)
	}
	var objErr *lfs.ObjectError
	if !errors.As(err, &objErr) {
		t.Fatalf("err = %v, want *ObjectError", err)
	}
	if objErr.OID != oid || objErr.Code != 404 {
		t.Fatalf("ObjectError = %+v, want oid %s code 404", objErr, oid)
	}
}

// TestUploadDownloadRoundTrip is the full e2e: 1MB into store A, upload,
// download into store B, byte-identical; re-upload is a no-op; the verify
// action fired.
func TestUploadDownloadRoundTrip(t *testing.T) {
	f, srv := newFakeLFS(t)
	c := &lfs.Client{Endpoint: srv.URL + "/lfs"}
	storeA := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	storeB := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	ctx := t.Context()

	content := make([]byte, 1<<20)
	for i := range content {
		content[i] = byte(i*i + i>>8)
	}
	src := writeFile(t, "big.bin", content)

	oid, size, err := storeA.PutFile(src)
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	sum := sha256.Sum256(content)
	if oid != hex.EncodeToString(sum[:]) || size != int64(len(content)) {
		t.Fatalf("PutFile = %s/%d, want %s/%d", oid, size, hex.EncodeToString(sum[:]), len(content))
	}
	objs := []lfs.Object{{OID: oid, Size: size}}

	uploaded, err := c.Upload(ctx, storeA, objs)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if uploaded != 1 {
		t.Fatalf("uploaded = %d, want 1", uploaded)
	}
	if !f.isVerified(oid) {
		t.Fatal("verify action never fired")
	}

	again, err := c.Upload(ctx, storeA, objs)
	if err != nil {
		t.Fatalf("re-Upload: %v", err)
	}
	if again != 0 {
		t.Fatalf("re-upload = %d, want 0 (idempotent skip)", again)
	}

	downloaded, err := c.Download(ctx, storeB, objs)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if downloaded != 1 {
		t.Fatalf("downloaded = %d, want 1", downloaded)
	}
	got, err := os.ReadFile(storeB.Path(oid))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("downloaded bytes differ from uploaded bytes")
	}
}

// TestDownloadCorruptionRejected proves a wrong-bytes download wraps
// ErrCorrupt and never lands in the store.
func TestDownloadCorruptionRejected(t *testing.T) {
	f, srv := newFakeLFS(t)
	c := &lfs.Client{Endpoint: srv.URL + "/lfs"}
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	oid := strings.Repeat("cd", 32)
	f.put(oid, []byte("not the content that hashes to oid"))

	downloaded, err := c.Download(t.Context(), store, []lfs.Object{{OID: oid, Size: 34}})
	if !errors.Is(err, lfs.ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if downloaded != 0 {
		t.Fatalf("downloaded = %d, want 0", downloaded)
	}
	if store.Has(oid) {
		t.Fatal("corrupt object landed in store")
	}
	entries, err := os.ReadDir(filepath.Join(store.Dir, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("tmp not cleaned: %d entries", len(entries))
	}
}

// TestNonLFSRemoteFailsFast: an HTML 404 — a remote with no LFS server —
// wraps ErrUnsupported.
func TestNonLFSRemoteFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := &lfs.Client{Endpoint: srv.URL}
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}

	_, err := c.Upload(t.Context(), store, []lfs.Object{{OID: strings.Repeat("aa", 32), Size: 1}})
	if !errors.Is(err, lfs.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestBatchClassification pins how a non-success batch response is
// classified: only 404/501 or a 2xx whose body is not LFS JSON reads as
// ErrUnsupported (the remote has no LFS endpoint). A transient non-2xx
// (502/503) is a transport error carrying its status — never ErrUnsupported,
// which would wrongly prescribe removing attachments during an outage.
func TestBatchClassification(t *testing.T) {
	obj := []lfs.Object{{OID: strings.Repeat("aa", 32), Size: 1}}
	cases := []struct {
		name            string
		status          int
		contentType     string
		body            string
		wantUnsupported bool
		wantStatus      string // substring the transport error must carry
	}{
		{name: "502 html outage", status: http.StatusBadGateway, contentType: "text/html", body: "<html>bad gateway</html>", wantStatus: "502"},
		{name: "503 html outage", status: http.StatusServiceUnavailable, contentType: "text/html", body: "<html>down</html>", wantStatus: "503"},
		{name: "500 lfs body outage", status: http.StatusInternalServerError, contentType: lfs.MediaType, body: `{"message":"boom"}`, wantStatus: "500"},
		{name: "404 no lfs endpoint", status: http.StatusNotFound, contentType: "text/plain", body: "not found", wantUnsupported: true},
		{name: "501 not implemented", status: http.StatusNotImplemented, contentType: "text/plain", body: "nope", wantUnsupported: true},
		{name: "200 non-lfs body", status: http.StatusOK, contentType: "text/html", body: "<html>hi</html>", wantUnsupported: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			c := &lfs.Client{Endpoint: srv.URL}
			store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}

			_, err := c.Download(t.Context(), store, obj)
			if err == nil {
				t.Fatalf("Download succeeded, want error")
			}
			if tc.wantUnsupported {
				if !errors.Is(err, lfs.ErrUnsupported) {
					t.Fatalf("err = %v, want ErrUnsupported", err)
				}
				return
			}
			if errors.Is(err, lfs.ErrUnsupported) {
				t.Fatalf("err = %v, want a transport error, not ErrUnsupported", err)
			}
			if !strings.Contains(err.Error(), tc.wantStatus) {
				t.Fatalf("err = %v, want status %s in message", err, tc.wantStatus)
			}
		})
	}
}

// TestCredentialAuthFlow drives the full https auth path against real git
// credential machinery: anonymous 401 → fill → Basic retry → approve, the
// credential cached in-process for the next batch, and batch auth never
// leaking to content requests (the fake server rejects it there).
func TestCredentialAuthFlow(t *testing.T) {
	f, srv := newFakeLFS(t)
	f.requireAuth = basicAuth("alice", "s3cret")
	g := initRepo(t)
	logPath := filepath.Join(t.TempDir(), "verbs.log")
	mustGit(t, g.Dir, "config", "credential.helper",
		fmt.Sprintf(`!f() { echo "$1" >>"%s"; if [ "$1" = get ]; then echo username=alice; echo password=s3cret; fi; }; f`, logPath))
	ctx := t.Context()

	c, err := lfs.NewClient(ctx, g, lfs.Endpoint{Href: srv.URL + "/lfs"}, "upload")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	content := []byte("authenticated payload")
	oid, size, err := store.PutFile(writeFile(t, "a.bin", content))
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	objs := []lfs.Object{{OID: oid, Size: size}}

	uploaded, err := c.Upload(ctx, store, objs)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if uploaded != 1 {
		t.Fatalf("uploaded = %d, want 1", uploaded)
	}

	// The second batch reuses the cached credential: no second fill.
	if _, err := c.Download(ctx, store, objs); err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read verb log: %v", err)
	}
	if got, want := strings.Fields(string(data)), "get store"; strings.Join(got, " ") != want {
		t.Fatalf("helper verbs = %q, want %q (fill once, approve once)", got, want)
	}
}

// TestCredentialAuthRejected: a credential the server refuses is rejected
// back to the helpers (erase verb) and the 401 surfaces.
func TestCredentialAuthRejected(t *testing.T) {
	f, srv := newFakeLFS(t)
	f.requireAuth = basicAuth("alice", "s3cret")
	g := initRepo(t)
	logPath := filepath.Join(t.TempDir(), "verbs.log")
	mustGit(t, g.Dir, "config", "credential.helper",
		fmt.Sprintf(`!f() { echo "$1" >>"%s"; if [ "$1" = get ]; then echo username=alice; echo password=wrong; fi; }; f`, logPath))
	ctx := t.Context()

	c, err := lfs.NewClient(ctx, g, lfs.Endpoint{Href: srv.URL + "/lfs"}, "upload")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}

	_, err = c.Upload(ctx, store, []lfs.Object{{OID: strings.Repeat("ef", 32), Size: 1}})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want status 401", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read verb log: %v", err)
	}
	if got, want := strings.Fields(string(data)), "get erase"; strings.Join(got, " ") != want {
		t.Fatalf("helper verbs = %q, want %q (fill once, reject once)", got, want)
	}
}

// TestCredentialForbidden: a credential the server authenticates but forbids
// (403 on the authenticated retry) is kept, not erased — a reject would purge
// a working login over an authorization failure and force a needless
// re-prompt. The 403 surfaces and no store/approve verb fires.
func TestCredentialForbidden(t *testing.T) {
	g := initRepo(t)
	logPath := filepath.Join(t.TempDir(), "verbs.log")
	mustGit(t, g.Dir, "config", "credential.helper",
		fmt.Sprintf(`!f() { echo "$1" >>"%s"; if [ "$1" = get ]; then echo username=alice; echo password=s3cret; fi; }; f`, logPath))
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("LFS-Authenticate", `Basic realm="Git LFS"`)
			w.Header().Set("Content-Type", lfs.MediaType)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", lfs.MediaType)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	c, err := lfs.NewClient(ctx, g, lfs.Endpoint{Href: srv.URL + "/lfs"}, "upload")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}

	_, err = c.Upload(ctx, store, []lfs.Object{{OID: strings.Repeat("ef", 32), Size: 1}})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want status 403", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read verb log: %v", err)
	}
	if got, want := strings.Fields(string(data)), "get"; strings.Join(got, " ") != want {
		t.Fatalf("helper verbs = %q, want %q (fill once, no reject/approve)", got, want)
	}
}

// stubSSH puts a fake ssh on PATH that records its argv and prints the given
// stdout, returning the argv capture path.
func stubSSH(t *testing.T, stdout string) (argvPath string) {
	t.Helper()
	bin := t.TempDir()
	argvPath = filepath.Join(bin, "argv.txt")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >\"%s\"\nprintf '%s'\n", argvPath, stdout)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvPath
}

// TestSSHGrantReplacesEndpoint: the git-lfs-authenticate grant's href
// authoritatively replaces the derived endpoint, and its header rides batch
// requests.
func TestSSHGrantReplacesEndpoint(t *testing.T) {
	f, srv := newFakeLFS(t)
	f.requireAuth = "RemoteAuth tok"
	g := initRepo(t)
	ctx := t.Context()

	grant := fmt.Sprintf(`{"href":"%s/lfs","header":{"Authorization":"RemoteAuth tok"},"expires_in":86400}`, srv.URL)
	argvPath := stubSSH(t, grant)

	ep := lfs.Endpoint{
		Href:        "https://derived.example/foo/bar.git/info/lfs",
		SSHUserHost: "git@git-server.com",
		SSHPort:     "2222",
		SSHPath:     "foo/bar.git",
	}
	c, err := lfs.NewClient(ctx, g, ep, "upload")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Endpoint != srv.URL+"/lfs" {
		t.Fatalf("Endpoint = %q, want grant href %q to replace the derived endpoint", c.Endpoint, srv.URL+"/lfs")
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(argv)), "-p 2222 -- git@git-server.com git-lfs-authenticate foo/bar.git upload"; got != want {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}

	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	content := []byte("over ssh grant")
	oid, size, err := store.PutFile(writeFile(t, "s.bin", content))
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	uploaded, err := c.Upload(ctx, store, []lfs.Object{{OID: oid, Size: size}})
	if err != nil {
		t.Fatalf("Upload with grant header: %v", err)
	}
	if uploaded != 1 {
		t.Fatalf("uploaded = %d, want 1", uploaded)
	}
}

func TestSSHGrantWithoutPort(t *testing.T) {
	g := initRepo(t)
	argvPath := stubSSH(t, `{"href":"https://lfs.example/foo/bar"}`)

	ep := lfs.Endpoint{
		Href:        "https://git-server.com/foo/bar.git/info/lfs",
		SSHUserHost: "git@git-server.com",
		SSHPath:     "foo/bar.git",
	}
	c, err := lfs.NewClient(t.Context(), g, ep, "download")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Endpoint != "https://lfs.example/foo/bar" {
		t.Fatalf("Endpoint = %q, want grant href", c.Endpoint)
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(argv)), "-- git@git-server.com git-lfs-authenticate foo/bar.git download"; got != want {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

func TestSSHGrantMissingHref(t *testing.T) {
	g := initRepo(t)
	stubSSH(t, `{"header":{"Authorization":"RemoteAuth tok"}}`)

	ep := lfs.Endpoint{SSHUserHost: "git@git-server.com", SSHPath: "foo/bar.git"}
	if _, err := lfs.NewClient(t.Context(), g, ep, "upload"); err == nil || !strings.Contains(err.Error(), "no href") {
		t.Fatalf("err = %v, want grant-has-no-href error", err)
	}
}
