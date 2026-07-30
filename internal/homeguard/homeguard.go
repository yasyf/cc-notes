// Package homeguard keeps a test binary out of the developer's real home: its
// package init redirects CC_NOTES_HOME, DAEMONKIT_HOME, and HOME to fresh
// per-process temp directories, and the Main/MainWith entrypoints bracket the
// run with a content digest of the real home's cc-notes footprint. It is
// separate from the gittest fixtures so importing those stays side-effect
// free: only a package declaring an entrypoint pays for — and cleans up — a
// redirect root.
package homeguard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	launchAgentGlob = "com.yasyf.cc-notes.*.plist"
	stableProgram   = "cc-notes"
	unreadable      = "unreadable"
)

const footprintRemedy = `This guard is a smoke alarm, not a stack trace. It watches the whole process,
so it cannot name the test that wrote; with package test binaries running in
parallel it cannot even name the package, and a concurrent session running a
cc-notes command outside the suite is an equally consistent explanation — on a
busy machine that happens several times an hour. A kgsnap run writing under
~/.cc-notes/eval-snapshots is the known repeat offender, and because every
guarded package watches the same tree, one burst reds all of them at once. A
path reported as unreadable could not be digested at all, so its content went
unwatched for that snapshot.
What the guard does say is that something resolved a per-user root without
honoring CC_NOTES_HOME, DAEMONKIT_HOME, or HOME. Re-run this package alone to
see whether it is the writer, then find the call site.`

var realHome = os.Getenv("HOME")

var (
	redirectRoot     string
	initialFootprint map[string]string
)

// Package init, not the entrypoint call, owns the redirect and the before
// snapshot. That ordering is guaranteed only for importers: Go initializes
// homeguard before every variable initializer and init function of a package
// that imports it — the entrypoint's own in-package test file supplies that
// edge for the package under test — while packages that do not import it
// initialize in import-path order, possibly first. The contract test's
// init-time pin, not this init, covers those: no package in the module
// derives a per-user home at package-initialization time, so code that runs
// before this init has no home to leak.
func init() {
	redirectRoot = redirectHomes()
	initialFootprint = homeFootprint()
}

func redirectHomes() string {
	root, err := os.MkdirTemp("", "cc-notes-testhome-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "homeguard: create test home: %v\n", err)
		os.Exit(1)
	}
	for _, redirect := range []struct{ key, name string }{
		{"CC_NOTES_HOME", "cc-notes"},
		{"DAEMONKIT_HOME", "daemonkit"},
		{"HOME", "home"},
	} {
		dir := filepath.Join(root, redirect.name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "homeguard: create %s: %v\n", redirect.key, err)
			os.Exit(1)
		}
		if err := os.Setenv(redirect.key, dir); err != nil {
			fmt.Fprintf(os.Stderr, "homeguard: set %s: %v\n", redirect.key, err)
			os.Exit(1)
		}
	}
	return root
}

// RealHome returns the HOME the test binary inherited, captured before this
// package's init redirected it. A test that execs the go tool passes it
// through that subprocess's Env: GOPATH, GOMODCACHE, GOCACHE, and GOENV are
// unexported HOME-derived defaults, so a subprocess inheriting the redirect
// resolves an empty module cache and an empty build cache.
func RealHome() string { return realHome }

// Main is the test entrypoint every package whose tests can reach a per-user
// state root declares, in one of the package's own test files so that
// importing homeguard orders the init-time redirect of CC_NOTES_HOME,
// DAEMONKIT_HOME, and HOME before every initializer of the package under
// test. It runs m, fails the run if the real home's cc-notes footprint
// changed since this package initialized, and exits.
func Main(m *testing.M) {
	os.Exit(MainWith(m.Run))
}

// ChildExit is the exit for a re-exec child of a guarded binary: it removes
// the redirect root this process's init created, then exits with code. A
// child inherits the parent's redirected HOME, so the footprint diff — which
// would bracket live state the parent mutates concurrently — does not apply;
// the child cleans up after its own init and nothing more.
func ChildExit(code int) {
	_ = os.RemoveAll(redirectRoot)
	os.Exit(code)
}

