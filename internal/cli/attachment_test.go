// Attachment CLI tests: attach at add/append time, read back via
// attachment get/path, remove via --rm-attachment, and move content through
// sync against an httptest LFS server wired via the lfs.url config override.
package cli_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
)

// writeAttachable writes a file named name under a fresh temp dir and
// returns its path plus the sha256 oid the store must derive.
func writeAttachable(t *testing.T, name string, content []byte) (path, oid string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	sum := sha256.Sum256(content)
	return path, hex.EncodeToString(sum[:])
}

func TestNoteAttachGetPathShow(t *testing.T) {
	dir := initRepo(t)
	content := []byte("attachment payload bytes")
	path, oid := writeAttachable(t, "report.txt", content)

	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "With file", "--attach", path, "--json"))
	want := []attachmentJSON{{Name: "report.txt", OID: oid, Size: int64(len(content)), Present: true}}
	if len(added.Attachments) != 1 || added.Attachments[0] != want[0] {
		t.Fatalf("attachments = %+v, want %+v", added.Attachments, want)
	}

	if got := mustRun(t, dir, "attachment", "get", added.ID, "report.txt"); got != string(content) {
		t.Fatalf("attachment get = %q, want %q", got, content)
	}

	out := filepath.Join(t.TempDir(), "copy.txt")
	mustRun(t, dir, "attachment", "get", added.ID, "report.txt", "-o", out)
	if data, err := os.ReadFile(out); err != nil || string(data) != string(content) {
		t.Fatalf("attachment get -o wrote %q (%v), want %q", data, err, content)
	}

	objPath := strings.TrimSpace(mustRun(t, dir, "attachment", "path", added.ID, "report.txt"))
	if !filepath.IsAbs(objPath) || !strings.Contains(objPath, filepath.Join("lfs", "objects", oid[:2], oid[2:4], oid)) {
		t.Fatalf("attachment path = %q, want absolute standard lfs layout for %s", objPath, oid)
	}
	if data, err := os.ReadFile(objPath); err != nil || string(data) != string(content) {
		t.Fatalf("object at path = %q (%v), want the attached bytes", data, err)
	}

	shown := mustRun(t, dir, "note", "show", added.ID)
	if want := fmt.Sprintf("attachment: report.txt (%d bytes, oid %s)\n", len(content), oid[:7]); !strings.Contains(shown, want) {
		t.Fatalf("note show = %q, want attachment line %q", shown, want)
	}
}

func TestAttachmentMissingLocally(t *testing.T) {
	dir := initRepo(t)
	path, oid := writeAttachable(t, "gone.bin", []byte("soon removed"))
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Missing", "--attach", path, "--json"))
	objPath := strings.TrimSpace(mustRun(t, dir, "attachment", "path", added.ID, "gone.bin"))
	if err := os.Remove(objPath); err != nil {
		t.Fatalf("remove object: %v", err)
	}

	shown := mustJSON[noteJSON](t, mustRun(t, dir, "note", "show", added.ID, "--json"))
	if len(shown.Attachments) != 1 || shown.Attachments[0].Present {
		t.Fatalf("attachments = %+v, want present=false after object removal", shown.Attachments)
	}
	lean := mustRun(t, dir, "note", "show", added.ID)
	if want := fmt.Sprintf("attachment: gone.bin (12 bytes, oid %s, missing locally — run `cc-notes sync`)\n", oid[:7]); !strings.Contains(lean, want) {
		t.Fatalf("note show = %q, want missing marker %q", lean, want)
	}

	if _, _, err := runCLI(t, dir, "attachment", "get", added.ID, "gone.bin"); err == nil || !strings.Contains(err.Error(), "cc-notes sync") {
		t.Fatalf("attachment get on missing content = %v, want the sync remediation", err)
	}
	if _, _, err := runCLI(t, dir, "attachment", "path", added.ID, "gone.bin"); err == nil || !strings.Contains(err.Error(), "cc-notes sync") {
		t.Fatalf("attachment path on missing content = %v, want the sync remediation", err)
	}
}

func TestAttachmentLookupErrors(t *testing.T) {
	dir := initRepo(t)
	path, _ := writeAttachable(t, "a.txt", []byte("x"))
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Holder", "--attach", path, "--json"))

	_, _, err := runCLI(t, dir, "attachment", "get", added.ID, "nope.txt")
	if cli.ExitCode(err) != 3 || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("get unknown name = %v (exit %d), want not-found listing a.txt", err, cli.ExitCode(err))
	}
	if _, _, err := runCLI(t, dir, "attachment", "get", "ffffffff", "a.txt"); cli.ExitCode(err) != 3 {
		t.Fatalf("get unknown id = %v (exit %d), want 3", err, cli.ExitCode(err))
	}
}

