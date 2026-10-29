package cli_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
)

func TestCancelledClientOpenDoesNotDiscoverRepository(t *testing.T) {
	dir := initRepo(t)
	trace := traceGit(t)
	if err := os.WriteFile(string(trace), nil, 0o600); err != nil {
		t.Fatalf("reset trace: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := cli.NewRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--repo", dir, "doc", "list", "--json"})
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command = %v, want context.Canceled", err)
	}
	data, err := os.ReadFile(string(trace))
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("cancelled client open executed git: %q", data)
	}
}
