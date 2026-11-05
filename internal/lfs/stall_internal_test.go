package lfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetObjectFailsAsStalledWhenNoBytesArrive(t *testing.T) {
	saved := stallAfter
	stallAfter = 200 * time.Millisecond
	t.Cleanup(func() { stallAfter = saved })
	content := []byte("a blob the CDN stops sending")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	tests := []struct {
		name  string
		serve func(w http.ResponseWriter, r *http.Request)
	}{
		{"before any header", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
		{"midway through the body", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "28")
			_, _ = w.Write(content[:8])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tc.serve))
			t.Cleanup(srv.Close)
			start := time.Now()
			err := (&Client{}).getObject(t.Context(), Store{Dir: t.TempDir()}, batchObject{OID: oid, Size: int64(len(content))}, action{Href: srv.URL})
			var stalled *StalledError
			if !errors.As(err, &stalled) || stalled.OID != oid {
				t.Fatalf("getObject from a server that stops sending = %v, want a *StalledError naming %s", err, oid)
			}
			if waited := time.Since(start); waited > 10*stallAfter {
				t.Fatalf("getObject gave up after %s, want about stallAfter %s", waited, stallAfter)
			}
		})
	}
}
