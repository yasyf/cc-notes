package lfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
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