func TestLogAppendAttachReplace(t *testing.T) {
	dir := initRepo(t)
	first, firstOID := writeAttachable(t, "trace.log", []byte("first capture"))
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "Rollout", "--attach", first, "--json"))
	if len(added.Attachments) != 1 || added.Attachments[0].OID != firstOID {
		t.Fatalf("log add attachments = %+v, want oid %s", added.Attachments, firstOID)
	}

	second, secondOID := writeAttachable(t, "trace.log", []byte("second, longer capture"))
	_, _, err := runCLI(t, dir, "log", "append", added.ID, "--attach", second)
	if cli.ExitCode(err) != 2 || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("colliding append = %v (exit %d), want usage error naming --replace", err, cli.ExitCode(err))
	}
	shown := mustJSON[logJSON](t, mustRun(t, dir, "log", "show", added.ID, "--json"))
	if len(shown.Attachments) != 1 || shown.Attachments[0].OID != firstOID {
		t.Fatalf("attachments after rejected append = %+v, want the original untouched", shown.Attachments)
	}

	replaced := mustJSON[logJSON](t, mustRun(t, dir, "log", "append", added.ID, "--attach", second, "--replace", "--json"))
	want := attachmentJSON{Name: "trace.log", OID: secondOID, Size: 22, Present: true}
	if len(replaced.Attachments) != 1 || replaced.Attachments[0] != want {
		t.Fatalf("attachments after --replace = %+v, want %+v", replaced.Attachments, want)
	}
	if len(replaced.Entries) != 0 {
		t.Fatalf("entries = %+v, want none from an attach-only append", replaced.Entries)
	}
}

func TestEditRmAttachment(t *testing.T) {
	dir := initRepo(t)
	for _, tc := range []struct {
		kind string
	}{
		{kind: "note"},
		{kind: "doc"},
		{kind: "log"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			path, _ := writeAttachable(t, "scrap.txt", []byte("to be removed"))
			raw := mustRun(t, dir, tc.kind, "add", "Holder "+tc.kind, "--attach", path, "--json")
			var added struct {
				ID          string           `json:"id"`
				Attachments []attachmentJSON `json:"attachments"`
			}
			if err := json.Unmarshal([]byte(raw), &added); err != nil {
				t.Fatalf("unmarshal %q: %v", raw, err)
			}
			if len(added.Attachments) != 1 {
				t.Fatalf("attachments = %+v, want one", added.Attachments)
			}
			edited := mustRun(t, dir, tc.kind, "edit", added.ID, "--rm-attachment", "scrap.txt", "--json")
			var after struct {
				Attachments []attachmentJSON `json:"attachments"`
			}
			if err := json.Unmarshal([]byte(edited), &after); err != nil {
				t.Fatalf("unmarshal %q: %v", edited, err)
			}
			if len(after.Attachments) != 0 {
				t.Fatalf("attachments after --rm-attachment = %+v, want none", after.Attachments)
			}
		})
	}
}

func TestAttachUsageErrors(t *testing.T) {
	dir := initRepo(t)
	dup1, _ := writeAttachable(t, "same.txt", []byte("one"))
	dup2, _ := writeAttachable(t, "same.txt", []byte("two"))
	empty, _ := writeAttachable(t, "empty.bin", nil)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "duplicate names in one invocation",
			args: []string{"note", "add", "Dup", "--attach", dup1, "--attach", dup2},
			want: "duplicate attachment name",
		},
		{
			name: "missing file",
			args: []string{"note", "add", "Missing", "--attach", filepath.Join(dir, "no-such-file.txt")},
			want: "no-such-file.txt",
		},
		{
			name: "empty file",
			args: []string{"note", "add", "Empty", "--attach", empty},
			want: "size",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCLI(t, dir, tc.args...)
			if cli.ExitCode(err) != 2 || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v (exit %d), want usage error containing %q", err, cli.ExitCode(err), tc.want)
			}
			if notes := mustRun(t, dir, "note", "list"); strings.Contains(notes, tc.args[2]) {
				t.Fatalf("note list = %q: a rejected attach must create nothing", notes)
			}
		})
	}
}

