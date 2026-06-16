package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/model"
)

// initGitRepo creates a git repository in a temp dir with global/system config
// pinned to /dev/null so only local config affects lookups.
func initGitRepo(t *testing.T) gitcmd.Git {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return gitcmd.Git{Dir: dir}
}

func TestLeaseTTL(t *testing.T) {
	t.Run("default 1h", func(t *testing.T) {
		os.Unsetenv(leaseTTLEnv)
		g := initGitRepo(t)
		got, err := leaseTTL(t.Context(), g)
		if err != nil {
			t.Fatalf("leaseTTL: %v", err)
		}
		if got != time.Hour {
			t.Fatalf("leaseTTL = %v, want 1h", got)
		}
	})
	t.Run("config fallback", func(t *testing.T) {
		os.Unsetenv(leaseTTLEnv)
		g := initGitRepo(t)
		if err := g.ConfigSet(t.Context(), leaseTTLConfig, "3h"); err != nil {
			t.Fatalf("config set: %v", err)
		}
		got, err := leaseTTL(t.Context(), g)
		if err != nil {
			t.Fatalf("leaseTTL: %v", err)
		}
		if got != 3*time.Hour {
			t.Fatalf("leaseTTL = %v, want 3h", got)
		}
	})
	t.Run("env overrides config", func(t *testing.T) {
		g := initGitRepo(t)
		if err := g.ConfigSet(t.Context(), leaseTTLConfig, "3h"); err != nil {
			t.Fatalf("config set: %v", err)
		}
		t.Setenv(leaseTTLEnv, "90m")
		got, err := leaseTTL(t.Context(), g)
		if err != nil {
			t.Fatalf("leaseTTL: %v", err)
		}
		if got != 90*time.Minute {
			t.Fatalf("leaseTTL = %v, want 90m", got)
		}
	})
}

func TestTaskHeartbeat(t *testing.T) {
	cases := []struct {
		name      string
		heartbeat int64
		started   int64
		updated   int64
		want      int64
	}{
		{"heartbeat set", 500, 100, 200, 500},
		{"never claimed", 0, 300, 400, 0},
		// HeartbeatAt is the lease heartbeat, independent of StartedAt/UpdatedAt:
		// an assignee-less in-progress task (HeartbeatAt 0) reads as never-renewed
		// even when UpdatedAt is recent.
		{"ignores updated", 100, 999, 999, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := taskHeartbeat(model.Task{HeartbeatAt: tc.heartbeat, StartedAt: tc.started, UpdatedAt: tc.updated})
			if got != tc.want {
				t.Fatalf("taskHeartbeat = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestIsStale(t *testing.T) {
	now := time.Unix(10_000, 0)
	ttl := time.Hour
	cases := []struct {
		name   string
		status model.Status
		hb     int64
		want   bool
	}{
		{"in_progress idle beyond ttl", model.StatusInProgress, now.Add(-2 * time.Hour).Unix(), true},
		{"in_progress idle within ttl", model.StatusInProgress, now.Add(-30 * time.Minute).Unix(), false},
		{"in_progress exactly at ttl", model.StatusInProgress, now.Add(-time.Hour).Unix(), false},
		{"open never stale", model.StatusOpen, now.Add(-100 * time.Hour).Unix(), false},
		{"done never stale", model.StatusDone, now.Add(-100 * time.Hour).Unix(), false},
		{"cancelled never stale", model.StatusCancelled, now.Add(-100 * time.Hour).Unix(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// UpdatedAt is set fresh to prove isStale keys off HeartbeatAt, not it.
			task := model.Task{Status: tc.status, HeartbeatAt: tc.hb, UpdatedAt: now.Unix()}
			if got := isStale(task, now, ttl); got != tc.want {
				t.Fatalf("isStale = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsArchived(t *testing.T) {
	cutoff := time.Unix(10_000, 0)
	cases := []struct {
		name     string
		status   model.Status
		closedAt int64
		want     bool
	}{
		{"done closed before cutoff", model.StatusDone, cutoff.Add(-time.Hour).Unix(), true},
		{"cancelled closed before cutoff", model.StatusCancelled, cutoff.Add(-time.Hour).Unix(), true},
		{"done closed after cutoff", model.StatusDone, cutoff.Add(time.Hour).Unix(), false},
		{"done closed exactly at cutoff", model.StatusDone, cutoff.Unix(), false},
		{"done with no closed time", model.StatusDone, 0, false},
		{"open never archived", model.StatusOpen, cutoff.Add(-time.Hour).Unix(), false},
		{"in_progress never archived", model.StatusInProgress, cutoff.Add(-time.Hour).Unix(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := model.Task{Status: tc.status, ClosedAt: tc.closedAt}
			if got := isArchived(task, cutoff); got != tc.want {
				t.Fatalf("isArchived = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	t.Run("relative duration", func(t *testing.T) {
		got, err := parseWhen("2h", now)
		if err != nil {
			t.Fatalf("parseWhen: %v", err)
		}
		if want := now.Add(-2 * time.Hour); !got.Equal(want) {
			t.Fatalf("parseWhen = %v, want %v", got, want)
		}
	})
	t.Run("absolute rfc3339", func(t *testing.T) {
		got, err := parseWhen("2026-01-02T03:04:05Z", now)
		if err != nil {
			t.Fatalf("parseWhen: %v", err)
		}
		want, _ := time.Parse(time.RFC3339, "2026-01-02T03:04:05Z")
		if !got.Equal(want) {
			t.Fatalf("parseWhen = %v, want %v", got, want)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		if _, err := parseWhen("not-a-time", now); err == nil {
			t.Fatal("parseWhen want error, got nil")
		}
	})
}

func TestFormatIdle(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{2*time.Hour + 13*time.Minute, "idle 2h13m"},
		{3 * time.Hour, "idle 3h"},
		{45 * time.Minute, "idle 45m"},
		{90 * time.Minute, "idle 1h30m"},
		{30 * time.Second, "idle 0m"},
		{0, "idle 0m"},
	}
	for _, tc := range cases {
		if got := formatIdle(tc.in); got != tc.want {
			t.Errorf("formatIdle(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
