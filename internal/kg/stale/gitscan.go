package stale

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// touch is one file's line churn in one commit.
type touch struct {
	Path  string
	TS    int64
	Lines int
}

// commitMark prefixes the commit-time record in the churn log's format, so a
// NUL-separated chunk is unambiguously either a commit header or a numstat row.
const commitMark = "\x01"

// churnLog reads every tracked-file touch since t in one `git log --numstat`
// pass, so a whole-corpus assessment costs one git invocation rather than one
// per anchor. Renames are disabled, which pins every numstat record to the
// three-field form; a merge contributes nothing, since its content already
// counted in the parents.
func churnLog(ctx context.Context, dir string, since time.Time) ([]touch, error) {
	out, err := gitOutput(ctx, dir,
		"log", "--since="+since.UTC().Format(time.RFC3339), "--no-renames", "--numstat", "-z",
		"--format="+commitMark+"%ct")
	if err != nil {
		return nil, err
	}
	var ts int64
	var touches []touch
	for _, chunk := range strings.Split(out, "\x00") {
		chunk = strings.TrimLeft(chunk, "\n")
		if chunk == "" {
			continue
		}
		if mark, ok := strings.CutPrefix(chunk, commitMark); ok {
			ts, err = strconv.ParseInt(mark, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("churn log: parse commit time %q: %w", mark, err)
			}
			continue
		}
		fields := strings.SplitN(chunk, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("churn log: malformed numstat record %q", chunk)
		}
		added, err := numstat(fields[0])
		if err != nil {
			return nil, err
		}
		deleted, err := numstat(fields[1])
		if err != nil {
			return nil, err
		}
		touches = append(touches, touch{Path: fields[2], TS: ts, Lines: added + deleted})
	}
	return touches, nil
}

// numstat parses one numstat count; a binary file reports "-" and counts zero
// lines.
func numstat(field string) (int, error) {
	if field == "-" {
		return 0, nil
	}
	n, err := strconv.Atoi(field)
	if err != nil {
		return 0, fmt.Errorf("churn log: parse numstat count %q: %w", field, err)
	}
	return n, nil
}

// gitOutput runs a read-only git command in dir and returns its stdout. The two
// callers need a tree listing and a numstat log, neither of which the internal
// gitcmd surface exposes.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	//nolint:gosec // G204: git is a fixed argv[0]; args are this package's own read-only subcommands, never user-shell input.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
