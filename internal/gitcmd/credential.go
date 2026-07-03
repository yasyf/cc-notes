package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// Credential is one git-credential answer: the username/password pair the
// configured helpers produced for a URL.
type Credential struct{ Username, Password string }

// credential execs `git credential <verb>` with the description on stdin.
// GIT_TERMINAL_PROMPT=0 rides every invocation: helpers may answer, but git
// itself must never block a sync waiting for a terminal.
func (g Git) credential(ctx context.Context, verb, input string) (string, error) {
	//nolint:gosec // G204: git is a fixed argv[0]; verb is an internal constant.
	cmd := exec.CommandContext(ctx, "git", "-C", g.Dir, "credential", verb)
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", &commandError{args: []string{"credential", verb}, stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return stdout.String(), nil
}

// credentialInput renders the key=value description git credential reads:
// protocol, host, path, plus any known username/password, blank-line
// terminated.
func credentialInput(rawurl string, cred Credential) (string, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", fmt.Errorf("parse credential url %q: %w", rawurl, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "protocol=%s\nhost=%s\npath=%s\n", u.Scheme, u.Host, strings.TrimPrefix(u.Path, "/"))
	if cred.Username != "" {
		fmt.Fprintf(&b, "username=%s\n", cred.Username)
	}
	if cred.Password != "" {
		fmt.Fprintf(&b, "password=%s\n", cred.Password)
	}
	b.WriteByte('\n')
	return b.String(), nil
}

// CredentialFill asks git's credential machinery — config, helpers, caches —
// for rawurl's credentials via `git credential fill`.
func (g Git) CredentialFill(ctx context.Context, rawurl string) (Credential, error) {
	input, err := credentialInput(rawurl, Credential{})
	if err != nil {
		return Credential{}, err
	}
	out, err := g.credential(ctx, "fill", input)
	if err != nil {
		return Credential{}, fmt.Errorf("credential fill %s: %w", rawurl, err)
	}
	var cred Credential
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(line, "username="); ok {
			cred.Username = v
		}
		if v, ok := strings.CutPrefix(line, "password="); ok {
			cred.Password = v
		}
	}
	if cred.Username == "" && cred.Password == "" {
		return Credential{}, fmt.Errorf("credential fill %s: no credential returned", rawurl)
	}
	return cred, nil
}

// CredentialApprove reports cred as accepted for rawurl via `git credential
// approve`, so helpers cache it for the next run.
func (g Git) CredentialApprove(ctx context.Context, rawurl string, cred Credential) error {
	input, err := credentialInput(rawurl, cred)
	if err != nil {
		return err
	}
	if _, err := g.credential(ctx, "approve", input); err != nil {
		return fmt.Errorf("credential approve %s: %w", rawurl, err)
	}
	return nil
}

// CredentialReject reports cred as rejected for rawurl via `git credential
// reject`, so helpers purge the bad entry.
func (g Git) CredentialReject(ctx context.Context, rawurl string, cred Credential) error {
	input, err := credentialInput(rawurl, cred)
	if err != nil {
		return err
	}
	if _, err := g.credential(ctx, "reject", input); err != nil {
		return fmt.Errorf("credential reject %s: %w", rawurl, err)
	}
	return nil
}
