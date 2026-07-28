package gitcmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/model"
)

// churnMark prefixes the commit header in a NumstatLog format string, so a
// NUL-separated chunk is unambiguously either a header or a numstat row.
const churnMark = "\x01"

// FileChurn is one file's added and deleted line counts in one commit. A binary
// file reports "-" for both and churns no lines.
type FileChurn struct {
	Path    string
	Added   int
	Deleted int
}

// CommitChurn is one commit's per-file line churn, as git log --numstat reports
// it. A commit that changed nothing under the scanned pathspec holds no files.
type CommitChurn struct {
	Time  time.Time
	Files []FileChurn
}

// NumstatScope bounds a NumstatLog walk: Since drops commits older than it,
// Limit caps how many commits are read newest-first, and Paths restricts the
// walk to a pathspec. A zero field imposes no bound.
type NumstatScope struct {
	Since time.Time
	Limit int
	Paths []string
}

// NumstatLog reads the per-file line churn of every commit in scope, newest
// first, in one git invocation. Merges and renames are excluded, so every
// numstat record is a three-field row belonging to exactly one commit; a
// merge's content already counted in its parents.
func (g Git) NumstatLog(ctx context.Context, scope NumstatScope) ([]CommitChurn, error) {
	args := []string{"log", "--no-merges", "--no-renames", "--numstat", "-z", "--format=" + churnMark + "%ct"}
	if !scope.Since.IsZero() {
		args = append(args, "--since="+scope.Since.UTC().Format(time.RFC3339))
	}
	if scope.Limit > 0 {
		args = append(args, "-n", strconv.Itoa(scope.Limit))
	}
	if len(scope.Paths) > 0 {
		args = append(args, "--")
		args = append(args, scope.Paths...)
	}
	out, err := g.run(ctx, "", args...)
	if err != nil {
		return nil, fmt.Errorf("numstat log: %w", err)
	}
	return parseNumstatLog(out)
}

// parseNumstatLog decodes `git log --numstat -z --format=<churnMark>%ct`: each
// chunk is either a commit header carrying the commit time or one numstat row,
// and a row belongs to the header above it.
func parseNumstatLog(out string) ([]CommitChurn, error) {
	var commits []CommitChurn
	for _, chunk := range strings.Split(out, "\x00") {
		chunk = strings.TrimLeft(chunk, "\n")
		if chunk == "" {
			continue
		}
		if mark, ok := strings.CutPrefix(chunk, churnMark); ok {
			secs, err := strconv.ParseInt(mark, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("numstat log: parse commit time %q: %w", mark, err)
			}
			commits = append(commits, CommitChurn{Time: time.Unix(secs, 0)})
			continue
		}
		fields := strings.SplitN(chunk, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("numstat log: malformed numstat record %q", chunk)
		}
		if len(commits) == 0 {
			return nil, fmt.Errorf("numstat log: record %q precedes every commit header", chunk)
		}
		added, err := numstatCount(fields[0])
		if err != nil {
			return nil, err
		}
		deleted, err := numstatCount(fields[1])
		if err != nil {
			return nil, err
		}
		last := &commits[len(commits)-1]
		last.Files = append(last.Files, FileChurn{Path: fields[2], Added: added, Deleted: deleted})
	}
	return commits, nil
}

// numstatCount parses one numstat count; a binary file reports "-".
func numstatCount(field string) (int, error) {
	if field == "-" {
		return 0, nil
	}
	n, err := strconv.Atoi(field)
	if err != nil {
		return 0, fmt.Errorf("numstat log: parse count %q: %w", field, err)
	}
	return n, nil
}

// TrackedFiles lists every path in the index, relative to the repository root.
func (g Git) TrackedFiles(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "", "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	var paths []string
	for name := range strings.SplitSeq(out, "\x00") {
		if name != "" {
			paths = append(paths, name)
		}
	}
	return paths, nil
}

// CommitPaths lists the paths each of shas touched, keyed by commit, in one git
// invocation. A merge commit and a sha the object database does not hold both
// contribute nothing: git prints no diff for either, so neither appears in the
// result and neither is an error.
func (g Git) CommitPaths(ctx context.Context, shas []model.SHA) (map[model.SHA][]string, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	var stdin strings.Builder
	for _, sha := range shas {
		stdin.WriteString(string(sha))
		stdin.WriteByte('\n')
	}
	out, err := g.run(ctx, stdin.String(), "diff-tree", "--stdin", "-r", "--root", "--no-renames", "-z")
	if err != nil {
		return nil, fmt.Errorf("commit paths for %d commits: %w", len(shas), err)
	}
	return parseDiffTree(out), nil
}

// parseDiffTree reads `git diff-tree --stdin -r -z` raw output: one record per
// commit sha, then a metadata record (":<modes> <oids> <status>") and a path
// record per change. --no-renames keeps every status single-path, so metadata
// and path always alternate.
func parseDiffTree(out string) map[model.SHA][]string {
	paths := map[model.SHA][]string{}
	var current model.SHA
	wantPath := false
	for record := range strings.SplitSeq(out, "\x00") {
		switch {
		case record == "":
		case wantPath:
			paths[current] = append(paths[current], record)
			wantPath = false
		case strings.HasPrefix(record, ":"):
			wantPath = true
		default:
			current = model.SHA(record)
		}
	}
	return paths
}
