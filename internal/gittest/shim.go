package gittest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitShim is a git wrapper put first on PATH: when "$*" matches Pattern (an
// sh case glob) for the Nth time (every time when N is 0) it runs each
// Interleave argv through the real git first, then execs the real git with the
// original argv. Tests use it to land a concurrent write between two steps of
// one operation.
type GitShim struct {
	Pattern    string
	N          int
	Interleave [][]string
}

// Install writes the shim and prepends its directory to PATH for the test.
func (sh GitShim) Install(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	dir := t.TempDir()
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\ncase \"$*\" in\n%s)\n", sh.Pattern)
	if sh.N > 0 {
		count := quote(filepath.Join(dir, "count"))
		fmt.Fprintf(&b, "\tn=$(($(cat %s 2>/dev/null || echo 0) + 1))\n\techo \"$n\" >%s\n\t[ \"$n\" -eq %d ] || exec %s \"$@\"\n", count, count, sh.N, quote(realGit))
	}
	for _, argv := range sh.Interleave {
		b.WriteString("\t" + quote(realGit))
		for _, arg := range argv {
			b.WriteString(" " + quote(arg))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\t;;\nesac\nexec %s \"$@\"\n", quote(realGit))
	//nolint:gosec // G306: the shim must be executable for exec.LookPath to run it.
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(b.String()), 0o700); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
