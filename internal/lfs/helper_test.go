package lfs_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/lfs"
)

func initRepo(t *testing.T) gitcmd.Git {
	t.Helper()
	return gitcmd.Git{Dir: gittest.InitRepo(t)}
}

// mintedToken is the per-action header value the fake server demands on
// every content request, proving action headers propagate verbatim.
const mintedToken = "tok"

// fakeLFS is an httptest LFS server speaking batch + basic content
// PUT/GET + verify. Content endpoints reject Authorization outright: action
// hrefs stand in for pre-signed URLs on other hosts, so a leaked batch
// credential is a test failure. PUT additionally demands the exact
// Content-Length and application/octet-stream the basic transfer spec's
// S3-shaped servers require.
type fakeLFS struct {
	mu          sync.Mutex
	objects     map[string][]byte
	verified    map[string]bool
	batchBodies [][]byte
	requireAuth string // exact batch Authorization demanded when set
}

func (f *fakeLFS) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batchBodies)
}

func (f *fakeLFS) lastBatchBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.batchBodies[len(f.batchBodies)-1])
}

func (f *fakeLFS) put(oid string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[oid] = data
}

func (f *fakeLFS) isVerified(oid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verified[oid]
}

func newFakeLFS(t *testing.T) (*fakeLFS, *httptest.Server) {
	t.Helper()
	f := &fakeLFS{objects: map[string][]byte{}, verified: map[string]bool{}}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rejectLeakedAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "batch auth leaked to content request", http.StatusForbidden)
			return true
		}
		if r.Header.Get("X-Fake-Token") != mintedToken {
			http.Error(w, "missing action header", http.StatusForbidden)
			return true
		}
		return false
	}

	mux.HandleFunc("POST /lfs/objects/batch", func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, lfs.MediaType) {
			http.Error(w, "bad content type "+ct, http.StatusNotAcceptable)
			return
		}
		if f.requireAuth != "" && r.Header.Get("Authorization") != f.requireAuth {
			w.Header().Set("LFS-Authenticate", `Basic realm="Git LFS"`)
			w.Header().Set("Content-Type", lfs.MediaType)
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Credentials needed"})
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var req struct {
			Operation string       `json:"operation"`
			Objects   []lfs.Object `json:"objects"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		header := map[string]string{"X-Fake-Token": mintedToken}
		var out []map[string]any
		f.mu.Lock()
		defer f.mu.Unlock()
		f.batchBodies = append(f.batchBodies, body)
		for _, obj := range req.Objects {
			res := map[string]any{"oid": obj.OID, "size": obj.Size, "authenticated": true}
			_, have := f.objects[obj.OID]
			switch req.Operation {
			case "upload":
				if !have {
					res["actions"] = map[string]any{
						"upload": map[string]any{"href": srv.URL + "/data/" + obj.OID, "header": header},
						"verify": map[string]any{"href": srv.URL + "/verify", "header": header},
					}
				}
			case "download":
				if have {
					res["actions"] = map[string]any{
						"download": map[string]any{"href": srv.URL + "/data/" + obj.OID, "header": header},
					}
				} else {
					res["error"] = map[string]any{"code": 404, "message": "Object does not exist"}
				}
			}
			out = append(out, res)
		}
		w.Header().Set("Content-Type", lfs.MediaType)
		_ = json.NewEncoder(w).Encode(map[string]any{"transfer": "basic", "objects": out, "hash_algo": "sha256"})
	})

	mux.HandleFunc("PUT /data/{oid}", func(w http.ResponseWriter, r *http.Request) {
		if rejectLeakedAuth(w, r) {
			return
		}
		if r.ContentLength < 0 {
			http.Error(w, "chunked upload rejected", http.StatusLengthRequired)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
			http.Error(w, "bad content type "+ct, http.StatusUnsupportedMediaType)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if int64(len(data)) != r.ContentLength {
			http.Error(w, "short body", http.StatusBadRequest)
			return
		}
		f.put(r.PathValue("oid"), data)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /data/{oid}", func(w http.ResponseWriter, r *http.Request) {
		if rejectLeakedAuth(w, r) {
			return
		}
		f.mu.Lock()
		data, ok := f.objects[r.PathValue("oid")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})

	mux.HandleFunc("POST /verify", func(w http.ResponseWriter, r *http.Request) {
		if rejectLeakedAuth(w, r) {
			return
		}
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

	return f, srv
}
