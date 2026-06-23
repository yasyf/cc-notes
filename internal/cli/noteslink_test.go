package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func countExactLines(t *testing.T, path, line string) int {
	t.Helper()
	n := 0
	for _, l := range strings.Split(readFileString(t, path), "\n") {
		if l == line {
			n++
		}
	}
	return n
}

func assertSymlink(t *testing.T, link, want string) {
	t.Helper()
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}
	if got != want {
		t.Errorf("%s -> %q, want %q", link, got, want)
	}
}

func TestAddNotesExclude(t *testing.T) {
	t.Run("appends and is idempotent", func(t *testing.T) {
		g := initGitRepo(t)
		exclude := filepath.Join(g.Dir, ".git", "info", "exclude")

		if err := addNotesExclude(t.Context(), g); err != nil {
			t.Fatalf("addNotesExclude: %v", err)
		}
		if n := countExactLines(t, exclude, "/.notes"); n != 1 {
			t.Fatalf("/.notes line count = %d, want 1", n)
		}
		first := readFileString(t, exclude)

		if err := addNotesExclude(t.Context(), g); err != nil {
			t.Fatalf("addNotesExclude (2nd): %v", err)
		}
		if got := readFileString(t, exclude); got != first {
			t.Errorf("second call changed exclude:\n%q\n->\n%q", first, got)
		}
	})

	t.Run("normalizes missing trailing newline and preserves content", func(t *testing.T) {
		g := initGitRepo(t)
		exclude := filepath.Join(g.Dir, ".git", "info", "exclude")
		if err := os.WriteFile(exclude, []byte("build/\n*.log"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := addNotesExclude(t.Context(), g); err != nil {
			t.Fatalf("addNotesExclude: %v", err)
		}
		const want = "build/\n*.log\n/.notes\n"
		if got := readFileString(t, exclude); got != want {
			t.Errorf("exclude = %q, want %q", got, want)
		}
	})

	t.Run("no-op when already present", func(t *testing.T) {
		g := initGitRepo(t)
		exclude := filepath.Join(g.Dir, ".git", "info", "exclude")
		if err := os.WriteFile(exclude, []byte("# header\n/.notes\nkeep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := addNotesExclude(t.Context(), g); err != nil {
			t.Fatalf("addNotesExclude: %v", err)
		}
		const want = "# header\n/.notes\nkeep\n"
		if got := readFileString(t, exclude); got != want {
			t.Errorf("exclude = %q, want unchanged %q", got, want)
		}
	})
}

func TestLinkNotes(t *testing.T) {
	t.Run("creates when absent", func(t *testing.T) {
		repo := t.TempDir()
		mp := filepath.Join(t.TempDir(), "mnt")
		if err := linkNotes(repo, mp); err != nil {
			t.Fatalf("linkNotes: %v", err)
		}
		assertSymlink(t, notesLinkPath(repo), mp)
	})

	t.Run("repoints an existing symlink", func(t *testing.T) {
		repo := t.TempDir()
		oldTarget := filepath.Join(t.TempDir(), "old")
		mp := filepath.Join(t.TempDir(), "new")
		if err := os.Symlink(oldTarget, notesLinkPath(repo)); err != nil {
			t.Fatal(err)
		}
		if err := linkNotes(repo, mp); err != nil {
			t.Fatalf("linkNotes: %v", err)
		}
		assertSymlink(t, notesLinkPath(repo), mp)
	})

	t.Run("repoints a dangling symlink", func(t *testing.T) {
		repo := t.TempDir()
		mp := filepath.Join(t.TempDir(), "mnt")
		if err := os.Symlink(filepath.Join(repo, "gone"), notesLinkPath(repo)); err != nil {
			t.Fatal(err)
		}
		if err := linkNotes(repo, mp); err != nil {
			t.Fatalf("linkNotes: %v", err)
		}
		assertSymlink(t, notesLinkPath(repo), mp)
	})

	t.Run("errors on a real directory and leaves it", func(t *testing.T) {
		repo := t.TempDir()
		link := notesLinkPath(repo)
		if err := os.Mkdir(link, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := linkNotes(repo, filepath.Join(t.TempDir(), "mnt")); err == nil {
			t.Fatal("linkNotes on a real dir succeeded, want error")
		}
		if info, err := os.Lstat(link); err != nil || !info.IsDir() {
			t.Errorf("real .notes dir disturbed: info=%v err=%v", info, err)
		}
	})

	t.Run("errors on a real file and leaves it", func(t *testing.T) {
		repo := t.TempDir()
		link := notesLinkPath(repo)
		if err := os.WriteFile(link, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := linkNotes(repo, filepath.Join(t.TempDir(), "mnt")); err == nil {
			t.Fatal("linkNotes on a real file succeeded, want error")
		}
		if got := readFileString(t, link); got != "keep" {
			t.Errorf("real .notes file changed to %q", got)
		}
	})
}

func TestUnlinkNotes(t *testing.T) {
	t.Run("removes a symlink pointing at the mountpoint", func(t *testing.T) {
		repo := t.TempDir()
		mp := filepath.Join(t.TempDir(), "mnt")
		if err := os.Symlink(mp, notesLinkPath(repo)); err != nil {
			t.Fatal(err)
		}
		if err := unlinkNotes(repo, mp); err != nil {
			t.Fatalf("unlinkNotes: %v", err)
		}
		if _, err := os.Lstat(notesLinkPath(repo)); !os.IsNotExist(err) {
			t.Errorf(".notes still present after unlink: err=%v", err)
		}
	})

	t.Run("leaves a symlink pointing elsewhere", func(t *testing.T) {
		repo := t.TempDir()
		other := filepath.Join(t.TempDir(), "other")
		if err := os.Symlink(other, notesLinkPath(repo)); err != nil {
			t.Fatal(err)
		}
		if err := unlinkNotes(repo, filepath.Join(t.TempDir(), "mnt")); err != nil {
			t.Fatalf("unlinkNotes: %v", err)
		}
		assertSymlink(t, notesLinkPath(repo), other)
	})

	t.Run("leaves a real file", func(t *testing.T) {
		repo := t.TempDir()
		link := notesLinkPath(repo)
		if err := os.WriteFile(link, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := unlinkNotes(repo, link); err != nil {
			t.Fatalf("unlinkNotes: %v", err)
		}
		if got := readFileString(t, link); got != "keep" {
			t.Errorf("real .notes file changed/removed: %q", got)
		}
	})

	t.Run("no-op when absent", func(t *testing.T) {
		repo := t.TempDir()
		if err := unlinkNotes(repo, filepath.Join(t.TempDir(), "mnt")); err != nil {
			t.Errorf("unlinkNotes on a missing link: %v", err)
		}
	})
}
