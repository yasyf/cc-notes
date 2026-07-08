package cli_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
)

// TestDidYouMeanFlagHintFires: a mistyped flag whose canonical replacement
// exists on the failing command exits 2 with a did-you-mean hint appended to
// the unchanged unknown-flag error.
func TestDidYouMeanFlagHintFires(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "seed.go", "package main")

	_, _, err := runCLI(t, dir, "note", "add", "x", "--desc", "y")
	if err == nil {
		t.Fatal("note add --desc should error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --desc") || !strings.Contains(err.Error(), "did you mean --body") {
		t.Fatalf("note add --desc error = %q, want the unknown-flag error plus a --body hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestDidYouMeanShorthandHintFires: the shorthand branch of the extractor is
// exercised by -m, whose synonym (entry, then body) resolves to --body on note
// add.
func TestDidYouMeanShorthandHintFires(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "seed.go", "package main")

	_, _, err := runCLI(t, dir, "note", "add", "x", "-m", "y")
	if err == nil {
		t.Fatal("note add -m should error")
	}
	if !strings.Contains(err.Error(), "unknown shorthand flag") || !strings.Contains(err.Error(), "did you mean --body") {
		t.Fatalf("note add -m error = %q, want the unknown-shorthand error plus a --body hint", err.Error())
	}
}

// TestDidYouMeanHintSuppressed: when the command has no canonical flag to point
// at, the error stays exactly pflag's, with no hint.
func TestDidYouMeanHintSuppressed(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "status", "--desc", "y")
	if err == nil {
		t.Fatal("status --desc should error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --desc") {
		t.Fatalf("status --desc error = %q, want the plain unknown-flag error", err.Error())
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("status has no --body, so --desc must carry no hint: %q", err.Error())
	}
}

// TestRootNounVerbHint: a bare noun verb at the top level exits 2 with the
// noun-scoped guidance.
func TestRootNounVerbHint(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "list")
	if err == nil {
		t.Fatal("bare 'list' should error")
	}
	if !strings.Contains(err.Error(), "noun-scoped") {
		t.Fatalf("bare list error = %q, want the noun-scoped hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestGlobalShow: the top-level "show ID" renders exactly what the noun-scoped
// show renders, in both text and JSON, for every kind it dispatches to; an
// unknown prefix is a clean not-found.
func TestGlobalShow(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "seed.go", "package main")

	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "A note", "--body", "the body", "--label", "x", "--json"))
	task := addTask(t, dir, "A task")

	cases := []struct {
		kind string
		id   string
	}{
		{"note", note.ID},
		{"task", task.ID},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			prefix := tc.id[:8]
			if got, want := mustRun(t, dir, "show", prefix), mustRun(t, dir, tc.kind, "show", prefix); got != want {
				t.Fatalf("show %s (text) = %q, want the noun-scoped %s show %q", prefix, got, tc.kind, want)
			}
			if got, want := mustRun(t, dir, "show", prefix, "--json"), mustRun(t, dir, tc.kind, "show", prefix, "--json"); got != want {
				t.Fatalf("show %s --json = %q, want the noun-scoped %s show %q", prefix, got, tc.kind, want)
			}
		})
	}

	// An id one hex longer than a real id cannot prefix any entity: clean 3.
	if _, _, err := runCLI(t, dir, "show", note.ID+"0"); err == nil {
		t.Fatal("show of an absent prefix should error")
	} else if got := cli.ExitCode(err); got != 3 {
		t.Fatalf("absent-prefix exit = %d, want 3 (not-found)", got)
	}

	// A prefix matching one note and one task across kinds is cross-kind
	// ambiguous: exit 5 with store.AmbiguousError listing both short ids (mirrors
	// the note/doc pigeonhole in TestCompactAmbiguousAcrossNoteAndDoc). A fresh
	// repo keeps the note and task namespaces the only populated ones, so a
	// leading char holding exactly one of each yields the cross-kind path.
	fresh := initRepo(t)
	notesByChar := map[byte][]string{}
	tasksByChar := map[byte][]string{}
	var ambNote, ambTask string
	pick := func() bool {
		for ch, notes := range notesByChar {
			if len(notes) == 1 && len(tasksByChar[ch]) == 1 {
				ambNote, ambTask = notes[0], tasksByChar[ch][0]
				return true
			}
		}
		return false
	}
	for i := 0; i < 32; i++ {
		n := mustJSON[noteJSON](t, mustRun(t, fresh, "note", "add", fmt.Sprintf("note-%d", i), "--json"))
		notesByChar[n.ID[0]] = append(notesByChar[n.ID[0]], n.ID)
		tk := addTask(t, fresh, fmt.Sprintf("task-%d", i))
		tasksByChar[tk.ID[0]] = append(tasksByChar[tk.ID[0]], tk.ID)
		if pick() {
			break
		}
	}
	if ambNote == "" {
		t.Fatal("no leading char with exactly one note and one task after 32 rounds")
	}
	prefix := ambNote[:1]
	_, _, err := runCLI(t, fresh, "show", prefix)
	if err == nil {
		t.Fatalf("show %q spanning a note and a task returned nil error", prefix)
	}
	if !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("show %q error = %v, want ErrAmbiguous", prefix, err)
	}
	if got := cli.ExitCode(err); got != 5 {
		t.Fatalf("cross-kind ambiguous exit = %d, want 5 (ambiguous)", got)
	}
	if msg := err.Error(); !strings.Contains(msg, ambNote[:7]) || !strings.Contains(msg, ambTask[:7]) {
		t.Fatalf("ambiguity error %q must list both note %s and task %s", msg, ambNote[:7], ambTask[:7])
	}
}

