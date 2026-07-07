package lfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

// sshAuthenticate obtains an LFS endpoint grant over ssh (exec:
// `ssh [-p port] -- <user@host> git-lfs-authenticate <path> <operation>`)
// per the LFS server-discovery spec. The grant carries the batch endpoint
// href and the auth headers to send with every batch request; it is fetched
// per operation because the operation is part of the command.
func sshAuthenticate(ctx context.Context, ep Endpoint, operation string) (action, error) {
	var args []string
	if ep.SSHPort != "" {
		args = append(args, "-p", ep.SSHPort)
	}
	args = append(args, "--", ep.SSHUserHost, "git-lfs-authenticate", ep.SSHPath, operation)
	//nolint:gosec // G204: ssh args derive solely from the locally-configured git remote URL (validated ssh:// port is digits-only, scp-like sets none); the host sits behind -- so it can't inject ssh flags, and no shell is involved.
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return action{}, fmt.Errorf("ssh %s git-lfs-authenticate: %w: %s", ep.SSHUserHost, err, strings.TrimSpace(stderr.String()))
	}
	var grant action
	if err := json.Unmarshal(out.Bytes(), &grant); err != nil {
		return action{}, fmt.Errorf("ssh %s git-lfs-authenticate: parse: %w", ep.SSHUserHost, err)
	}
	return grant, nil
}

// extraHeader resolves the batch endpoint's http.<url>.extraheader git config —
// how actions/checkout injects CI auth — into the client's batch header map.
// Entries match the way git applies them (unscoped http.extraheader always
// matches; URL-scoped entries match via git's urlmatch). Because cc-notes
// carries a single header it refuses ambiguity loudly rather than silently
// dropping any: no match means no header, exactly one match is used, and more
// than one is a configuration error naming the matching keys.
func extraHeader(ctx context.Context, g gitcmd.Git, endpoint string) (map[string]string, error) {
	entries, err := g.ConfigGetRegexp(ctx, `^http\.(.+\.)?extraheader$`)
	if err != nil {
		return nil, fmt.Errorf("lfs extraheader: %w", err)
	}
	var matched [][2]string
	for _, e := range entries {
		if e[0] == "http.extraheader" {
			matched = append(matched, e)
			continue
		}
		ok, err := g.ConfigURLMatch(ctx, "http.extraheader", e[0], endpoint)
		if err != nil {
			return nil, fmt.Errorf("lfs extraheader: %w", err)
		}
		if ok {
			matched = append(matched, e)
		}
	}
	switch len(matched) {
	case 0:
		return nil, nil
	case 1:
		return parseExtraHeader(matched[0][0], matched[0][1])
	default:
		keys := make([]string, len(matched))
		for i, e := range matched {
			keys[i] = e[0]
		}
		return nil, fmt.Errorf("lfs extraheader: %d entries match %s (%s); cc-notes sends at most one — consolidate them", len(matched), endpoint, strings.Join(keys, ", "))
	}
}

// parseExtraHeader splits one extraheader config value keyed by key into a
// single-entry header map. The value is "<name>: <value>"; a missing colon or
// an empty header name is a config error reported without the value, since the
// value may be a secret that must not reach logs.
func parseExtraHeader(key, raw string) (map[string]string, error) {
	name, value, ok := strings.Cut(raw, ":")
	if !ok {
		return nil, fmt.Errorf("lfs extraheader %s: malformed value (missing colon)", key)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("lfs extraheader %s: malformed value (empty header name)", key)
	}
	return map[string]string{name: strings.TrimSpace(value)}, nil
}