func TestAttachInstallsPruneGuardOnce(t *testing.T) {
	dir := initRepo(t)
	first, _ := writeAttachable(t, "one.txt", []byte("one"))
	second, _ := writeAttachable(t, "two.txt", []byte("two"))

	_, stderr, err := runCLI(t, dir, "note", "add", "First", "--attach", first)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if !strings.Contains(stderr, store.PruneGuardConfig) {
		t.Fatalf("first attach stderr = %q, want the %s announcement", stderr, store.PruneGuardConfig)
	}
	if got := mustGit(t, dir, "config", "--get", "lfs.pruneverifyremotealways"); got != "true" {
		t.Fatalf("lfs.pruneverifyremotealways = %q, want true", got)
	}

	_, stderr, err = runCLI(t, dir, "note", "add", "Second", "--attach", second)
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if strings.Contains(stderr, store.PruneGuardConfig) {
		t.Fatalf("second attach stderr = %q, want no repeat announcement", stderr)
	}
}

// fakeLFS is a minimal httptest LFS server speaking batch + basic content
// PUT/GET + verify, enough to drive the CLI sync transfer phases.
type fakeLFS struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeLFS(t *testing.T) (*fakeLFS, string) {
	t.Helper()
	f := &fakeLFS{objects: map[string][]byte{}}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("POST /lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operation string `json:"operation"`
			Objects   []struct {
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			} `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		var out []map[string]any
		for _, obj := range req.Objects {
			res := map[string]any{"oid": obj.OID, "size": obj.Size}
			_, have := f.objects[obj.OID]
			switch req.Operation {
			case "upload":
				if !have {
					res["actions"] = map[string]any{
						"upload": map[string]any{"href": srv.URL + "/data/" + obj.OID},
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
		data, ok := f.objects[r.PathValue("oid")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})

	return f, srv.URL + "/lfs"
}

func TestSyncTransfersAttachments(t *testing.T) {
	dir, bare := initRepoWithRemote(t)
	server, endpoint := newFakeLFS(t)
	mustGit(t, dir, "config", "lfs.url", endpoint)

	content := []byte("synced attachment bytes")
	path, oid := writeAttachable(t, "artifact.bin", content)
	mustRun(t, dir, "note", "add", "Carrier", "--attach", path, "--json")

	out := mustRun(t, dir, "sync")
	if !strings.Contains(out, "uploaded: 1\n") || !strings.Contains(out, "pushed: 1\n") {
		t.Fatalf("sync output = %q, want uploaded: 1 and pushed: 1", out)
	}
	server.mu.Lock()
	uploaded := string(server.objects[oid])
	server.mu.Unlock()
	if uploaded != string(content) {
		t.Fatalf("server object = %q, want the attached bytes", uploaded)
	}

	clone := t.TempDir()
	mustGit(t, clone, "clone", "-q", bare, "repo")
	cloneDir := filepath.Join(clone, "repo")
	mustGit(t, cloneDir, "config", "user.name", "Test User")
	mustGit(t, cloneDir, "config", "user.email", "test@example.com")
	mustGit(t, cloneDir, "config", "lfs.url", endpoint)
	report := mustJSON[syncJSON](t, mustRun(t, cloneDir, "sync", "--json"))
	if report.Downloaded != 1 || report.Created != 1 {
		t.Fatalf("clone sync = %+v, want created: 1 and downloaded: 1", report)
	}
	notes := mustRun(t, cloneDir, "note", "list", "--json")
	var listed []noteJSON
	if err := json.Unmarshal([]byte(notes), &listed); err != nil {
		t.Fatalf("unmarshal %q: %v", notes, err)
	}
	if got := mustRun(t, cloneDir, "attachment", "get", listed[0].ID, "artifact.bin"); got != string(content) {
		t.Fatalf("cloned attachment get = %q, want %q", got, content)
	}
}

// TestSyncUnsupportedLFSRemote pins the failure surface for a remote with no
// LFS endpoint: the upload phase blocks the push, the error names the
// attachment and its removal remediation, and the partial report still
// prints before the non-zero exit.
func TestSyncUnsupportedLFSRemote(t *testing.T) {
	dir, bare := initRepoWithRemote(t)
	path, oid := writeAttachable(t, "stuck.bin", []byte("cannot upload"))
	mustRun(t, dir, "note", "add", "Stuck", "--attach", path)

	stdout, _, err := runCLI(t, dir, "sync")
	if err == nil {
		t.Fatal("sync against an LFS-less remote succeeded, want an upload failure")
	}
	for _, frag := range []string{oid, "--rm-attachment", "stuck.bin"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("sync error %q missing %q", err, frag)
		}
	}
	if !strings.Contains(stdout, "rounds: 1\n") {
		t.Errorf("sync stdout = %q, want the partial report before the error", stdout)
	}
	if refs := mustGit(t, bare, "for-each-ref", "refs/cc-notes/"); refs != "" {
		t.Errorf("remote refs = %q, want none: a failed upload must block the push", refs)
	}
}
