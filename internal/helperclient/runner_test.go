package helperclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/bundle"
	"github.com/yasyf/daemonkit/fetch"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"
)

type recordingHelperFetcher struct {
	installation fetch.Installation
	err          error
	calls        int
	config       fetch.Config
}

func (f *recordingHelperFetcher) Fetch(_ context.Context, config fetch.Config) (fetch.Installation, error) {
	f.calls++
	f.config = config
	return f.installation, f.err
}

func TestToolRunnerExecutesAndSettlesOneTask(t *testing.T) {
	runner, err := NewToolRunner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), supervise.Task{
		RecoveryClass: proc.RecoveryTask, Path: "/usr/bin/true",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := runner.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestHelperReleasePinsExactVersionURLAndDigest(t *testing.T) {
	setHelperRelease(t, "v0.39.3", "0.39.3", strings.Repeat("ab", 32))
	release, err := helperRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.39.3" {
		t.Fatalf("version = %q", release.Version)
	}
	if release.URL != "https://github.com/yasyf/cc-notes/releases/download/v0.39.3/cc-notes-helper-v0.39.3-darwin.zip" {
		t.Fatalf("asset URL = %q", release.URL)
	}
	if release.SHA256.String() != strings.Repeat("ab", 32) {
		t.Fatalf("sha256 = %q", release.SHA256.String())
	}
}

func TestHelperReleaseMatchesPrereleaseBundleMarketingVersion(t *testing.T) {
	setHelperRelease(t, "v0.39.3-rc.1", "0.39.3", strings.Repeat("ab", 32))
	release, err := helperRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.39.3" {
		t.Fatalf("version = %q", release.Version)
	}
	if release.URL != "https://github.com/yasyf/cc-notes/releases/download/v0.39.3-rc.1/cc-notes-helper-v0.39.3-rc.1-darwin.zip" {
		t.Fatalf("asset URL = %q", release.URL)
	}
}

func TestHelperReleaseRejectsMissingDigest(t *testing.T) {
	setHelperRelease(t, "v0.39.3", "0.39.3", "")
	if _, err := helperRelease(); err == nil {
		t.Fatal("helperRelease succeeded without an exact digest")
	}
}

func TestHelperReleaseRejectsInvalidBundleVersion(t *testing.T) {
	for _, value := range []string{"", " 0.39.3"} {
		t.Run(value, func(t *testing.T) {
			setHelperRelease(t, "v0.39.3", value, strings.Repeat("ab", 32))
			if _, err := helperRelease(); err == nil {
				t.Fatalf("helperRelease succeeded with version %q", value)
			}
		})
	}
}

func TestReconcileHelperCallsFetcherWhenExecutableAlreadyExists(t *testing.T) {
	setHelperRelease(t, "v0.39.3", "0.39.3", strings.Repeat("cd", 32))
	t.Setenv("HOME", t.TempDir())
	dir, err := InstalledDir()
	if err != nil {
		t.Fatal(err)
	}
	appPath := bundle.AppPath(dir, ExecutableName)
	executable := bundle.ExePath(appPath, ExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("stale"), 0o750); err != nil {
		t.Fatal(err)
	}
	release, err := helperRelease()
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &recordingHelperFetcher{installation: fetch.Installation{Path: appPath, Release: release}}
	got, err := reconcileHelper(t.Context(), fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls)
	}
	if got != executable {
		t.Fatalf("executable = %q, want %q", got, executable)
	}
	if fetcher.config.Release != release || fetcher.config.Dir != dir || fetcher.config.AppName != ExecutableName {
		t.Fatalf("fetch config = %+v", fetcher.config)
	}
	if fetcher.config.Identity.TeamID != TeamID || fetcher.config.Identity.SigningIdentifier != BundleID {
		t.Fatalf("identity = %+v", fetcher.config.Identity)
	}
}

func TestReconcileHelperCreatesPrivateUserApplicationsDirectory(t *testing.T) {
	setHelperRelease(t, "v0.39.3", "0.39.3", strings.Repeat("cd", 32))
	home := t.TempDir()
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	dir, err := InstalledDir()
	if err != nil {
		t.Fatal(err)
	}
	appPath := bundle.AppPath(dir, ExecutableName)
	fetcher := &recordingHelperFetcher{installation: fetch.Installation{Path: appPath}}
	if _, err := reconcileHelper(t.Context(), fetcher); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatalf("install directory %q mode = %v", dir, info.Mode())
	}
	homeInfo, err := os.Lstat(home)
	if err != nil {
		t.Fatal(err)
	}
	if homeInfo.Mode().Perm() != 0o755 {
		t.Fatalf("home permissions changed to %v", homeInfo.Mode().Perm())
	}
	if fetcher.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls)
	}
}

func TestReconcileHelperRejectsSymlinkedApplicationsDirectory(t *testing.T) {
	setHelperRelease(t, "v0.39.3", "0.39.3", strings.Repeat("cd", 32))
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Symlink(t.TempDir(), filepath.Join(home, "Applications")); err != nil {
		t.Fatal(err)
	}
	fetcher := &recordingHelperFetcher{}
	if _, err := reconcileHelper(t.Context(), fetcher); err == nil {
		t.Fatal("reconcileHelper accepted a symlinked Applications directory")
	}
	if fetcher.calls != 0 {
		t.Fatalf("fetch calls = %d, want 0", fetcher.calls)
	}
}

func TestReconcileHelperRejectsSymlinkedHome(t *testing.T) {
	setHelperRelease(t, "v0.39.3", "0.39.3", strings.Repeat("cd", 32))
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Symlink(t.TempDir(), home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	fetcher := &recordingHelperFetcher{}
	if _, err := reconcileHelper(t.Context(), fetcher); err == nil {
		t.Fatal("reconcileHelper accepted a symlinked home directory")
	}
	if fetcher.calls != 0 {
		t.Fatalf("fetch calls = %d, want 0", fetcher.calls)
	}
}

func TestReconcileHelperReturnsFetchFailure(t *testing.T) {
	setHelperRelease(t, "v0.39.3", "0.39.3", strings.Repeat("ef", 32))
	t.Setenv("HOME", t.TempDir())
	want := errors.New("download failed")
	fetcher := &recordingHelperFetcher{err: want}
	if _, err := reconcileHelper(t.Context(), fetcher); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if fetcher.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls)
	}
}

func setHelperRelease(t *testing.T, releaseVersion, helperVersion, digest string) {
	t.Helper()
	previousVersion, previousHelperVersion, previousDigest := version.Version, version.HelperVersion, version.HelperSHA256
	version.Version, version.HelperVersion, version.HelperSHA256 = releaseVersion, helperVersion, digest
	t.Cleanup(func() {
		version.Version, version.HelperVersion, version.HelperSHA256 = previousVersion, previousHelperVersion, previousDigest
	})
}
