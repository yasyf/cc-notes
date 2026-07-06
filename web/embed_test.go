//go:build webui

package web

import (
	"io/fs"
	"testing"
)

// TestDistEmbedsIndex proves the webui build carries the SPA entrypoint: the
// embed directive rooted the build output and dist/index.html is readable, so
// the viz server's fs.Sub("dist") + index.html fallback has a page to serve.
func TestDistEmbedsIndex(t *testing.T) {
	if !Embedded {
		t.Fatal("Embedded = false in the webui build, want true")
	}
	data, err := fs.ReadFile(Dist, "dist/index.html")
	if err != nil {
		t.Fatalf("read dist/index.html: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("dist/index.html is empty, want the built SPA shell")
	}
}
