package lfs_test

import (
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/internal/lfs"
)

// TestDiscoverDerivation covers the five remote-URL shapes from the LFS
// server-discovery spec, through real git config via `git remote add`.
func TestDiscoverDerivation(t *testing.T) {
	cases := []struct {
		name, remote string
		want         lfs.Endpoint
	}{
		{
			name:   "https with .git",
			remote: "https://git-server.com/foo/bar.git",
			want:   lfs.Endpoint{Href: "https://git-server.com/foo/bar.git/info/lfs"},
		},
		{
			name:   "https without .git",
			remote: "https://git-server.com/foo/bar",
			want:   lfs.Endpoint{Href: "https://git-server.com/foo/bar.git/info/lfs"},
		},
		{
			name:   "scp-like",
			remote: "git@git-server.com:foo/bar.git",
			want: lfs.Endpoint{
				Href:        "https://git-server.com/foo/bar.git/info/lfs",
				SSHUserHost: "git@git-server.com",
				SSHPath:     "foo/bar.git",
			},
		},
		{
			name:   "ssh url",
			remote: "ssh://git-server.com/foo/bar.git",
			want: lfs.Endpoint{
				Href:        "https://git-server.com/foo/bar.git/info/lfs",
				SSHUserHost: "git-server.com",
				SSHPath:     "foo/bar.git",
			},
		},
		{
			name:   "ssh url with user and port",
			remote: "ssh://git@git-server.com:2222/foo/bar.git",
			want: lfs.Endpoint{
				Href:        "https://git-server.com/foo/bar.git/info/lfs",
				SSHUserHost: "git@git-server.com",
				SSHPort:     "2222",
				SSHPath:     "foo/bar.git",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := initRepo(t)
			mustGit(t, g.Dir, "remote", "add", "origin", tc.remote)
			got, err := lfs.Discover(t.Context(), g, "origin")
			if err != nil {
				t.Fatalf("Discover(%q): %v", tc.remote, err)
			}
			if got != tc.want {
				t.Fatalf("Discover(%q) = %+v, want %+v", tc.remote, got, tc.want)
			}
		})
	}
}

// TestDiscoverConfigPrecedence pins the override order — lfs.url beats
// remote.<r>.lfsurl beats derivation — matching git-lfs v3.7.1, so it can
// never silently flip.
func TestDiscoverConfigPrecedence(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	mustGit(t, g.Dir, "remote", "add", "origin", "https://git-server.com/foo/bar.git")

	mustGit(t, g.Dir, "config", "remote.origin.lfsurl", "https://lfsurl.example/lfs/")
	got, err := lfs.Discover(ctx, g, "origin")
	if err != nil {
		t.Fatalf("Discover with remote lfsurl: %v", err)
	}
	// Overrides are the endpoint verbatim — no /info/lfs — trailing slash
	// trimmed.
	if want := (lfs.Endpoint{Href: "https://lfsurl.example/lfs"}); got != want {
		t.Fatalf("remote.origin.lfsurl override = %+v, want %+v", got, want)
	}

	mustGit(t, g.Dir, "config", "lfs.url", "https://lfsurl-global.example/lfs")
	got, err = lfs.Discover(ctx, g, "origin")
	if err != nil {
		t.Fatalf("Discover with both keys: %v", err)
	}
	if want := (lfs.Endpoint{Href: "https://lfsurl-global.example/lfs"}); got != want {
		t.Fatalf("both keys set: got %+v, want lfs.url to win: %+v", got, want)
	}
}

func TestDiscoverUnsupportedRemotes(t *testing.T) {
	for _, tc := range []struct {
		name, remote string
	}{
		{name: "local path", remote: "/tmp/some/repo"},
		{name: "file url", remote: "file:///tmp/some/repo"},
		{name: "git protocol", remote: "git://git-server.com/foo/bar.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := initRepo(t)
			mustGit(t, g.Dir, "remote", "add", "origin", tc.remote)
			if _, err := lfs.Discover(t.Context(), g, "origin"); !errors.Is(err, lfs.ErrUnsupported) {
				t.Fatalf("Discover(%q) = %v, want ErrUnsupported", tc.remote, err)
			}
		})
	}
}
