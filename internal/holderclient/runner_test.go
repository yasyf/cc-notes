package holderclient

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

type recordingHolderFetcher struct {
	installation fetch.Installation
	err          error
	calls        int
	config       fetch.Config
}

func (f *recordingHolderFetcher) Fetch(_ context.Context, config fetch.Config) (fetch.Installation, error) {
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

func TestHolderReleasePinsExactVersionURLAndDigest(t *testing.T) {
	setHolderRelease(t, "v0.39.2", "0.39.2", strings.Repeat("ab", 32))
	release, err := holderRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.39.2" {
		t.Fatalf("version = %q", release.Version)
	}
	if release.URL != "https://github.com/yasyf/cc-notes/releases/download/v0.39.2/cc-notes-holder-v0.39.2-darwin.zip" {
		t.Fatalf("asset URL = %q", release.URL)
	}
	if release.SHA256.String() != strings.Repeat("ab", 32) {
		t.Fatalf("sha256 = %q", release.SHA256.String())
	}
}

func TestHolderReleaseMatchesPrereleaseBundleMarketingVersion(t *testing.T) {
	setHolderRelease(t, "v0.39.2-rc.1", "0.39.2", strings.Repeat("ab", 32))
	release, err := holderRelease()
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.39.2" {
		t.Fatalf("version = %q", release.Version)
	}
	if release.URL != "https://github.com/yasyf/cc-notes/releases/download/v0.39.2-rc.1/cc-notes-holder-v0.39.2-rc.1-darwin.zip" {
		t.Fatalf("asset URL = %q", release.URL)
	}
}

func TestHolderReleaseRejectsMissingDigest(t *testing.T) {
	setHolderRelease(t, "v0.39.2", "0.39.2", "")
	if _, err := holderRelease(); err == nil {
		t.Fatal("holderRelease succeeded without an exact digest")
	}
}

func TestHolderReleaseRejectsInvalidBundleVersion(t *testing.T) {
	for _, value := range []string{"", " 0.39.2"} {
		t.Run(value, func(t *testing.T) {
			setHolderRelease(t, "v0.39.2", value, strings.Repeat("ab", 32))
			if _, err := holderRelease(); err == nil {
				t.Fatalf("holderRelease succeeded with version %q", value)
			}
		})
	}
}

func TestReconcileHolderCallsFetcherWhenExecutableAlreadyExists(t *testing.T) {
	setHolderRelease(t, "v0.39.2", "0.39.2", strings.Repeat("cd", 32))
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
	release, err := holderRelease()
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &recordingHolderFetcher{installation: fetch.Installation{Path: appPath, Release: release}}
	got, err := reconcileHolder(t.Context(), fetcher)
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

func TestReconcileHolderCreatesPrivateInstallDirectoryFromCleanHome(t *testing.T) {
	setHolderRelease(t, "v0.39.2", "0.39.2", strings.Repeat("cd", 32))
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := InstalledDir()
	if err != nil {
		t.Fatal(err)
	}
	appPath := bundle.AppPath(dir, ExecutableName)
	fetcher := &recordingHolderFetcher{installation: fetch.Installation{Path: appPath}}
	if _, err := reconcileHolder(t.Context(), fetcher); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".cc-notes"), dir} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			t.Fatalf("install directory %q mode = %v", path, info.Mode())
		}
	}
	if fetcher.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls)
	}
}

func TestReconcileHolderRejectsSymlinkedStateDirectory(t *testing.T) {
	setHolderRelease(t, "v0.39.2", "0.39.2", strings.Repeat("cd", 32))
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".cc-notes")); err != nil {
		t.Fatal(err)
	}
	fetcher := &recordingHolderFetcher{}
	if _, err := reconcileHolder(t.Context(), fetcher); err == nil {
		t.Fatal("reconcileHolder accepted a symlinked state directory")
	}
	if fetcher.calls != 0 {
		t.Fatalf("fetch calls = %d, want 0", fetcher.calls)
	}
}

func TestReconcileHolderReturnsFetchFailure(t *testing.T) {
	setHolderRelease(t, "v0.39.2", "0.39.2", strings.Repeat("ef", 32))
	t.Setenv("HOME", t.TempDir())
	want := errors.New("download failed")
	fetcher := &recordingHolderFetcher{err: want}
	if _, err := reconcileHolder(t.Context(), fetcher); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if fetcher.calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls)
	}
}

func setHolderRelease(t *testing.T, releaseVersion, holderVersion, digest string) {
	t.Helper()
	previousVersion, previousHolderVersion, previousDigest := version.Version, version.HolderVersion, version.HolderSHA256
	version.Version, version.HolderVersion, version.HolderSHA256 = releaseVersion, holderVersion, digest
	t.Cleanup(func() {
		version.Version, version.HolderVersion, version.HolderSHA256 = previousVersion, previousHolderVersion, previousDigest
	})
}
