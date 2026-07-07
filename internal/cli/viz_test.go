package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/cli"
)

func TestVizBadFlag(t *testing.T) {
	dir := initRepo(t)
	_, _, err := runCLI(t, dir, "viz", "--nope")
	if err == nil {
		t.Fatal("viz --nope returned nil, want a usage error")
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage)", got)
	}
}

// TestVizServesAndShutsDown drives the command through the root with a
// cancellable context: it prints the loopback URL alone on stdout, then exits 0
// when the context is cancelled (the SIGINT path).
func TestVizServesAndShutsDown(t *testing.T) {
	dir := initRepo(t)
	t.Chdir(dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := cli.NewRootCmd()
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	root.SetOut(pw)
	root.SetErr(&stderr)
	root.SetArgs([]string{"viz", "--no-open", "--port", "0"})

	done := make(chan error, 1)
	go func() {
		err := root.ExecuteContext(ctx)
		_ = pw.Close()
		done <- err
	}()

	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatalf("read url line: %v (stderr %q)", err, stderr.String())
	}
	url := strings.TrimSpace(line)
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("stdout url = %q, want http://127.0.0.1:<port>", url)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("viz returned %v, want nil on context cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("viz did not shut down within 5s of context cancel")
	}
}

// TestVizPortInUse proves a bind failure is a plain error (exit 1), not a hang:
// the command fails before it starts serving.
func TestVizPortInUse(t *testing.T) {
	dir := initRepo(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	_, stderr, err := runCLI(t, dir, "viz", "--no-open", "--port", strconv.Itoa(port))
	if err == nil {
		t.Fatalf("viz on occupied port %d returned nil, want error (stderr %q)", port, stderr)
	}
	if got := cli.ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1 (error)", got)
	}
}
