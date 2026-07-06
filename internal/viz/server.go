package viz

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/web"
)

// noWebUIPage is the placeholder served for every non-API route when the binary
// was built without the web UI (the default build). It points at the two ways
// to get a UI: a release binary that embeds it, or the Vite dev server proxying
// this API.
const noWebUIPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>cc-notes viz</title></head>
<body>
<h1>cc-notes viz</h1>
<p>This binary was built without the web UI, so only the JSON API under
<code>/api</code> is served here.</p>
<p>To see the visualization, either install a release binary (which embeds the
UI) or run the dev server against this API:</p>
<pre>cc-notes viz --no-open --port 5177
cd web &amp;&amp; npm run dev</pre>
<p>The dev server proxies <code>/api</code> to the port above.</p>
</body>
</html>
`

// Server is the read-only HTTP surface over one repository's visualization
// graph: a small stdlib mux fronting the JSON API and, in a webui build, the
// embedded single-page app. It holds no mutable state — every handler reads
// through the Builder and Store under the request context — so one Server is
// safe for concurrent requests.
type Server struct {
	store   *store.Store
	builder *Builder
	hub     *Hub
	mux     *http.ServeMux

	// static serves the embedded SPA with an index.html fallback; it is nil in a
	// build without the web UI, where handleRoot serves noWebUIPage instead.
	static http.Handler
}

// NewServer builds the viz HTTP handler over the store and its Builder. It owns
// the SSE Hub that backs GET /api/stream; the caller drives a Watcher that
// publishes to Hub() and closes it on shutdown.
func NewServer(s *store.Store, b *Builder) *Server {
	srv := &Server{store: s, builder: b, hub: NewHub(), mux: http.NewServeMux()}
	if web.Embedded {
		srv.static = spaHandler()
	}
	srv.mux.HandleFunc("GET /api/repo", srv.handleRepo)
	srv.mux.HandleFunc("GET /api/graph", srv.handleGraph)
	srv.mux.HandleFunc("GET /api/commits", srv.handleCommits)
	srv.mux.HandleFunc("GET /api/entity/{kind}/{id}", srv.handleEntity)
	srv.mux.HandleFunc("GET /api/stream", srv.handleStream)
	srv.mux.HandleFunc("GET /", srv.handleRoot)
	return srv
}

// Hub returns the server's SSE hub, so the caller's Watcher can publish
// ref-change events to every attached /api/stream client and close it on
// shutdown.
func (s *Server) Hub() *Hub { return s.hub }

// ServeHTTP dispatches through the internal mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handleRoot serves the web UI: an unregistered /api path is a JSON 404 (the
// SPA never shadows the API), and every other path is the embedded SPA or, in a
// build without the web UI, the placeholder page.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "no such endpoint: "+r.URL.Path)
		return
	}
	if s.static == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(noWebUIPage))
		return
	}
	s.static.ServeHTTP(w, r)
}

// spaHandler serves the embedded dist/ subtree with a single-page-app fallback:
// a request for a missing path with no file extension rewrites to index.html so
// client-side routes resolve, while a missing asset with an extension stays a
// 404.
func spaHandler() http.Handler {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic("viz: embedded web UI missing dist subtree: " + err.Error())
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, statErr := fs.Stat(sub, clean); statErr != nil && path.Ext(clean) == "" {
			r.URL.Path = "/index.html"
		}
		files.ServeHTTP(w, r)
	})
}

// writeJSON marshals v as one compact JSON document with status. Encoding these
// small wire structs cannot fail; a post-header failure has nothing to recover.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// writeError writes {"error": msg} with status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
