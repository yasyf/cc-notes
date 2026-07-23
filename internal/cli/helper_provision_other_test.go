//go:build !darwin && !ccnotes_test

package cli

import (
	"context"
	"strings"
	"testing"
)

func TestServicePlatformRejectsUnsupportedOS(t *testing.T) {
	for name, run := range map[string]func(context.Context) error{
		"install":   installServicePlatform,
		"uninstall": uninstallServicePlatform,
	} {
		t.Run(name, func(t *testing.T) {
			err := run(t.Context())
			if err == nil || !strings.Contains(err.Error(), "only supported on macOS") || ExitCode(err) != 1 {
				t.Fatalf("error = %v, exit = %d", err, ExitCode(err))
			}
		})
	}
}
