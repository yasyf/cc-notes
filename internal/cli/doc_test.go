package cli_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
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
	Author       string  `json:"author"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	VerifiedAt   *string `json:"verified_at"`
	VerifiedBy   *string `json:"verified_by"`
	SupersededBy *string `json:"superseded_by"`
	Drift        *string `json:"drift"`
	Deleted      bool    `json:"deleted"`
	StaleAt      *string `json:"stale_at"`
	StaleBy      *string `json:"stale_by"`
	StaleReason  *string `json:"stale_reason"`
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

func TestDocAddLeanLine(t *testing.T) {
	dir := initRepo(t)
	added := mustRun(t, dir, "doc", "add", "Handoff", "--when", "resuming the cutover", "--tag", "b", "--tag", "a")
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
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Handoff", "--when", "first trigger", "--json"))
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

func TestDocExpireReview(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Stale handoff", "--when", "resuming", "--json"))
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
	keep := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Kept", "--tag", "keep", "--dir", "internal/api", "--json"))
	mustRun(t, dir, "doc", "add", "Dropped", "--tag", "skip", "--dir", "internal/sync")

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
