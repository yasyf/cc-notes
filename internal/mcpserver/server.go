package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
)

// Config injects the internal/cli seam the server drives, so this package does
// not import internal/cli (which imports this one to register the mcp command).
type Config struct {
	Version string
	NewRoot func() *cobra.Command
	Label   func(error) string
	// Message renders an error's body as the CLI does, trimming the notes-layer
	// "cc-notes: " prefix so it is not doubled under the Label.
	Message func(error) string
}

// New builds the MCP server with every tool table registered.
func New(cfg Config) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "cc-notes", Version: cfg.Version},
		&mcp.ServerOptions{Instructions: instructions},
	)
	b := &bridge{newRoot: cfg.NewRoot, label: cfg.Label, message: cfg.Message}
	ts := &toolset{srv: srv, props: map[string]toolProps{}}
	registerAll(ts, b)
	srv.AddReceivingMiddleware(didYouMeanMiddleware(ts.props))
	return srv
}

// registerAll installs every tool table onto ts, recording each tool's accepted
// properties for the did-you-mean middleware.
func registerAll(ts *toolset, b *bridge) {
	registerRepo(ts, b)
	registerNote(ts, b)
	registerDoc(ts, b)
	registerLog(ts, b)
	registerPapercut(ts, b)
	registerTask(ts, b)
	registerPlanning(ts, b)
	registerRunbook(ts, b)
	registerInvestigation(ts, b)
}

// Serve resolves the project directory, chdirs once (per-call chdir would race
// concurrent tool calls), writes the liveness marker, and runs the server over
// stdio until ctx is cancelled — at which point the deferred marker cleanup
// runs. A signal-initiated stop is a clean shutdown, not a failure: the
// cancellation-induced transport error is suppressed so the process exits 0.
func Serve(ctx context.Context, dir string, cfg Config) error {
	workdir, err := resolveWorkdir(dir)
	if err != nil {
		return err
	}
	if err := os.Chdir(workdir); err != nil {
		return fmt.Errorf("chdir %s: %w", workdir, err)
	}
	s, err := store.OpenContext(ctx, workdir)
	if err != nil {
		return err
	}
	common, err := s.Git.CommonDir(ctx)
	if err != nil {
		return err
	}
	markerDir := filepath.Join(common, "cc-notes", "mcp")
	if err := WriteMarker(markerDir); err != nil {
		return err
	}
	defer RemoveMarker(markerDir)
	err = New(cfg).Run(ctx, &mcp.StdioTransport{})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// resolveWorkdir picks the project directory: the --dir flag, then
// $CLAUDE_PROJECT_DIR, then the current working directory.
func resolveWorkdir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	if env := os.Getenv("CLAUDE_PROJECT_DIR"); env != "" {
		return env, nil
	}
	return os.Getwd()
}
