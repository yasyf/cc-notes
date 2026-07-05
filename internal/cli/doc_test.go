package cli_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
)

// docJSON mirrors the doc output DTO for round-trip assertions: the noteJSON
// shape plus the free-text when trigger.
type docJSON struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	When    string   `json:"when"`
	Tags    []string `json:"tags"`
	Anchors []struct {
		Kind    string  `json:"kind"`
		Value   string  `json:"value"`
		Witness *string `json:"witness"`
	} `json:"anchors"`
	Author       string           `json:"author"`
	CreatedAt    string           `json:"created_at"`
	UpdatedAt    string           `json:"updated_at"`
	VerifiedAt   *string          `json:"verified_at"`
	VerifiedBy   *string          `json:"verified_by"`
	SupersededBy *string          `json:"superseded_by"`
	Drift        *string          `json:"drift"`
	Deleted      bool             `json:"deleted"`
	StaleAt      *string          `json:"stale_at"`
	StaleBy      *string          `json:"stale_by"`
	StaleReason  *string          `json:"stale_reason"`
	Attachments  []attachmentJSON `json:"attachments"`
}

func docIDs(docs []docJSON) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out
}

func TestDocAddRoundTrip(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "auth.go", "v1\n")
	out := mustRun(t, dir, "doc", "add", "Auth migration handoff",
		"--when", "resuming the auth cutover", "--body", "the long body",
		"--tag", "handoff", "--path", "auth.go", "--json")
	if !strings.HasPrefix(out, `{"id":"`) {
		t.Fatalf("doc JSON does not lead with id: %q", out)
	}
	added := mustJSON[docJSON](t, out)
	if added.When != "resuming the auth cutover" {
		t.Fatalf("when = %q, want %q", added.When, "resuming the auth cutover")
	}
	if added.Title != "Auth migration handoff" || added.Body != "the long body" {
		t.Fatalf("title/body = %q/%q", added.Title, added.Body)
	}
	if len(added.ID) != 40 {
		t.Errorf("id length = %d, want 40", len(added.ID))
	}
	// Born-verified: doc add does the create + VerifyNote double-append, so a
	// fresh doc carries a verify timestamp instead of showing UNVERIFIED.
	if added.VerifiedAt == nil || *added.VerifiedAt == "" {
		t.Error("verified_at = null, want a born-verified timestamp")
	}
	if added.VerifiedBy == nil || *added.VerifiedBy != actorA {
		t.Errorf("verified_by = %v, want %q", added.VerifiedBy, actorA)
	}
	ref := "refs/cc-notes/docs/" + added.ID
	if got := mustGit(t, dir, "rev-list", "--count", ref); got != "2" {
		t.Errorf("doc chain has %s commits, want 2 (create + born-verified)", got)
	}

	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", added.ID, "--json"))
	if shown.ID != added.ID || shown.When != added.When {
		t.Fatalf("show id/when = %q/%q, want %q/%q", shown.ID, shown.When, added.ID, added.When)
	}
	if shown.Drift != nil {
		t.Errorf("drift = %v, want null on a fresh doc", *shown.Drift)
	}
	lean := mustRun(t, dir, "doc", "show", added.ID)
	if !strings.Contains(lean, "when: resuming the auth cutover\n") {
		t.Fatalf("lean show = %q, want a when header line", lean)
	}
	if !strings.HasSuffix(lean, "\n\nthe long body\n") {
		t.Fatalf("lean show = %q, want a blank line then the body", lean)
	}
}