// TestDidYouMeanTagToLabel: after the records cutover --tag is gone from note
// add; the tag→label synonym points at the command's --label.
func TestDidYouMeanTagToLabel(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "seed.go", "package main")

	_, _, err := runCLI(t, dir, "note", "add", "t", "--tag", "x")
	if err == nil {
		t.Fatal("note add --tag should error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --tag") || !strings.Contains(err.Error(), "did you mean --label") {
		t.Fatalf("note add --tag error = %q, want the unknown-flag error plus a --label hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestDidYouMeanAnchorPathToPath: note search's anchor filters lost their
// anchor- prefix; the anchor-path→path synonym points at the bare --path.
func TestDidYouMeanAnchorPathToPath(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "note", "search", "q", "--anchor-path", "p")
	if err == nil {
		t.Fatal("note search --anchor-path should error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --anchor-path") || !strings.Contains(err.Error(), "did you mean --path") {
		t.Fatalf("note search --anchor-path error = %q, want the unknown-flag error plus a --path hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestDidYouMeanMessageToEntry: log append dropped -m/--message for --entry;
// the m shorthand synonym resolves to --entry on the failing command.
func TestDidYouMeanMessageToEntry(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "L", "--json"))

	_, _, err := runCLI(t, dir, "log", "append", added.ID, "-m", "x")
	if err == nil {
		t.Fatal("log append -m should error")
	}
	if !strings.Contains(err.Error(), "unknown shorthand flag") || !strings.Contains(err.Error(), "did you mean --entry") {
		t.Fatalf("log append -m error = %q, want the unknown-shorthand error plus a --entry hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestDidYouMeanTaskDescToBody: task add's --desc became --body; the desc→body
// synonym points at the command's --body.
func TestDidYouMeanTaskDescToBody(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "task", "add", "t", "--desc", "x")
	if err == nil {
		t.Fatal("task add --desc should error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --desc") || !strings.Contains(err.Error(), "did you mean --body") {
		t.Fatalf("task add --desc error = %q, want the unknown-flag error plus a --body hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestDidYouMeanUnassignToNoAssignee: task edit's --unassign became
// --no-assignee; the unassign→no-assignee synonym points at it.
func TestDidYouMeanUnassignToNoAssignee(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "task", "edit", "deadbeef", "--unassign")
	if err == nil {
		t.Fatal("task edit --unassign should error")
	}
	if !strings.Contains(err.Error(), "unknown flag: --unassign") || !strings.Contains(err.Error(), "did you mean --no-assignee") {
		t.Fatalf("task edit --unassign error = %q, want the unknown-flag error plus a --no-assignee hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestDidYouMeanSprintStartToActivate: the sprint start verb became activate;
// the removed-subcommand hint points at it.
func TestDidYouMeanSprintStartToActivate(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "sprint", "start", "deadbeef")
	if err == nil {
		t.Fatal("sprint start should error")
	}
	if !strings.Contains(err.Error(), "sprint activate") {
		t.Fatalf("sprint start error = %q, want the 'sprint activate' hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestDidYouMeanCriterionResetToPending: the criterion reset verb became pending;
// the removed-subcommand hint points at it.
func TestDidYouMeanCriterionResetToPending(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "task", "criterion", "reset", "deadbeef", "c")
	if err == nil {
		t.Fatal("task criterion reset should error")
	}
	if !strings.Contains(err.Error(), "task criterion pending") {
		t.Fatalf("criterion reset error = %q, want the 'task criterion pending' hint", err.Error())
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage)", got)
	}
}

// TestLegacyVerbWithFlagHints: a removed verb that carries an unknown flag still
// exits 2 with its migration hint. cobra parses flags before validating
// positionals, so without flagError's leading-positional check these would
// surface as a bare "unknown flag" error, hiding the hint the flagless form gets
// through noUnknownSubcommand.
func TestLegacyVerbWithFlagHints(t *testing.T) {
	dir := initRepo(t)

	tests := []struct {
		name string
		args []string
		hint string
	}{
		{"task move --to", []string{"task", "move", "abc", "--to", "main"}, "task edit --branch"},
		{"task move --backlog", []string{"task", "move", "abc", "--backlog"}, "task edit --branch"},
		{"sprint start --json", []string{"sprint", "start", "abc", "--json"}, "sprint activate"},
		{"criterion reset --json", []string{"task", "criterion", "reset", "t1", "c1", "--json"}, "task criterion pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCLI(t, dir, tt.args...)
			if err == nil {
				t.Fatalf("%v should error", tt.args)
			}
			if !strings.Contains(err.Error(), tt.hint) {
				t.Fatalf("%v error = %q, want the %q hint", tt.args, err.Error(), tt.hint)
			}
			if got := cli.ExitCode(err); got != 2 {
				t.Fatalf("%v exit = %d, want 2 (usage)", tt.args, got)
			}
		})
	}
}
