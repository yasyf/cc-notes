package viz

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser best-effort launches the platform browser at url: `open` on
// macOS, `xdg-open` on Linux. Any other platform, or a launch failure, returns
// an error the caller surfaces as a one-line stderr hint — opening a browser is
// a convenience, never a precondition for serving. The URL is the server's own
// loopback address, so the fixed-binary exec is not an injection surface.
func OpenBrowser(ctx context.Context, url string) error {
	var name string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "linux":
		name = "xdg-open"
	default:
		return fmt.Errorf("no browser opener for %s", runtime.GOOS)
	}
	//nolint:gosec // G204: fixed binary name, server-owned loopback URL.
	if err := exec.CommandContext(ctx, name, url).Start(); err != nil {
		return fmt.Errorf("launch %s: %w", name, err)
	}
	return nil
}
