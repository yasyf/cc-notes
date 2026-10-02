package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestWorkDirOf(t *testing.T) {
	rows := []struct {
		name    string
		repo    func(t *testing.T) (commonDir, want string)
		wantErr bool
	}{
		{name: "working tree beside .git", repo: func(t *testing.T) (string, string) {
			dir := gittest.InitRepo(t)
			return filepath.Join(dir, ".git"), dir
		}},
		{name: "bare", repo: func(t *testing.T) (string, string) {
			dir := gittest.InitBare(t)
			return dir, dir
		}},
		{name: "core.worktree absolute", repo: func(t *testing.T) (string, string) {
			dir, tree := gittest.InitRepo(t), t.TempDir()
			gittest.Git(t, dir, "config", "core.worktree", tree)
			return filepath.Join(dir, ".git"), tree
		}},
		{name: "core.worktree relative to the git directory", repo: func(t *testing.T) (string, string) {
			dir := gittest.InitRepo(t)
			gittest.Git(t, dir, "config", "core.worktree", "../tree")
			return filepath.Join(dir, ".git"), filepath.Join(dir, "tree")
		}},
		{name: "bare wins over core.worktree", repo: func(t *testing.T) (string, string) {
			dir := gittest.InitBare(t)
			gittest.Git(t, dir, "config", "core.worktree", t.TempDir())
			return dir, dir
		}},
		{name: "non-bare git directory not named .git", repo: func(t *testing.T) (string, string) {
			dir := gittest.InitBare(t)
			gittest.Git(t, dir, "config", "core.bare", "false")
			return dir, dir
		}},
		{name: "undecodable config", repo: func(t *testing.T) (string, string) {
			dir := gittest.InitBare(t)
			if err := os.WriteFile(filepath.Join(dir, "config"), []byte("[core\n"), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			return dir, ""
		}, wantErr: true},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			commonDir, want := tc.repo(t)
			got, err := workDirOf(commonDir)
			if (err != nil) != tc.wantErr || got != want {
				t.Fatalf("workDirOf(%s) = %q, %v; want %q (error %v)", commonDir, got, err, want, tc.wantErr)
			}
		})
	}
}
