// Package gitguard runs one git command under a guardian, the calling binary
// re-executed, which holds a lease file until that git exits. The guardian's
// duplicate of the lease keeps a flock past the caller's death, and git and
// its children never receive the lease.
package gitguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

const childArg = "--cc-notes-gitguard-v1"

const (
	leaseFD = 3 + iota
	stdoutFD
	stderrFD
)

const guardFailure = 125

// Runs before main or TestMain in every binary linking gitcmd (CLI, MCP
// server, test binaries, library hosts): the deliberate exception to
// STYLEGUIDE's no-os.Exit rule for internal packages.
func init() {
	if len(os.Args) > 2 && os.Args[1] == childArg && os.Args[2] == "--" {
		os.Exit(serve(os.Args[3:]))
	}
}

// Command returns a guardian that holds lease while it runs git with argv
// under env, git writing to stdout and stderr and reading no input. The
// guardian closes lease only after git exits, then mirrors git's exit status
// or signal. Cancelling ctx sends it SIGTERM, which it forwards to git; it
// writes its own stdout or stderr only when it fails itself, exiting 125.
func Command(ctx context.Context, lease, stdout, stderr *os.File, env, argv []string) (*exec.Cmd, error) {
	exe, err := executable()
	if err != nil {
		return nil, fmt.Errorf("git guardian executable: %w", err)
	}
	//nolint:gosec // G204: re-executes this binary as the git guardian; argv is an internal git subcommand, not user-shell input.
	cmd := exec.CommandContext(ctx, exe, append([]string{childArg, "--"}, argv...)...)
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{lease, stdout, stderr}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	return cmd, nil
}

func serve(argv []string) int {
	for fd := leaseFD; fd <= stderrFD; fd++ {
		syscall.CloseOnExec(fd)
	}
	lease := os.NewFile(leaseFD, "lease")
	stdout := os.NewFile(stdoutFD, "stdout")
	stderr := os.NewFile(stderrFD, "stderr")
	for _, f := range []*os.File{lease, stdout, stderr} {
		if _, err := f.Stat(); err != nil {
			return fail(err)
		}
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, syscall.SIGINT, syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	//nolint:gosec // G204: git is a fixed argv[0]; argv is the caller's internal git subcommand.
	git := exec.CommandContext(ctx, "git", argv...)
	git.Stdout, git.Stderr = stdout, stderr
	git.Cancel = func() error { return git.Process.Signal(syscall.SIGTERM) }
	runErr := git.Run()
	if err := lease.Close(); err != nil {
		return fail(err)
	}
	if git.ProcessState == nil {
		return fail(runErr)
	}
	status := git.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() {
		return status.ExitStatus()
	}
	sig := status.Signal()
	signal.Reset(sig)
	_ = syscall.Kill(os.Getpid(), sig)
	return 128 + int(sig)
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "cc-notes gitguard: %v\n", err)
	return guardFailure
}
