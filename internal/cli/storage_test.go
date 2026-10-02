package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
)

var bindLine = regexp.MustCompile(`^(bound|already bound) (.+) to records at (.+)\n$`)

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", path, err)
	}
	return resolved
}

func assertBindLine(t *testing.T, out, wantVerb, thin, sourceCommon string) {
	t.Helper()
	m := bindLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("output %q does not match %s", out, bindLine)
	}
	if m[1] != wantVerb {
		t.Fatalf("verb = %q, want %q", m[1], wantVerb)
	}
	if got, want := canonicalPath(t, m[2]), canonicalPath(t, filepath.Join(thin, ".git")); got != want {
		t.Fatalf("context = %q, want %q", m[2], want)
	}
	if m[3] != sourceCommon {
		t.Fatalf("records = %q, want %q", m[3], sourceCommon)
	}
}

func TestStorageBindCommand(t *testing.T) {
	source := initRepo(t)
	commitFile(t, source, "README.md", "source\n")
	sourceCommon := canonicalPath(t, filepath.Join(source, ".git"))

	t.Run("binds and reports", func(t *testing.T) {
		thin := gittest.ShallowClone(t, source, 1)
		assertBindLine(t, mustRun(t, thin, "storage", "bind", "--source", source), "bound", thin, sourceCommon)
		assertBindLine(t, mustRun(t, thin, "storage", "bind", "--source", source), "already bound", thin, sourceCommon)
	})

	t.Run("--repo selects the context", func(t *testing.T) {
		thin := gittest.ShallowClone(t, source, 1)
		assertBindLine(t, mustRun(t, source, "-R", thin, "storage", "bind", "--source", source), "bound", thin, sourceCommon)
	})

	t.Run("--source is required", func(t *testing.T) {
		thin := gittest.ShallowClone(t, source, 1)
		_, _, err := runCLI(t, thin, "storage", "bind")
		if err == nil || cli.ExitCode(err) != 2 {
			t.Fatalf("storage bind without --source: err %v, exit %d; want a usage error", err, cli.ExitCode(err))
		}
	})

	t.Run("--source must be a directory", func(t *testing.T) {
		thin := gittest.ShallowClone(t, source, 1)
		_, _, err := runCLI(t, thin, "storage", "bind", "--source", filepath.Join(thin, "nope"))
		var usage *cli.UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("storage bind --source <missing> = %v, want *UsageError", err)
		}
	})

	t.Run("bind takes no positional arguments", func(t *testing.T) {
		thin := gittest.ShallowClone(t, source, 1)
		_, _, err := runCLI(t, thin, "storage", "bind", source, "--source", source)
		if cli.ExitCode(err) != 2 {
			t.Fatalf("storage bind with a positional: err %v, exit %d; want 2", err, cli.ExitCode(err))
		}
	})

	t.Run("a refusal propagates the sentinel", func(t *testing.T) {
		thin := gittest.ShallowClone(t, source, 1)
		gittest.Git(t, thin, "update-ref", "refs/cc-notes/notes/x", "HEAD")
		stdout, _, err := runCLI(t, thin, "storage", "bind", "--source", source)
		if !errors.Is(err, store.ErrContextHasRecords) {
			t.Fatalf("storage bind on a context with records = %v, want ErrContextHasRecords", err)
		}
		var be *store.BindingError
		if !errors.As(err, &be) {
			t.Fatalf("error %T is not a *store.BindingError", err)
		}
		if cli.ExitCode(err) != 1 {
			t.Fatalf("exit %d, want 1", cli.ExitCode(err))
		}
		if stdout != "" {
			t.Fatalf("refused bind printed %q", stdout)
		}
		//nolint:gosec // G304: reads the test repository's own config.
		config, err := os.ReadFile(filepath.Join(thin, ".git", "config"))
		if err != nil {
			t.Fatalf("read thin config: %v", err)
		}
		if strings.Contains(string(config), "[cc-notes]") {
			t.Fatalf("refused bind published into the context config:\n%s", config)
		}
	})

	t.Run("the group prints help and rejects unknown verbs", func(t *testing.T) {
		thin := gittest.ShallowClone(t, source, 1)
		out := mustRun(t, thin, "storage")
		if !strings.Contains(out, "bind") {
			t.Fatalf("storage help %q does not list bind", out)
		}
		_, _, err := runCLI(t, thin, "storage", "unbind")
		if cli.ExitCode(err) != 2 {
			t.Fatalf("storage unbind: err %v, exit %d; want 2", err, cli.ExitCode(err))
		}
	})
}
