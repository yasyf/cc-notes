package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/viz"
)

// vizOptions collects the viz command's flags. poll is the interval the liveness
// Watcher polls refs at to feed the /api/stream SSE endpoint.
type vizOptions struct {
	port   int
	noOpen bool
	poll   time.Duration
}

// newVizCmd builds "cc-notes viz": serve the branch-and-task visualization over
// loopback HTTP and open it in a browser. The server is read-only and shuts
// down cleanly on SIGINT/SIGTERM.
func newVizCmd() *cobra.Command {
	var opts vizOptions
	cmd := &cobra.Command{
		Use:   "viz",
		Short: "Serve the branch-and-task visualization in a browser",
		Args:  maxArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runViz(cmd, opts)
		},
	}
	f := cmd.Flags()
	f.IntVar(&opts.port, "port", 0, "TCP port to listen on (0 = ephemeral)")
	f.BoolVar(&opts.noOpen, "no-open", false, "do not open a browser")
	f.DurationVar(&opts.poll, "poll", 2*time.Second, "liveness poll interval")
	return cmd
}

// runViz opens the store, binds a loopback listener, prints the URL on stdout
// (alone) and a human line on stderr, best-effort opens a browser, then serves
// until the command context is cancelled and shuts down within 3s.
func runViz(cmd *cobra.Command, opts vizOptions) error {
	if opts.poll <= 0 {
		return &UsageError{Err: fmt.Errorf("--poll must be positive, got %s", opts.poll)}
	}
	s, err := openStore(cmd)
	if err != nil {
		return err
	}
	builder := viz.NewBuilder(s)
	srv := viz.NewServer(s, builder)
	hub := srv.Hub()
	watcher := viz.NewWatcher(s, builder, hub, opts.poll)

	addr := fmt.Sprintf("127.0.0.1:%d", opts.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	url := "http://" + ln.Addr().String()
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), url); err != nil {
		_ = ln.Close()
		return err
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes viz serving %s (Ctrl-C to stop)\n", url)
	if !opts.noOpen {
		if err := viz.OpenBrowser(cmd.Context(), url); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: could not open a browser (%v); open %s yourself\n", err, url)
		}
	}

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 5 * time.Second}
	g, gctx := errgroup.WithContext(cmd.Context())
	g.Go(func() error {
		return watcher.Run(gctx)
	})
	g.Go(func() error {
		if err := httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(gctx), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return nil
	})

	// Order matters: g.Wait blocks until the Watcher (the hub's only publisher)
	// has returned and the server has stopped, so hub.Close cannot race a send on
	// a closed channel.
	err = g.Wait()
	hub.Close()
	return err
}
