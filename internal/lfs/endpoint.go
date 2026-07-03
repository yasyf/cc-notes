package lfs

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

// Endpoint locates one remote's LFS server: the batch API base URL plus, for
// ssh remotes, the metadata git-lfs-authenticate needs.
type Endpoint struct {
	Href        string
	SSHUserHost string
	SSHPort     string
	SSHPath     string
}

// Discover resolves remote's LFS endpoint. Config overrides win — lfs.url,
// then remote.<remote>.lfsurl, read full-scope, the precedence git-lfs
// itself applies — used verbatim as the endpoint; otherwise the endpoint
// derives from the remote URL per the LFS server-discovery spec. A remote
// whose URL supports no LFS endpoint (a local path, file://) wraps
// ErrUnsupported. .lfsconfig files are a v1 non-goal.
func Discover(ctx context.Context, g gitcmd.Git, remote string) (Endpoint, error) {
	for _, key := range []string{"lfs.url", "remote." + remote + ".lfsurl"} {
		href, err := g.ConfigGet(ctx, key)
		if err != nil {
			return Endpoint{}, fmt.Errorf("discover lfs endpoint: %w", err)
		}
		if href != "" {
			return Endpoint{Href: strings.TrimSuffix(href, "/")}, nil
		}
	}
	raw, err := g.RemoteURL(ctx, remote)
	if err != nil {
		return Endpoint{}, fmt.Errorf("discover lfs endpoint: %w", err)
	}
	ep, err := derive(raw)
	if err != nil {
		return Endpoint{}, fmt.Errorf("discover lfs endpoint for %s: %w", remote, err)
	}
	return ep, nil
}

// derive maps a git remote URL to its default LFS endpoint per the server
// discovery spec: https and http URLs gain ".git" (when absent) and
// "/info/lfs"; ssh:// and scp-like remotes derive the same https endpoint
// plus the ssh metadata for git-lfs-authenticate.
func derive(remote string) (Endpoint, error) {
	switch {
	case strings.HasPrefix(remote, "https://"), strings.HasPrefix(remote, "http://"):
		base := strings.TrimSuffix(remote, "/")
		if !strings.HasSuffix(base, ".git") {
			base += ".git"
		}
		return Endpoint{Href: base + "/info/lfs"}, nil
	case strings.HasPrefix(remote, "ssh://"):
		u, err := url.Parse(remote)
		if err != nil {
			return Endpoint{}, fmt.Errorf("parse ssh remote %q: %w", remote, err)
		}
		path := strings.TrimPrefix(u.Path, "/")
		userHost := u.Host
		if u.Port() != "" {
			userHost = u.Hostname()
		}
		if u.User != nil {
			userHost = u.User.Username() + "@" + userHost
		}
		ep, err := derive("https://" + u.Hostname() + "/" + path)
		if err != nil {
			return Endpoint{}, err
		}
		ep.SSHUserHost, ep.SSHPort, ep.SSHPath = userHost, u.Port(), path
		return ep, nil
	case strings.Contains(remote, "://"):
		return Endpoint{}, fmt.Errorf("remote %q: %w", remote, ErrUnsupported)
	default:
		// scp-like: [user@]host:path
		userHost, path, ok := strings.Cut(remote, ":")
		if !ok || strings.Contains(userHost, "/") {
			return Endpoint{}, fmt.Errorf("remote %q: %w", remote, ErrUnsupported)
		}
		host := userHost
		if _, h, ok := strings.Cut(userHost, "@"); ok {
			host = h
		}
		ep, err := derive("https://" + host + "/" + path)
		if err != nil {
			return Endpoint{}, err
		}
		ep.SSHUserHost, ep.SSHPath = userHost, path
		return ep, nil
	}
}
