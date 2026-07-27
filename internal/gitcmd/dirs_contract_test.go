package gitcmd_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
)

// TestDirsRepositoryLayouts pins what Dirs returns for the layouts TestDirs
// does not reach: a bare repository, a submodule checkout, and a repository
// reached through a symlinked alias, alongside the linked worktree. The
// assertions compare the returned paths verbatim rather than through
// filepath.EvalSymlinks, because the difference EvalSymlinks would hide — Git
// answering --absolute-git-dir physically while Dirs joins a relative
// --git-common-dir onto the caller's lexical directory — is the contract a
// consumer that hashes the common directory has to know about.
func TestDirsRepositoryLayouts(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()

	normal := filepath.Join(root, "normal")
	initDirsContractRepo(t, normal)
	gittest.Git(t, normal, "commit", "-q", "--allow-empty", "-m", "base")

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

	physicalNormalCommon := filepath.Join(evalDirsContractPath(t, normal), ".git")
	physicalSubmoduleCommon := filepath.Join(evalDirsContractPath(t, super), ".git", "modules", "module")

	cases := []struct {
		name          string
		dir           string
		wantGitDir    string
		wantCommonDir string
	}{
		{
			name:          "normal non-bare repo",
			dir:           normal,
			wantGitDir:    physicalNormalCommon,
			wantCommonDir: filepath.Join(normal, ".git"),
		},
		{
			name:          "linked worktree",
			dir:           linked,
			wantGitDir:    filepath.Join(physicalNormalCommon, "worktrees", "linked"),
			wantCommonDir: physicalNormalCommon,
		},
		{
			name:          "bare repo",
			dir:           bare,
			wantGitDir:    evalDirsContractPath(t, bare),
			wantCommonDir: bare,
		},
		{
			name:          "submodule checkout",
			dir:           submodule,
			wantGitDir:    physicalSubmoduleCommon,
			wantCommonDir: physicalSubmoduleCommon,
		},
		{
			name:          "symlinked repo path",
			dir:           alias,
			wantGitDir:    physicalNormalCommon,
			wantCommonDir: filepath.Join(alias, ".git"),
		},
	}

	commonDirs := make(map[string]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitDir, commonDir, err := (gitcmd.Git{Dir: tc.dir}).Dirs(t.Context())
			if err != nil {
				t.Fatalf("Dirs(%q): %v", tc.dir, err)
			}
			if gitDir != tc.wantGitDir {
				t.Fatalf("git dir: got %q, want %q", gitDir, tc.wantGitDir)
			}
			if commonDir != tc.wantCommonDir {
				t.Fatalf("common dir: got %q, want %q", commonDir, tc.wantCommonDir)
			}
			if !filepath.IsAbs(commonDir) {
				t.Fatalf("common dir %q is not absolute", commonDir)
			}
			commonDirs[tc.name] = commonDir
		})
	}

	if commonDirs["normal non-bare repo"] == commonDirs["symlinked repo path"] {
		t.Fatalf("Dirs resolved the symlink alias to %q; it must not", commonDirs["normal non-bare repo"])
	}
	if got, want := evalDirsContractPath(t, commonDirs["symlinked repo path"]), evalDirsContractPath(t, commonDirs["normal non-bare repo"]); got != want {
		t.Fatalf("resolved alias common dir %q, want %q", got, want)
	}
	if got, want := evalDirsContractPath(t, commonDirs["linked worktree"]), evalDirsContractPath(t, commonDirs["normal non-bare repo"]); got != want {
		t.Fatalf("resolved linked-worktree common dir %q, want %q", got, want)
	}
	for _, pair := range [][2]string{
		{"normal non-bare repo", "bare repo"},
		{"normal non-bare repo", "submodule checkout"},
		{"bare repo", "submodule checkout"},
	} {
		if evalDirsContractPath(t, commonDirs[pair[0]]) == evalDirsContractPath(t, commonDirs[pair[1]]) {
			t.Fatalf("%s and %s share a common dir: %q", pair[0], pair[1], commonDirs[pair[0]])
		}
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