// TestDocAddBodyRequired proves a doc must carry content: an empty body with no
// --attach is a UsageError, but --attach alone satisfies the requirement.
func TestDocAddBodyRequired(t *testing.T) {
	dir := initRepo(t)

	// No body, no --attach: rejected before anything is written.
	_, _, err := runCLI(t, dir, "doc", "add", "Handoff", "--when", "resuming")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("bodyless doc add err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "doc body is empty") {
		t.Fatalf("bodyless doc add message = %q, want it to explain the empty body", err.Error())
	}
	if docs := mustJSON[[]docJSON](t, mustRun(t, dir, "doc", "list", "--json")); len(docs) != 0 {
		t.Fatalf("doc count after rejected add = %d, want 0", len(docs))
	}

	// --attach with no body succeeds: the attachment is the content.
	f := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(f, []byte("attached bytes\n"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Handoff", "--attach", f, "--json"))
	if added.Title != "Handoff" || added.Body != "" {
		t.Fatalf("title/body = %q/%q, want Handoff/empty", added.Title, added.Body)
	}
	if len(added.Attachments) != 1 || added.Attachments[0].Name != "artifact.txt" {
		t.Fatalf("attachments = %v, want one named artifact.txt", added.Attachments)
	}
}

// TestDocEditRejectsBlankBody proves the empty-doc-body requirement holds at edit
// time: clearing a doc's body with --body "" is a UsageError that commits nothing,
// while clearing a NOTE body is legal (a note is not its body).
func TestDocEditRejectsBlankBody(t *testing.T) {
	dir := initRepo(t)
	doc := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Handoff", "--body", "orig", "--json"))

	_, _, err := runCLI(t, dir, "doc", "edit", doc.ID, "--body", "")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("doc edit --body \"\" err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "doc body is empty") {
		t.Fatalf("blank-body doc edit message = %q, want it to explain the empty body", err.Error())
	}
	if strings.Contains(err.Error(), "--attach") {
		t.Errorf("blank-body doc edit message %q must not name --attach (edit has no --attach)", err.Error())
	}
	if shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", doc.ID, "--json")); shown.Body != "orig" {
		t.Fatalf("doc body = %q, want orig (a rejected edit commits nothing)", shown.Body)
	}

	// A note body, by contrast, may be cleared — the requirement is doc-only.
	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Fact", "--body", "orig", "--json"))
	cleared := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", note.ID, "--body", "", "--json"))
	if cleared.Body != "" {
		t.Fatalf("note body = %q, want empty (clearing a note body is legal)", cleared.Body)
	}
}

func TestDocAddLeanLine(t *testing.T) {
	dir := initRepo(t)
	added := mustRun(t, dir, "doc", "add", "Handoff", "--body", "x", "--when", "resuming the cutover", "--tag", "b", "--tag", "a")
	listed := mustRun(t, dir, "doc", "list")
	if listed != added {
		t.Fatalf("doc list = %q, want the line doc add printed %q", listed, added)
	}
	dto := mustJSON[[]docJSON](t, mustRun(t, dir, "doc", "list", "--json"))[0]
	want := fmt.Sprintf("%s\t%s\ta,b\tHandoff\tresuming the cutover\n", dto.ID[:7], dateOf(t, dto.UpdatedAt))
	if added != want {
		t.Fatalf("doc add output = %q, want %q (when in the trailing field)", added, want)
	}
}

func TestDocEditWhen(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Handoff", "--body", "x", "--when", "first trigger", "--json"))
	if added.When != "first trigger" {
		t.Fatalf("created when = %q, want %q", added.When, "first trigger")
	}

	// edit with no flags is a usage error, exactly like note edit.
	_, _, err := runCLI(t, dir, "doc", "edit", added.ID)
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("doc edit with no flags err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}

	edited := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", added.ID, "--when", "second trigger", "--json"))
	if edited.ID != added.ID {
		t.Fatalf("edit id = %q, want %q (stable)", edited.ID, added.ID)
	}
	if edited.When != "second trigger" {
		t.Fatalf("edited when = %q, want %q", edited.When, "second trigger")
	}
	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", added.ID, "--json"))
	if shown.When != "second trigger" {
		t.Fatalf("shown when = %q, want %q", shown.When, "second trigger")
	}
}

// TestDocCommitAnchorShortSha is the regression for the short-sha anchor bug:
// an abbreviated --commit value must be expanded to the full 40-char sha at
// add time, so the read paths that resolve the anchor commit (doc show, status,
// drift) — which reject anything shorter than a full sha — never see it raw. A
// commit that does not exist is a hard error at add time, with nothing stored.
func TestDocCommitAnchorShortSha(t *testing.T) {
	dir := initRepo(t)
	full := commitFile(t, dir, "seed.go", "package main")
	short := full[:8]

	// add: the short sha is expanded to the full sha on the stored anchor.
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Anchored", "--body", "x", "--commit", short, "--json"))
	var addedCommit string
	for _, a := range added.Anchors {
		if a.Kind == "commit" {
			addedCommit = a.Value
		}
	}
	if addedCommit != full {
		t.Fatalf("stored commit anchor = %q, want the full sha %q (from short %q)", addedCommit, full, short)
	}

	// show: the originally-poisoned read path now resolves cleanly, because the
	// stored value is a full sha rather than the raw 8-char prefix.
	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", added.ID, "--json"))
	var shownCommit string
	for _, a := range shown.Anchors {
		if a.Kind == "commit" {
			shownCommit = a.Value
		}
	}
	if shownCommit != full {
		t.Fatalf("shown commit anchor = %q, want %q", shownCommit, full)
	}
	if shown.Drift != nil {
		t.Fatalf("drift = %v, want null: the anchor commit is reachable from HEAD", *shown.Drift)
	}

	// edit --add-commit: the same expansion applies on the edit path.
	c2 := commitFile(t, dir, "seed.go", "package main // v2")
	edited := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", added.ID, "--add-commit", c2[:10], "--json"))
	var hasC2 bool
	for _, a := range edited.Anchors {
		if a.Kind == "commit" && a.Value == c2 {
			hasC2 = true
		}
		if a.Kind == "commit" && len(a.Value) != 40 {
			t.Fatalf("edited commit anchor = %q, want a full 40-char sha", a.Value)
		}
	}
	if !hasC2 {
		t.Fatalf("edited anchors = %v, want the expanded %q present", edited.Anchors, c2)
	}

	// add of a non-existent commit is a hard error at add time, and nothing is
	// stored: no new doc is created.
	before := mustJSON[[]docJSON](t, mustRun(t, dir, "doc", "list", "--json"))
	_, _, err := runCLI(t, dir, "doc", "add", "Bad", "--body", "x", "--commit", "deadbeef")
	if !errors.Is(err, store.ErrNotFound) || cli.ExitCode(err) != 3 {
		t.Fatalf("add with nonexistent commit err = %v (exit %d), want ErrNotFound exit 3", err, cli.ExitCode(err))
	}
	after := mustJSON[[]docJSON](t, mustRun(t, dir, "doc", "list", "--json"))
	if len(after) != len(before) {
		t.Fatalf("doc count after failed add = %d, want unchanged %d", len(after), len(before))
	}
}

func TestDocExpireReview(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Stale handoff", "--body", "x", "--when", "resuming", "--json"))
	if out := mustRun(t, dir, "doc", "review"); out != "" {
		t.Fatalf("review of a fresh born-verified doc = %q, want empty", out)
	}

	mustRun(t, dir, "doc", "expire", added.ID, "--reason", "obsolete after the cutover")
	review := mustRun(t, dir, "doc", "review")
	if !strings.HasPrefix(review, added.ID[:7]+"\t") || !strings.HasSuffix(review, "\tEXPIRED\n") {
		t.Fatalf("review = %q, want %s...EXPIRED", review, added.ID[:7])
	}
	if out := mustRun(t, dir, "doc", "review", "--expired"); !strings.HasSuffix(out, "\tEXPIRED\n") {
		t.Fatalf("review --expired = %q, want the expired doc", out)
	}
	if out := mustRun(t, dir, "doc", "review", "--drift"); out != "" {
		t.Fatalf("review --drift = %q, want empty (the doc is expired, not drifted)", out)
	}
	dj := mustJSON[[]docJSON](t, mustRun(t, dir, "doc", "review", "--json"))
	if len(dj) != 1 || dj[0].Drift == nil || *dj[0].Drift != "EXPIRED" {
		t.Fatalf("review --json = %+v, want one EXPIRED doc", dj)
	}

	mustRun(t, dir, "doc", "expire", added.ID, "--clear")
	if out := mustRun(t, dir, "doc", "review"); out != "" {
		t.Fatalf("review after --clear = %q, want empty", out)
	}
}

func TestDocListFilters(t *testing.T) {
	dir := initRepo(t)
	keep := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Kept", "--body", "x", "--tag", "keep", "--dir", "internal/api", "--json"))
	mustRun(t, dir, "doc", "add", "Dropped", "--body", "x", "--tag", "skip", "--dir", "internal/sync")

	byTag := mustJSON[[]docJSON](t, mustRun(t, dir, "doc", "list", "--tag", "keep", "--json"))
	if len(byTag) != 1 || byTag[0].ID != keep.ID {
		t.Fatalf("list --tag keep = %v, want only %s", docIDs(byTag), keep.ID)
	}
	byDir := mustRun(t, dir, "doc", "list", "--dir", "internal/api")
	if !strings.HasPrefix(byDir, keep.ID[:7]+"\t") || strings.Count(byDir, "\n") != 1 {
		t.Fatalf("list --dir internal/api = %q, want only %s", byDir, keep.ID[:7])
	}

	rm := mustRun(t, dir, "doc", "rm", keep.ID)
	if !strings.HasPrefix(rm, keep.ID[:7]+"\t") {
		t.Fatalf("rm echo = %q, want the tombstoned lean line", rm)
	}
	if out := mustRun(t, dir, "doc", "list"); strings.Contains(out, keep.ID[:7]) {
		t.Fatalf("list after rm = %q, want the tombstoned doc dropped", out)
	}
	if out := mustRun(t, dir, "doc", "list", "--all"); !strings.Contains(out, keep.ID[:7]) {
		t.Fatalf("list --all = %q, want the tombstoned doc present", out)
	}
}
