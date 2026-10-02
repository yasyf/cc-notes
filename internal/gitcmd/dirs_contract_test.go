package gitcmd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
)

// TestDirsRepositoryLayouts pins the physical directories Dirs returns for a
// bare repository, a submodule, a linked worktree, and a repository reached
// through a symlinked alias or a symlink to one of its subdirectories.
func TestDirsRepositoryLayouts(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()

	normal := filepath.Join(root, "normal")
	initDirsContractRepo(t, normal)
	gittest.Git(t, normal, "commit", "-q", "--allow-empty", "-m", "base")
	if err := os.Mkdir(filepath.Join(normal, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir subdirectory: %v", err)
	}

	linked := filepath.Join(root, "linked")
	gittest.Git(t, normal, "worktree", "add", "-q", "-b", "dirs-linked", linked)

	bare := filepath.Join(root, "bare.git")
	if err := os.Mkdir(bare, 0o750); err != nil {
		t.Fatalf("mkdir bare repo: %v", err)
	}
	gittest.Git(t, bare, "init", "-q", "--bare")

	source := filepath.Join(root, "source")
	initDirsContractRepo(t, source)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "base")

	super := filepath.Join(root, "super")
	initDirsContractRepo(t, super)
	gittest.Git(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "module")
	submodule := filepath.Join(super, "module")

	alias := filepath.Join(root, "normal-alias")
	if err := os.Symlink(normal, alias); err != nil {
		t.Fatalf("symlink repo: %v", err)
	}
	subAlias := filepath.Join(root, "normal-sub-alias")
	if err := os.Symlink(filepath.Join(normal, "sub"), subAlias); err != nil {
		t.Fatalf("symlink subdirectory: %v", err)
	}

	physicalNormalCommon := filepath.Join(evalDirsContractPath(t, normal), ".git")
	physicalSubmoduleCommon := filepath.Join(evalDirsContractPath(t, super), ".git", "modules", "module")
	physicalBare := evalDirsContractPath(t, bare)

	cases := []struct {
		name          string
		dir           string
		wantGitDir    string
		wantCommonDir string
	}{
		{"normal non-bare repo", normal, physicalNormalCommon, physicalNormalCommon},
		{"linked worktree", linked, filepath.Join(physicalNormalCommon, "worktrees", "linked"), physicalNormalCommon},
		{"bare repo", bare, physicalBare, physicalBare},
		{"submodule checkout", submodule, physicalSubmoduleCommon, physicalSubmoduleCommon},
		{"symlinked repo path", alias, physicalNormalCommon, physicalNormalCommon},
		{"symlinked subdirectory", subAlias, physicalNormalCommon, physicalNormalCommon},
	}

	commonDirs := make(map[string]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitDir, commonDir, _, err := (gitcmd.Git{Dir: tc.dir}).Dirs(t.Context())
			if err != nil {
				t.Fatalf("Dirs(%q): %v", tc.dir, err)
			}
			if gitDir != tc.wantGitDir {
				t.Fatalf("git dir: got %q, want %q", gitDir, tc.wantGitDir)
			}
			if commonDir != tc.wantCommonDir {
				t.Fatalf("common dir: got %q, want %q", commonDir, tc.wantCommonDir)
			}
			commonDirs[tc.name] = commonDir
		})
	}

	for _, name := range []string{"symlinked repo path", "symlinked subdirectory", "linked worktree"} {
		if commonDirs[name] != commonDirs["normal non-bare repo"] {
			t.Fatalf("%s common dir %q, want the main checkout's %q", name, commonDirs[name], commonDirs["normal non-bare repo"])
		}
	}
	for _, pair := range [][2]string{
		{"normal non-bare repo", "bare repo"},
		{"normal non-bare repo", "submodule checkout"},
		{"bare repo", "submodule checkout"},
	} {
		if commonDirs[pair[0]] == commonDirs[pair[1]] {
			t.Fatalf("%s and %s share a common dir: %q", pair[0], pair[1], commonDirs[pair[0]])
		}
	}

	// A Backend handle on a common directory answers with that directory
	// itself: records stores open the backend at its common dir, never through
	// a worktree, and sourceindex joins CommonDir back onto Dir.
	for _, name := range []string{"normal non-bare repo", "linked worktree", "bare repo"} {
		t.Run("backend on "+name, func(t *testing.T) {
			common := commonDirs[name]
			backend := gitcmd.Backend(common)
			gitDir, commonDir, _, err := backend.Dirs(t.Context())
			if err != nil {
				t.Fatalf("Backend(%q).Dirs: %v", common, err)
			}
			if evalDirsContractPath(t, gitDir) != evalDirsContractPath(t, common) {
				t.Fatalf("backend git dir: got %q, want %q", gitDir, common)
			}
			if commonDir != common {
				t.Fatalf("backend common dir: got %q, want %q", commonDir, common)
			}
			got, err := backend.CommonDir(t.Context())
			if err != nil {
				t.Fatalf("Backend(%q).CommonDir: %v", common, err)
			}
			if got != common {
				t.Fatalf("backend CommonDir(): got %q, want %q", got, common)
			}
			first, err := backend.FirstRef(t.Context(), "refs/cc-notes/")
			if err != nil {
				t.Fatalf("Backend(%q).FirstRef: %v", common, err)
			}
			if first != "" {
				t.Fatalf("backend FirstRef on an empty namespace = %q, want \"\"", first)
			}
		})
	}
	backend := gitcmd.Backend(commonDirs["normal non-bare repo"])
	gittest.Git(t, normal, "update-ref", "refs/cc-notes/notes/b", "HEAD")
	gittest.Git(t, normal, "update-ref", "refs/cc-notes/notes/a", "HEAD")
	first, err := backend.FirstRef(t.Context(), "refs/cc-notes/")
	if err != nil {
		t.Fatalf("backend FirstRef: %v", err)
	}
	if first != "refs/cc-notes/notes/a" {
		t.Fatalf("backend FirstRef = %q, want refs/cc-notes/notes/a", first)
	}
}

func initDirsContractRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "config", "user.name", "Test User")
	gittest.Git(t, dir, "config", "user.email", "test@example.com")
}

func evalDirsContractPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", path, err)
	}
	return resolved
}
