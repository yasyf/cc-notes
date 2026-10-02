package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
)

const recordsRefspec = "refs/cc-notes/*:refs/cc-notes/*"

func gitConfigFile(t *testing.T, repo string) string {
	t.Helper()
	//nolint:gosec // G304: reads the test repository's own config.
	raw, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatalf("read %s config: %v", repo, err)
	}
	return string(raw)
}

func addRemote(t *testing.T, repo, name string) {
	t.Helper()
	gittest.Git(t, repo, "remote", "add", name, gittest.InitBare(t))
}

func configValues(t *testing.T, repo, key string) []string {
	t.Helper()
	out := gittest.Git(t, repo, "config", "--get-all", key)
	return strings.Split(out, "\n")
}

func TestAutoInstallBoundWritesRecordsConfig(t *testing.T) {
	cases := []struct {
		name       string
		bound      bool
		prewired   string
		wantRemote string
	}{
		{name: "unbound", bound: false, wantRemote: "origin"},
		{name: "bound", bound: true, wantRemote: "origin"},
		{name: "bound derives the wired remote from the records repository", bound: true, prewired: "upstream", wantRemote: "upstream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := initRepo(t)
			commitFile(t, source, "README.md", "source\n")
			addRemote(t, source, "origin")
			if tc.prewired != "" {
				addRemote(t, source, tc.prewired)
				gittest.Git(t, source, "config", "--add", "remote."+tc.prewired+".fetch", "+refs/cc-notes/*:refs/cc-notes-sync/"+tc.prewired+"/*")
			}
			checkout, wantWhere := source, ".git/config"
			if tc.bound {
				checkout = gittest.ShallowClone(t, source, 1)
				mustRun(t, checkout, "storage", "bind", "--source", source)
				wantWhere = filepath.Join(canonicalPath(t, filepath.Join(source, ".git")), "config")
			}
			checkoutBefore := gitConfigFile(t, checkout)

			_, stderr, err := runCLI(t, checkout, "note", "add", "Fact", "--body", "first")
			if err != nil {
				t.Fatalf("note add: %v (stderr %q)", err, stderr)
			}
			wantPrefix := "cc-notes: installed refspecs in " + wantWhere + " for \"" + tc.wantRemote + "\": "
			if !strings.HasPrefix(stderr, wantPrefix) {
				t.Fatalf("stderr = %q, want prefix %q", stderr, wantPrefix)
			}
			if got := configValues(t, source, "remote."+tc.wantRemote+".push"); !slices.Contains(got, recordsRefspec) {
				t.Fatalf("source remote.%s.push = %q, want it to carry %q", tc.wantRemote, got, recordsRefspec)
			}
			if tc.bound {
				if got := gitConfigFile(t, checkout); got != checkoutBefore {
					t.Fatalf("thin config changed:\n%s\nwas:\n%s", got, checkoutBefore)
				}
				if strings.Contains(gitConfigFile(t, checkout), "cc-notes-sync") {
					t.Fatalf("thin config carries a cc-notes refspec:\n%s", gitConfigFile(t, checkout))
				}
			}
			if tc.prewired != "" {
				if got := configValues(t, source, "remote.origin.fetch"); slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, "cc-notes") }) {
					t.Fatalf("source remote.origin.fetch = %q, want no cc-notes refspec for the unwired remote", got)
				}
			}

			_, stderr, err = runCLI(t, checkout, "note", "add", "Fact", "--body", "second")
			if err != nil {
				t.Fatalf("second note add: %v (stderr %q)", err, stderr)
			}
			if stderr != "" {
				t.Fatalf("second note add stderr = %q, want silence", stderr)
			}
		})
	}
}

func TestInitBoundInstallsRefspecsInRecordsRepository(t *testing.T) {
	source := initRepo(t)
	commitFile(t, source, "README.md", "source\n")
	addRemote(t, source, "origin")
	thin := gittest.ShallowClone(t, source, 1)
	mustRun(t, thin, "storage", "bind", "--source", source)
	thinBefore := gitConfigFile(t, thin)

	out := mustRun(t, thin, "init")
	if want := "initialized: refs/cc-notes/* refspecs installed for origin\n"; out != want {
		t.Fatalf("init output = %q, want %q", out, want)
	}
	if got := configValues(t, source, "remote.origin.push"); !slices.Contains(got, recordsRefspec) {
		t.Fatalf("source remote.origin.push = %q, want it to carry %q", got, recordsRefspec)
	}
	if got := gitConfigFile(t, thin); got != thinBefore {
		t.Fatalf("thin config changed:\n%s\nwas:\n%s", got, thinBefore)
	}
}

func TestAttachPruneGuardNamesRecordsConfig(t *testing.T) {
	cases := []struct {
		name  string
		bound bool
	}{
		{name: "unbound", bound: false},
		{name: "bound", bound: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := initRepo(t)
			commitFile(t, source, "README.md", "source\n")
			checkout, wantWhere := source, ".git/config"
			if tc.bound {
				checkout = gittest.ShallowClone(t, source, 1)
				mustRun(t, checkout, "storage", "bind", "--source", source)
				wantWhere = filepath.Join(canonicalPath(t, filepath.Join(source, ".git")), "config")
			}
			attachment, _ := writeAttachable(t, "one.txt", []byte("one"))

			_, stderr, err := runCLI(t, checkout, "note", "add", "Attached", "--attach", attachment)
			if err != nil {
				t.Fatalf("note add --attach: %v (stderr %q)", err, stderr)
			}
			want := "cc-notes: installed " + strings.Join(store.PruneGuardConfigs[:], " and ") + " in " + wantWhere + " (makes"
			if !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
			}
			if got := gittest.Git(t, source, "config", "--get", "lfs.pruneverifyremotealways"); got != "true" {
				t.Fatalf("source lfs.pruneverifyremotealways = %q, want true", got)
			}
			if tc.bound && strings.Contains(gitConfigFile(t, checkout), "lfs") {
				t.Fatalf("thin config carries the prune guard:\n%s", gitConfigFile(t, checkout))
			}
		})
	}
}

func TestSkillsInstallIgnoresBrokenBinding(t *testing.T) {
	cases := []struct {
		name         string
		breakBinding func(t *testing.T, source, thin string)
		want         error
	}{
		{
			name: "backend removed",
			breakBinding: func(t *testing.T, source, _ string) {
				if err := os.RemoveAll(source); err != nil {
					t.Fatalf("remove source: %v", err)
				}
			},
			want: store.ErrBackendUnavailable,
		},
		{
			name: "binding malformed",
			breakBinding: func(t *testing.T, _, thin string) {
				gittest.Git(t, thin, "config", "cc-notes.storage", "{not json")
			},
			want: store.ErrBindingMalformed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := initRepo(t)
			commitFile(t, source, "README.md", "source\n")
			thin := gittest.ShallowClone(t, source, 1)
			mustRun(t, thin, "storage", "bind", "--source", source)
			tc.breakBinding(t, source, thin)

			if _, _, err := runCLI(t, thin, "note", "list"); !errors.Is(err, tc.want) {
				t.Fatalf("note list with a broken binding = %v, want %v", err, tc.want)
			}

			mustRun(t, thin, "skills", "install")
			if _, err := os.Stat(filepath.Join(thin, ".claude", "settings.json")); err != nil {
				t.Fatalf("skills install left no settings: %v", err)
			}
			mustRun(t, thin, "workflows", "install")
			if _, err := os.Stat(filepath.Join(thin, ".github", "workflows")); err != nil {
				t.Fatalf("workflows install left no workflow directory: %v", err)
			}
		})
	}
}
