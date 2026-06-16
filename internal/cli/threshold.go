package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/model"
)

const (
	defaultLeaseTTL   = time.Hour
	leaseTTLEnv       = "CC_NOTES_LEASE_TTL"
	leaseTTLConfig    = "cc-notes.leaseTTL"
	defaultArchiveAge = 720 * time.Hour // 30 days
)

// parseDuration wraps time.ParseDuration, returning a UsageError on failure so
// a malformed threshold flag exits 2.
func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, &UsageError{Err: fmt.Errorf("invalid duration %q", s)}
	}
	return d, nil
}

// leaseTTL resolves the staleness threshold with precedence
// env > git config > 1h default: CC_NOTES_LEASE_TTL overrides the last
// cc-notes.leaseTTL git config value.
func leaseTTL(ctx context.Context, g gitcmd.Git) (time.Duration, error) {
	if value, ok := os.LookupEnv(leaseTTLEnv); ok {
		return parseDuration(value)
	}
	values, err := g.ConfigGetAll(ctx, leaseTTLConfig)
	if err != nil {
		return 0, err
	}
	if len(values) > 0 {
		return parseDuration(values[len(values)-1])
	}
	return defaultLeaseTTL, nil
}

// parseWhen parses a --closed-before value: a valid Go duration is relative
// (now - d); anything else is tried as an absolute RFC3339 timestamp.
func parseWhen(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, &UsageError{Err: fmt.Errorf("invalid --closed-before %q (want a Go duration or RFC3339 timestamp)", s)}
	}
	return t, nil
}

// taskHeartbeat is the lease heartbeat: the AuthorTime of the assignee's latest
// op, as unix seconds. Zero before any claim.
func taskHeartbeat(t model.Task) int64 { return t.HeartbeatAt }

// isStale reports an in-progress task whose idle time exceeds ttl.
func isStale(t model.Task, now time.Time, ttl time.Duration) bool {
	return t.Status == model.StatusInProgress &&
		now.Sub(time.Unix(taskHeartbeat(t), 0)) > ttl
}

// isArchived reports a done or cancelled task closed strictly before cutoff.
func isArchived(t model.Task, cutoff time.Time) bool {
	return (t.Status == model.StatusDone || t.Status == model.StatusCancelled) &&
		t.ClosedAt != 0 && time.Unix(t.ClosedAt, 0).Before(cutoff)
}

// formatIdle renders the trailing stale marker at minute precision, flooring
// toward zero, e.g. "idle 2h13m", "idle 3h", "idle 45m".
func formatIdle(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	mins := int(d / time.Minute)
	h, m := mins/60, mins%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("idle %dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("idle %dh", h)
	default:
		return fmt.Sprintf("idle %dm", m)
	}
}
