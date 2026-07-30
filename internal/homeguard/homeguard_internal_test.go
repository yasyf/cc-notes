package homeguard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeRealHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved := realHome
	realHome = home
	t.Cleanup(func() { realHome = saved })
	return home
}

func TestRedirectActiveBeforeRun(t *testing.T) {
	if redirectRoot == "" {
		t.Fatal("redirectRoot is empty: package init did not redirect")
	}
	for key, name := range map[string]string{
		"CC_NOTES_HOME":  "cc-notes",
		"DAEMONKIT_HOME": "daemonkit",
		"HOME":           "home",
	} {
		want := filepath.Join(redirectRoot, name)
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if realHome == os.Getenv("HOME") {
		t.Errorf("realHome %q equals the redirected HOME: captured after the redirect", realHome)
	}
}

func TestMainRefusesUnusableHome(t *testing.T) {
	cases := []struct {
		name string
		home []string
	}{
		{"unset", nil},
		{"relative", []string{"HOME=relative/home"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			//nolint:gosec // G204: re-exec of this test binary itself.
			cmd := exec.Command(os.Args[0], "-test.run", "^$")
			env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
				return strings.HasPrefix(kv, "HOME=")
			})
			cmd.Env = append(env, tc.home...)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("re-exec with %s HOME: err %v, output:\n%s", tc.name, err, out)
			}
			if !strings.Contains(string(out), "homeguard: inherited HOME") {
				t.Errorf("re-exec output does not name the HOME refusal:\n%s", out)
			}
		})
	}
}

func TestHomeFootprintWatchSet(t *testing.T) {
	home := fakeRealHome(t)

	cases := []struct {
		name    string
		path    string
		watched bool
	}{
		{"cc-notes tree", filepath.Join(".cc-notes", "repos", "key", "repo.json"), true},
		{"cc-notes eval snapshots", filepath.Join(".cc-notes", "eval-snapshots", "run.json"), true},
		{"claude settings", filepath.Join(".claude", "settings.json"), false},
		{"daemonkit stable binary", filepath.Join(".daemonkit", "bin", stableProgram), true},
		{"daemonkit stable metadata", filepath.Join(".daemonkit", "bin", stableProgram+".meta.json"), true},
		{"daemonkit stable lock", filepath.Join(".daemonkit", "locks", "stable-"+stableProgram+".lock"), true},
		{"daemonkit foreign subtree", filepath.Join(".daemonkit", "tools", "other-product"), false},
		{"cc-notes launch agent", filepath.Join("Library", "LaunchAgents", "com.yasyf.cc-notes.daemon.plist"), true},
		{"foreign launch agent", filepath.Join("Library", "LaunchAgents", "com.example.other.plist"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(home, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.name), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, watched := homeFootprint()[path]; watched != tc.watched {
				t.Errorf("homeFootprint watches %s = %v, want %v", tc.path, watched, tc.watched)
			}
		})
	}
}

func TestHomeFootprintFollowsSymlinkedRoots(t *testing.T) {
	t.Run("cc-notes tree", func(t *testing.T) {
		home := fakeRealHome(t)
		target, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(home, ".cc-notes")); err != nil {
			t.Fatal(err)
		}
		before := homeFootprint()
		marker := filepath.Join(target, "evil-init", "marker")
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("evil"), 0o600); err != nil {
			t.Fatal(err)
		}
		want := []string{"  added   " + filepath.Dir(marker), "  added   " + marker}
		if got := footprintChanges(before, homeFootprint()); !slices.Equal(got, want) {
			t.Errorf("footprintChanges = %q, want %q", got, want)
		}
	})

	t.Run("daemonkit stable binary", func(t *testing.T) {
		home := fakeRealHome(t)
		outside, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "cc-notes-release")
		if err := os.WriteFile(target, []byte("v1"), 0o700); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(home, ".daemonkit", "bin")
		if err := os.MkdirAll(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(bin, stableProgram)); err != nil {
			t.Fatal(err)
		}
		before := homeFootprint()
		if err := os.WriteFile(target, []byte("v2"), 0o700); err != nil {
			t.Fatal(err)
		}
		want := []string{"  changed " + target}
		if got := footprintChanges(before, homeFootprint()); !slices.Equal(got, want) {
			t.Errorf("footprintChanges = %q, want %q", got, want)
		}
	})
}

func TestHomeFootprintFollowsNestedSymlinks(t *testing.T) {
	ccNotes := func(t *testing.T, home string) string {
		t.Helper()
		root := filepath.Join(home, ".cc-notes")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		return root
	}

	t.Run("directory symlink is followed", func(t *testing.T) {
		home := fakeRealHome(t)
		root := ccNotes(t, home)
		target, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "repos")); err != nil {
			t.Fatal(err)
		}
		before := homeFootprint()
		marker := filepath.Join(target, "key", "repo.json")
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("moved"), 0o600); err != nil {
			t.Fatal(err)
		}
		want := []string{"  added   " + filepath.Dir(marker), "  added   " + marker}
		if got := footprintChanges(before, homeFootprint()); !slices.Equal(got, want) {
			t.Errorf("footprintChanges = %q, want %q", got, want)
		}
	})

	t.Run("directory symlink cycle terminates", func(t *testing.T) {
		home := fakeRealHome(t)
		root := ccNotes(t, home)
		if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
			t.Fatal(err)
		}
		before := homeFootprint()
		marker := filepath.Join(root, "marker")
		if err := os.WriteFile(marker, []byte("once"), 0o600); err != nil {
			t.Fatal(err)
		}
		want := []string{"  added   " + marker}
		if got := footprintChanges(before, homeFootprint()); !slices.Equal(got, want) {
			t.Errorf("footprintChanges = %q, want %q", got, want)
		}
	})

	t.Run("file symlink stays mode-only", func(t *testing.T) {
		home := fakeRealHome(t)
		root := ccNotes(t, home)
		outside, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "blob")
		if err := os.WriteFile(target, []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "blob")); err != nil {
			t.Fatal(err)
		}
		before := homeFootprint()
		if err := os.WriteFile(target, []byte("v2"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := footprintChanges(before, homeFootprint()); len(got) != 0 {
			t.Errorf("footprintChanges = %q, want none for a symlinked file's target", got)
		}
	})
}
