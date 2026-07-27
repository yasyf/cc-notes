package kg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

// commitPaths lists the repository paths each of shas touched, keyed by
// commit, in one git invocation. A merge commit and a sha the object database
// does not hold both contribute nothing: git prints no diff for either, so
// neither appears in the result and neither is an error.
func commitPaths(ctx context.Context, dir string, shas []model.SHA) (map[model.SHA][]string, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	var stdin strings.Builder
	for _, sha := range shas {
		stdin.WriteString(string(sha))
		stdin.WriteByte('\n')
	}
	//nolint:gosec // G204: git is a fixed argv[0], every flag is a literal, and the only variable is this repository's own directory.
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff-tree", "--stdin", "-r", "--root", "--no-renames", "-z")
	cmd.Stdin = strings.NewReader(stdin.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff-tree: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseDiffTree(stdout.String()), nil
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
