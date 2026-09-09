//go:build darwin

package helperpackage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yasyf/cc-notes/internal/helperclient"
)

// Invoke runs one deployment verb in the packaged signed helper. The packaged
// copy runs it, never the installed one: superseding a generation must prove
// the installed executables empty, which a controller inside them forbids.
func Invoke(ctx context.Context, verb string, arguments ...string) error {
	source, err := PackagedPath()
	if err != nil {
		return err
	}
	return invokeAt(ctx, source, verb, arguments...)
}

func invokeAt(ctx context.Context, appPath, verb string, arguments ...string) error {
	executable := filepath.Join(appPath, "Contents", "MacOS", helperclient.ExecutableName)
	info, err := os.Lstat(executable)
	if err != nil {
		return fmt.Errorf("cc-notes package: locate signed helper: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("cc-notes package: signed helper is not a real executable file")
	}
	//nolint:gosec // G204: the bundle's own runtime binary, just proved a real
	// regular file; verb is a constant and each argument is one argv element.
	command := exec.CommandContext(ctx, executable, append([]string{verb}, arguments...)...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("cc-notes package: signed helper %s: %w", verb, err)
	}
	return nil
}