// MainWith is Main for an entrypoint carrying its own setup and teardown around
// the run. It refuses to run against an unwatchable home, calls run, checks
// the real home's cc-notes footprint, and returns the exit code the caller
// passes to os.Exit. The HOME check lives here rather than in init because a
// trust verifier child re-executes a guarded binary under a deliberately
// scrubbed env with no HOME, and that child exits through ChildExit without
// ever needing a home to watch.
func MainWith(run func() int) int {
	defer func() { _ = os.RemoveAll(redirectRoot) }()
	if realHome == "" || !filepath.IsAbs(realHome) {
		fmt.Fprintf(os.Stderr, "homeguard: inherited HOME %q is not a non-empty absolute path; refusing to run against an unwatchable home\n", realHome)
		return 1
	}
	code := run()
	if changes := footprintChanges(initialFootprint, homeFootprint()); len(changes) > 0 {
		fmt.Fprintf(os.Stderr, "%s\n%s\n%s\n",
			"homeguard: the real home's cc-notes footprint changed while this test binary ran:",
			strings.Join(changes, "\n"), footprintRemedy)
		return 1
	}
	return code
}

// Only .cc-notes is walked. ~/.daemonkit is a multi-gigabyte tree of six-figure
// file counts whose unrelated churn would both dominate the run and red the
// guard, so just the stable-deploy names cc-notes generates are enumerated.
// ~/.claude/settings.json — a real write target of `cc-notes setup --global` —
// is deliberately unwatched: any concurrent Claude Code session rewrites it
// several times an hour without cc-notes involvement, and each rewrite would
// red every guarded package at once, so the watch carried noise, not signal;
// the setup tests assert the --global write lands under the redirected HOME.
func homeFootprint() map[string]string {
	prints := map[string]string{}
	digestTree(prints, resolved(filepath.Join(realHome, ".cc-notes")))
	daemonkit := filepath.Join(realHome, ".daemonkit")
	paths := []string{
		filepath.Join(daemonkit, "bin", stableProgram),
		filepath.Join(daemonkit, "bin", stableProgram+".meta.json"),
		filepath.Join(daemonkit, "locks", "stable-"+stableProgram+".lock"),
	}
	agents, err := filepath.Glob(filepath.Join(realHome, "Library", "LaunchAgents", launchAgentGlob))
	if err != nil {
		panic(fmt.Sprintf("homeguard: launch agent pattern %q: %v", launchAgentGlob, err))
	}
	for _, path := range append(paths, agents...) {
		digestPath(prints, resolved(path))
	}
	return prints
}

// A chezmoi- or stow-managed home symlinks whole watch roots (~/.cc-notes
// itself, ~/.daemonkit/bin entries), and WalkDir and Lstat both stop at the
// link, leaving the target unwatched — so every watched root resolves before
// digesting.
func resolved(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return target
}

func digestTree(prints map[string]string, root string) {
	walkTree(prints, root, map[string]bool{root: true})
}

// A snapshot must never abort: an unreadable directory or a file a concurrent
// writer removed mid-walk would otherwise turn somebody else's ordinary write
// into a zero-tests-run failure with no remedy. Undigestable paths are recorded
// as unreadable so they still show up in the diff.
// A symlinked directory inside the tree is followed — relocating a state
// subtree onto another volume is ordinary user configuration, and stopping at
// the link left its whole target unwatched — deduplicated by resolved target,
// with the visited set cutting cycles. A symlinked file stays mode-only: its
// content lives in a tree the guard does not own.
func walkTree(prints map[string]string, root string, visited map[string]bool) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			if !errors.Is(err, fs.ErrNotExist) {
				prints[path] = unreadable
			}
		case entry.Type()&fs.ModeSymlink != 0:
			digestPath(prints, path)
			followDirLink(prints, path, visited)
		default:
			digestPath(prints, path)
		}
		return nil
	})
}

func followDirLink(prints map[string]string, path string, visited map[string]bool) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() || visited[target] {
		return
	}
	visited[target] = true
	walkTree(prints, target, visited)
}

func digestPath(prints map[string]string, path string) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		prints[path] = unreadable
		return
	case !info.Mode().IsRegular():
		prints[path] = info.Mode().String()
		return
	}
	//nolint:gosec // G304: path comes from a walk of the user's own home directory.
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		prints[path] = unreadable
		return
	}
	defer func() { _ = file.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		prints[path] = unreadable
		return
	}
	prints[path] = hex.EncodeToString(sum.Sum(nil))
}

func footprintChanges(before, after map[string]string) []string {
	var changes []string
	for path, sum := range after {
		switch prior, ok := before[path]; {
		case !ok:
			changes = append(changes, "  added   "+path)
		case prior != sum:
			changes = append(changes, "  changed "+path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changes = append(changes, "  removed "+path)
		}
	}
	slices.Sort(changes)
	return changes
}
