// Package gitguard runs one git command under a guardian, the calling binary
// re-executed, which holds a lease file until that git exits. The guardian's
// duplicate of the lease keeps a flock past the caller's death, and git and
// its children never receive the lease.
package gitguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const childArg = "--cc-notes-gitguard-v1"

const (
	leaseFD = 3 + iota
	stdoutFD
	stderrFD
	statusFD
)

const guardFailure = 125

// Exits from package init: the internal/gitguard exception in STYLEGUIDE.md.
func init() {
	if len(os.Args) > 2 && os.Args[1] == childArg && os.Args[2] == "--" {
		os.Exit(serve(os.Args[3:]))
	}
}

// Command returns a guardian that holds lease while it runs git with argv
// under env. Once git exits, the guardian writes git's output to stdout and
// stderr and "exit N" or "signal N" to status, closes lease, and exits with
// N, or 128+N for a signal. Cancelling ctx sends it SIGTERM, which kills
// git. The guardian writes its own stderr only on its own failure, exit 125.
func Command(ctx context.Context, lease, stdout, stderr, status *os.File, env, argv []string) (*exec.Cmd, error) {
	exe, err := executable()
	if err != nil {
		return nil, fmt.Errorf("git guardian executable: %w", err)
	}
	//nolint:gosec // G204: re-executes this binary as the git guardian; argv is an internal git subcommand, not user-shell input.
	cmd := exec.CommandContext(ctx, exe, append([]string{childArg, "--"}, argv...)...)
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{lease, stdout, stderr, status}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	return cmd, nil
}

func serve(argv []string) int {
	for fd := leaseFD; fd <= statusFD; fd++ {
		syscall.CloseOnExec(fd)
	}
	lease := os.NewFile(leaseFD, "lease")
	stdout := os.NewFile(stdoutFD, "stdout")
	stderr := os.NewFile(stderrFD, "stderr")
	status := os.NewFile(statusFD, "status")
	for _, f := range []*os.File{lease, stdout, stderr, status} {
		if _, err := f.Stat(); err != nil {
			return fail(err)
		}
	}
	interrupts := make(chan os.Signal, 1)
	for _, sig := range []os.Signal{syscall.SIGINT, syscall.SIGHUP} {
		if !signal.Ignored(sig) {
			signal.Notify(interrupts, sig)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	//nolint:gosec // G204: git is a fixed argv[0]; argv is the caller's internal git subcommand.
	git := exec.CommandContext(ctx, "git", argv...)
	git.Stdout, git.Stderr = outWrite, errWrite
	if err := git.Start(); err != nil {
		return fail(err)
	}
	if err := errors.Join(outWrite.Close(), errWrite.Close()); err != nil {
		return fail(err)
	}
	outDrained, errDrained := drainWhileRunning(outRead), drainWhileRunning(errRead)
	if err := git.Wait(); git.ProcessState == nil {
		return fail(err)
	}
	outBuf, err := finishDrain(outRead, outDrained)
	if err != nil {
		return fail(err)
	}
	errBuf, err := finishDrain(errRead, errDrained)
	if err != nil {
		return fail(err)
	}
	if err := writeCapture(stdout, outBuf); err != nil {
		return fail(err)
	}
	if err := writeCapture(stderr, errBuf); err != nil {
		return fail(err)
	}
	code, report := exitReport(git.ProcessState)
	if err := writeCapture(status, []byte(report)); err != nil {
		return fail(err)
	}
	if err := lease.Close(); err != nil {
		return fail(err)
	}
	return code
}

func exitReport(state *os.ProcessState) (int, string) {
	wait := state.Sys().(syscall.WaitStatus)
	if wait.Signaled() {
		return 128 + int(wait.Signal()), fmt.Sprintf("signal %d", wait.Signal())
	}
	return wait.ExitStatus(), fmt.Sprintf("exit %d", wait.ExitStatus())
}

type drained struct {
	buf *bytes.Buffer
	err error
}

func drainWhileRunning(r *os.File) <-chan drained {
	done := make(chan drained, 1)
	go func() {
		var buf bytes.Buffer
		err := drain(r, &buf, false)
		done <- drained{&buf, err}
	}()
	return done
}

func finishDrain(r *os.File, running <-chan drained) ([]byte, error) {
	if err := r.SetReadDeadline(time.Now()); err != nil {
		return nil, err
	}
	d := <-running
	if d.err != nil && !errors.Is(d.err, os.ErrDeadlineExceeded) {
		return nil, d.err
	}
	if err := r.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if err := drain(r, d.buf, true); err != nil {
		return nil, err
	}
	return d.buf.Bytes(), r.Close()
}

func drain(r *os.File, buf *bytes.Buffer, final bool) error {
	conn, err := r.SyscallConn()
	if err != nil {
		return err
	}
	chunk := make([]byte, 32<<10)
	var readErr error
	err = conn.Read(func(fd uintptr) bool {
		for {
			n, err := syscall.Read(int(fd), chunk)
			switch {
			case errors.Is(err, syscall.EINTR):
				continue
			case errors.Is(err, syscall.EAGAIN):
				return final
			case err != nil:
				readErr = err
				return true
			case n == 0:
				return true
			}
			buf.Write(chunk[:n])
		}
	})
	return errors.Join(err, readErr)
}

func writeCapture(f *os.File, out []byte) error {
	_, err := f.Write(out)
	return errors.Join(err, f.Close())
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "cc-notes gitguard: %v\n", err)
	return guardFailure
}
