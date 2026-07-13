// File-edit mode tests: --checkout writes an entity to an editable file, the
// test edits that file, and --apply diffs it back into ops. Every test runs
// the cobra tree in-process against a real git repository.
package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
)

// mustCheckout runs a --checkout command and returns the buffer path it prints
// to stdout (the hint goes to stderr, so stdout is the path alone).
func mustCheckout(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := runCLI(t, dir, args...)
	if err != nil {
		t.Fatalf("cc-notes %s: %v (stderr %q)", strings.Join(args, " "), err, stderr)
	}
	path := strings.TrimSpace(stdout)
	if path == "" {
		t.Fatalf("cc-notes %s printed no buffer path (stderr %q)", strings.Join(args, " "), stderr)
	}
	return path
}

func readBuf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read buffer %s: %v", path, err)
	}
	return string(data)
}

func writeBuf(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write buffer %s: %v", path, err)
	}
}

// replaceInBuf rewrites the first occurrence of old in the buffer, failing if
// it is absent so a test never silently edits nothing.
func replaceInBuf(t *testing.T, path, old, repl string) {
	t.Helper()
	content := readBuf(t, path)
	if !strings.Contains(content, old) {
		t.Fatalf("buffer %s missing %q:\n%s", path, old, content)
	}
	writeBuf(t, path, strings.Replace(content, old, repl, 1))
}

func bufExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestDocEditCheckoutApply(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Handoff",
		"--when", "later", "--body", "orig body", "--label", "design", "--json"))
	ref := "refs/cc-notes/docs/" + added.ID
	before := gittest.Git(t, dir, "rev-list", "--count", ref)

	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	if !strings.Contains(path, ".git") || !strings.HasSuffix(path, added.ID+".md") {
		t.Fatalf("checkout path = %q, want <git>/cc-notes/edit/<id>.md", path)
	}
	if buf := readBuf(t, path); !strings.Contains(buf, "orig body") || !strings.Contains(buf, "title: Handoff") {
		t.Fatalf("buffer does not round-trip the rendered doc:\n%s", buf)
	}

	replaceInBuf(t, path, "orig body", "EDITED via file")
	replaceInBuf(t, path, "tags: [design]", "tags: [design, fromfile]")

	applied := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", added.ID, "--apply", "--json"))
	if applied.Body != "EDITED via file" {
		t.Fatalf("body = %q, want EDITED via file", applied.Body)
	}
	if strings.Join(applied.Tags, ",") != "design,fromfile" {
		t.Fatalf("tags = %v, want [design fromfile]", applied.Tags)
	}
	if applied.When != "later" {
		t.Fatalf("when = %q, want later (untouched field unchanged)", applied.When)
	}
	if bufExists(path) {
		t.Fatalf("buffer %s still present after apply, want removed", path)
	}
	if after := gittest.Git(t, dir, "rev-list", "--count", ref); after == before {
		t.Fatalf("chain still %s commits after apply, want one more", after)
	}
}

func TestNoteEditCheckoutApply(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Fact", "--body", "v1", "--json"))
	path := mustCheckout(t, dir, "note", "edit", added.ID, "--checkout")
	replaceInBuf(t, path, "v1", "v2 via file")
	applied := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", added.ID, "--apply", "--json"))
	if applied.Body != "v2 via file" {
		t.Fatalf("body = %q, want v2 via file", applied.Body)
	}
	if bufExists(path) {
		t.Fatalf("buffer present after apply")
	}
}

func TestDocAddCheckoutApply(t *testing.T) {
	dir := initRepo(t)
	path := mustCheckout(t, dir, "doc", "add", "--checkout")
	if !strings.HasPrefix(filepath.Base(path), "new-") {
		t.Fatalf("add checkout path = %q, want a new-<nonce> stem", path)
	}
	content := readBuf(t, path)
	content = strings.Replace(content, "title: \"\"", "title: Made from a file", 1)
	content = strings.Replace(content, "when: \"\"", "when: reading the readme", 1)
	content += "Body via the file workflow."
	writeBuf(t, path, content)

	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "--apply", path, "--json"))
	if added.Title != "Made from a file" || added.When != "reading the readme" {
		t.Fatalf("title/when = %q/%q", added.Title, added.When)
	}
	if added.Body != "Body via the file workflow." {
		t.Fatalf("body = %q, want Body via the file workflow.", added.Body)
	}
	if added.VerifiedAt == nil || added.VerifiedBy == nil || *added.VerifiedBy != actorA {
		t.Fatalf("created doc not born-verified: verified_at=%v verified_by=%v, want set and %q",
			added.VerifiedAt, added.VerifiedBy, actorA)
	}
	if bufExists(path) {
		t.Fatalf("buffer %s present after apply, want removed", path)
	}
	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", added.ID, "--json"))
	if shown.Body != added.Body {
		t.Fatalf("show body = %q, want %q", shown.Body, added.Body)
	}
}

// TestDocAddApplyOverCapTitleKeepsBuffer proves the title cap guards the
// file-mode create path too: an over-cap title in the buffer fails with a
// UsageError carrying the re-run hint, the buffer survives, and fixing the
// title lets the same buffer apply.
func TestDocAddApplyOverCapTitleKeepsBuffer(t *testing.T) {
	dir := initRepo(t)
	path := mustCheckout(t, dir, "doc", "add", "--checkout")
	over := strings.Repeat("x", 257)
	content := readBuf(t, path)
	content = strings.Replace(content, "title: \"\"", "title: "+over, 1)
	content += "a body"
	writeBuf(t, path, content)

	_, _, err := runCLI(t, dir, "doc", "add", "--apply", path, "--json")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("apply of over-cap title err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "re-run --apply") {
		t.Fatalf("apply error %q, want the re-run --apply hint", err.Error())
	}
	if !bufExists(path) {
		t.Fatalf("buffer %s removed on rejected apply, want kept so the agent can fix it", path)
	}

	replaceInBuf(t, path, over, "Short title")
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "--apply", path, "--json"))
	if added.Title != "Short title" {
		t.Fatalf("title = %q, want Short title after fixing the buffer", added.Title)
	}
	if bufExists(path) {
		t.Fatalf("buffer %s present after a successful apply, want removed", path)
	}
}

// TestDocAddApplyEmptyBodyKeepsBuffer proves the doc body requirement guards the
// file-mode create path: a titled but bodyless buffer is rejected and preserved,
// and filling in the body lets the same buffer apply.
func TestDocAddApplyEmptyBodyKeepsBuffer(t *testing.T) {
	dir := initRepo(t)
	path := mustCheckout(t, dir, "doc", "add", "--checkout")
	content := strings.Replace(readBuf(t, path), "title: \"\"", "title: Titled but bodyless", 1)
	writeBuf(t, path, content)

	_, _, err := runCLI(t, dir, "doc", "add", "--apply", path, "--json")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("apply of bodyless doc err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "doc body is empty") {
		t.Fatalf("apply error %q, want it to explain the empty body", err.Error())
	}
	if !bufExists(path) {
		t.Fatalf("buffer %s removed on rejected apply, want kept", path)
	}

	writeBuf(t, path, content+"Now it has a body.")
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "--apply", path, "--json"))
	if added.Body != "Now it has a body." {
		t.Fatalf("body = %q, want the filled-in body", added.Body)
	}
}

// TestDocEditApplyBlankBodyKeepsBuffer proves the doc body requirement guards the
// file-mode edit path: blanking a checked-out doc's body is rejected, the buffer
// survives, and the doc is unchanged — while the same blanking on a note buffer
// applies, since clearing a note body is legal.
func TestDocEditApplyBlankBodyKeepsBuffer(t *testing.T) {
	dir := initRepo(t)
	doc := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Handoff", "--body", "orig body", "--json"))
	path := mustCheckout(t, dir, "doc", "edit", doc.ID, "--checkout")
	replaceInBuf(t, path, "orig body", "")

	_, _, err := runCLI(t, dir, "doc", "edit", doc.ID, "--apply")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("apply of blanked doc body err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "doc body is empty") {
		t.Fatalf("apply error %q, want it to explain the empty body", err.Error())
	}
	if strings.Contains(err.Error(), "--body") || strings.Contains(err.Error(), "--attach") {
		t.Errorf("file-mode apply error %q must hint at the buffer, not flags (caller is already in a checked-out file)", err.Error())
	}
	if !bufExists(path) {
		t.Fatalf("buffer %s removed on rejected apply, want kept", path)
	}
	if shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", doc.ID, "--json")); shown.Body != "orig body" {
		t.Fatalf("body = %q, want orig body (a rejected apply commits nothing)", shown.Body)
	}

	// A note buffer blanked to no body applies cleanly.
	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Fact", "--body", "orig body", "--json"))
	npath := mustCheckout(t, dir, "note", "edit", note.ID, "--checkout")
	replaceInBuf(t, npath, "orig body", "")
	cleared := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", note.ID, "--apply", "--json"))
	if cleared.Body != "" {
		t.Fatalf("note body = %q, want empty (clearing a note body applies)", cleared.Body)
	}
}

func TestNoteAddCheckoutApply(t *testing.T) {
	dir := initRepo(t)
	path := mustCheckout(t, dir, "note", "add", "--checkout")
	content := readBuf(t, path)
	content = strings.Replace(content, "title: \"\"", "title: File note", 1)
	content += "A fact captured via the file workflow."
	writeBuf(t, path, content)
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "--apply", path, "--json"))
	if added.Title != "File note" || added.Body != "A fact captured via the file workflow." {
		t.Fatalf("title/body = %q/%q", added.Title, added.Body)
	}
	if added.VerifiedAt == nil || added.VerifiedBy == nil || *added.VerifiedBy != actorA {
		t.Fatalf("created note not born-verified: %v/%v", added.VerifiedAt, added.VerifiedBy)
	}
}

func TestEditApplyNoBuffer(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "X", "--body", "orig", "--json"))
	_, _, err := runCLI(t, dir, "doc", "edit", added.ID, "--apply")
	if !errors.Is(err, store.ErrNotFound) || cli.ExitCode(err) != 3 {
		t.Fatalf("apply with no buffer err = %v (exit %d), want ErrNotFound exit 3", err, cli.ExitCode(err))
	}
}

func TestEditApplyParseErrorKeepsBuffer(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "X", "--body", "orig", "--json"))
	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	writeBuf(t, path, "not a valid doc file: no frontmatter delimiter\n")
	_, _, err := runCLI(t, dir, "doc", "edit", added.ID, "--apply")
	if !errors.Is(err, fusefs.ErrParse) {
		t.Fatalf("apply of garbage err = %v, want ErrParse", err)
	}
	if !bufExists(path) {
		t.Fatalf("buffer %s removed on parse error, want kept so the agent can fix it", path)
	}
	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", added.ID, "--json"))
	if shown.Body != "orig" {
		t.Fatalf("body = %q, want orig (a failed apply commits nothing)", shown.Body)
	}
}

func TestEditApplyImmutableFieldKeepsBuffer(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "X", "--body", "orig", "--json"))
	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	// The author line renders before verified_by, so the first occurrence of the
	// actor email is the immutable author field.
	replaceInBuf(t, path, "a@example.com", "evil@example.com")
	_, _, err := runCLI(t, dir, "doc", "edit", added.ID, "--apply")
	if !errors.Is(err, fusefs.ErrImmutableField) {
		t.Fatalf("apply with a changed immutable field err = %v, want ErrImmutableField", err)
	}
	if !bufExists(path) {
		t.Fatalf("buffer removed on immutable-field error, want kept")
	}
}

func TestEditAbortRemovesBuffer(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "X", "--body", "orig", "--json"))
	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	replaceInBuf(t, path, "orig", "discarded edit")
	mustRun(t, dir, "doc", "edit", added.ID, "--abort")
	if bufExists(path) {
		t.Fatalf("buffer present after abort")
	}
	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", added.ID, "--json"))
	if shown.Body != "orig" {
		t.Fatalf("body = %q, want orig (abort discards the edit)", shown.Body)
	}
	_, _, err := runCLI(t, dir, "doc", "edit", added.ID, "--abort")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("abort with no buffer err = %v, want ErrNotFound", err)
	}
}

func TestEditEmptyDiffCommitsNothing(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "X", "--body", "orig", "--json"))
	ref := "refs/cc-notes/docs/" + added.ID
	before := gittest.Git(t, dir, "rev-list", "--count", ref)
	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	_, stderr, err := runCLI(t, dir, "doc", "edit", added.ID, "--apply")
	if err != nil {
		t.Fatalf("apply with no changes: %v (stderr %q)", err, stderr)
	}
	if !strings.Contains(stderr, "no changes") {
		t.Fatalf("stderr = %q, want a no-changes note", stderr)
	}
	if after := gittest.Git(t, dir, "rev-list", "--count", ref); after != before {
		t.Fatalf("chain = %s commits, want unchanged %s (an empty diff commits nothing)", after, before)
	}
	if bufExists(path) {
		t.Fatalf("buffer present after empty-diff apply, want cleaned up")
	}
}

// TestEditConcurrentMerge proves --apply diffs against the checkout-time base,
// not the current tip: a concurrent edit to an untouched field survives. If it
// diffed against the tip, the stale title in the buffer would revert the
// concurrent title edit.
func TestEditConcurrentMerge(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Title one", "--body", "orig", "--json"))
	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	mustRun(t, dir, "doc", "edit", added.ID, "--title", "Title two")
	replaceInBuf(t, path, "orig", "body three")
	applied := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", added.ID, "--apply", "--json"))
	if applied.Title != "Title two" {
		t.Fatalf("title = %q, want Title two (concurrent title edit must survive)", applied.Title)
	}
	if applied.Body != "body three" {
		t.Fatalf("body = %q, want body three (our file edit applied)", applied.Body)
	}
}

func TestFileModeUsageErrors(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "X", "--body", "orig", "--json"))
	for _, args := range [][]string{
		{"doc", "edit", added.ID, "--checkout", "--apply"},       // mutually exclusive
		{"doc", "edit", added.ID, "--checkout", "--title", "Y"},  // file mode + content flag
		{"note", "edit", added.ID, "--apply", "--abort"},         // mutually exclusive
		{"doc", "add", "--checkout", "one", "two"},               // checkout takes at most one (the optional TITLE)
		{"doc", "add", "--apply"},                                // apply needs the buffer path
		{"doc", "add", "--checkout", "--attach", "x"},            // --attach is not a checkout flag
		{"doc", "add", "--checkout", "--body", "x"},              // --body is not a checkout flag
		{"doc", "add", "--apply", "P", "--when", "w"},            // --when is not an apply flag
		{"doc", "edit", added.ID, "--checkout", "--attach", "x"}, // edit checkout takes no content flag
		{"note", "edit", added.ID, "--apply", "--attach", "x"},   // edit apply has no --attach
		{"doc", "edit", added.ID, "--replace"},                   // --replace requires --attach
	} {
		_, _, err := runCLI(t, dir, args...)
		var usage *cli.UsageError
		if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
			t.Errorf("cc-notes %s err = %v (exit %d), want UsageError exit 2", strings.Join(args, " "), err, cli.ExitCode(err))
		}
	}
}

func TestDocAddCheckoutPrefill(t *testing.T) {
	dir := initRepo(t)
	head := commitFile(t, dir, "seed.go", "package main")
	path := mustCheckout(t, dir, "doc", "add", "--checkout", "Prefilled title", "--when", "resuming",
		"--label", "handoff", "--commit", "HEAD", "--path", "internal/cli", "--branch", "main")

	// The buffer carries the prefilled editable keys, and --commit HEAD is
	// resolved to the full 40-char sha before rendering.
	buf := readBuf(t, path)
	for _, want := range []string{
		"title: Prefilled title", "when: resuming", "tags: [handoff]",
		"commits: [" + head + "]", "paths: [internal/cli]", "branches: [main]",
	} {
		if !strings.Contains(buf, want) {
			t.Fatalf("checkout buffer missing %q:\n%s", want, buf)
		}
	}

	writeBuf(t, path, buf+"The long-form body.")
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "--apply", path, "--json"))
	if added.Title != "Prefilled title" || added.When != "resuming" {
		t.Fatalf("title/when = %q/%q, want Prefilled title/resuming", added.Title, added.When)
	}
	if added.Body != "The long-form body." {
		t.Fatalf("body = %q, want the appended body", added.Body)
	}
	if strings.Join(added.Tags, ",") != "handoff" {
		t.Fatalf("tags = %v, want [handoff]", added.Tags)
	}
	got := map[string]string{}
	for _, a := range added.Anchors {
		got[a.Kind] = a.Value
	}
	if got["commit"] != head {
		t.Fatalf("commit anchor = %q, want the full sha %q from --commit HEAD", got["commit"], head)
	}
	if got["path"] != "internal/cli" || got["branch"] != "main" {
		t.Fatalf("anchors = %+v, want path internal/cli and branch main", got)
	}
	if added.VerifiedAt == nil || added.VerifiedBy == nil || *added.VerifiedBy != actorA {
		t.Fatalf("created doc not born-verified: verified_at=%v verified_by=%v", added.VerifiedAt, added.VerifiedBy)
	}
}

func TestNoteAddCheckoutPrefill(t *testing.T) {
	dir := initRepo(t)
	head := commitFile(t, dir, "seed.go", "package main")
	path := mustCheckout(t, dir, "note", "add", "--checkout", "Prefilled note",
		"--label", "design", "--commit", "HEAD", "--path", "auth.go")

	buf := readBuf(t, path)
	if strings.Contains(buf, "when:") {
		t.Fatalf("note checkout buffer names when; notes have no trigger:\n%s", buf)
	}
	for _, want := range []string{"title: Prefilled note", "tags: [design]", "commits: [" + head + "]", "paths: [auth.go]"} {
		if !strings.Contains(buf, want) {
			t.Fatalf("checkout buffer missing %q:\n%s", want, buf)
		}
	}

	writeBuf(t, path, buf+"The captured fact.")
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "--apply", path, "--json"))
	if added.Title != "Prefilled note" || added.Body != "The captured fact." {
		t.Fatalf("title/body = %q/%q", added.Title, added.Body)
	}
	if strings.Join(added.Tags, ",") != "design" {
		t.Fatalf("tags = %v, want [design]", added.Tags)
	}
	got := map[string]string{}
	for _, a := range added.Anchors {
		got[a.Kind] = a.Value
	}
	if got["commit"] != head {
		t.Fatalf("commit anchor = %q, want the full sha %q", got["commit"], head)
	}
	if got["path"] != "auth.go" {
		t.Fatalf("path anchor = %q, want auth.go", got["path"])
	}
	if added.VerifiedAt == nil || added.VerifiedBy == nil || *added.VerifiedBy != actorA {
		t.Fatalf("created note not born-verified: %v/%v", added.VerifiedAt, added.VerifiedBy)
	}
}

func TestDocAddApplyAttach(t *testing.T) {
	dir := initRepo(t)
	att, oid := writeAttachable(t, "artifact.txt", []byte("attached payload"))
	path := mustCheckout(t, dir, "doc", "add", "--checkout")
	writeBuf(t, path, strings.Replace(readBuf(t, path), "title: \"\"", "title: With attachment", 1)+"The doc body.")

	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "--apply", path, "--attach", att, "--json"))
	if len(added.Attachments) != 1 || added.Attachments[0].Name != "artifact.txt" || added.Attachments[0].OID != oid {
		t.Fatalf("attachments = %+v, want one artifact.txt oid %s", added.Attachments, oid)
	}
	if added.Body != "The doc body." {
		t.Fatalf("body = %q, want the doc body", added.Body)
	}
	if bufExists(path) {
		t.Fatalf("buffer %s present after apply, want removed", path)
	}

	// Duplicate basenames in one invocation → UsageError, buffer kept.
	dup1, _ := writeAttachable(t, "same.txt", []byte("one"))
	dup2, _ := writeAttachable(t, "same.txt", []byte("two"))
	path2 := mustCheckout(t, dir, "doc", "add", "--checkout")
	writeBuf(t, path2, strings.Replace(readBuf(t, path2), "title: \"\"", "title: Dup", 1)+"body")
	_, _, err := runCLI(t, dir, "doc", "add", "--apply", path2, "--attach", dup1, "--attach", dup2)
	if cli.ExitCode(err) != 2 || !strings.Contains(err.Error(), "duplicate attachment name") {
		t.Fatalf("dup attach = %v (exit %d), want UsageError naming the duplicate", err, cli.ExitCode(err))
	}
	if !bufExists(path2) {
		t.Fatalf("buffer %s removed on rejected apply, want kept", path2)
	}

	// A rejected apply (empty body) with --attach leaves the buffer AND ingests
	// nothing: createOps fails before attachOps runs, so ok.txt never reaches LFS.
	path3 := mustCheckout(t, dir, "doc", "add", "--checkout")
	writeBuf(t, path3, strings.Replace(readBuf(t, path3), "title: \"\"", "title: Bodyless", 1))
	good, goodOID := writeAttachable(t, "ok.txt", []byte("never ingested"))
	_, _, err = runCLI(t, dir, "doc", "add", "--apply", path3, "--attach", good)
	if cli.ExitCode(err) != 2 || !strings.Contains(err.Error(), "doc body is empty") {
		t.Fatalf("bodyless apply+attach = %v (exit %d), want the doc-body UsageError", err, cli.ExitCode(err))
	}
	if !bufExists(path3) {
		t.Fatalf("buffer %s removed on rejected apply, want kept", path3)
	}
	artifactObj := strings.TrimSpace(mustRun(t, dir, "attachment", "path", added.ID, "artifact.txt"))
	objectsRoot := filepath.Dir(filepath.Dir(filepath.Dir(artifactObj)))
	goodObj := filepath.Join(objectsRoot, goodOID[:2], goodOID[2:4], goodOID)
	if _, err := os.Stat(goodObj); !os.IsNotExist(err) {
		t.Fatalf("ok.txt ingested into LFS at %s despite a rejected apply, want nothing ingested", goodObj)
	}
}

func TestDocEditAttach(t *testing.T) {
	dir := initRepo(t)
	doc := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Handoff", "--body", "orig", "--json"))
	first, firstOID := writeAttachable(t, "report.txt", []byte("first"))
	edited := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", doc.ID, "--attach", first, "--json"))
	if len(edited.Attachments) != 1 || edited.Attachments[0].OID != firstOID {
		t.Fatalf("attachments = %+v, want report.txt oid %s", edited.Attachments, firstOID)
	}

	// Re-attach the same name without --replace → collision error naming the file.
	second, secondOID := writeAttachable(t, "report.txt", []byte("second longer"))
	_, _, err := runCLI(t, dir, "doc", "edit", doc.ID, "--attach", second)
	if cli.ExitCode(err) != 2 || !strings.Contains(err.Error(), "report.txt") || !strings.Contains(cli.Hint(err), "--replace") {
		t.Fatalf("colliding edit = %v (exit %d, hint %q), want an exit-2 error naming report.txt with a --replace hint", err, cli.ExitCode(err), cli.Hint(err))
	}
	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", doc.ID, "--json"))
	if len(shown.Attachments) != 1 || shown.Attachments[0].OID != firstOID {
		t.Fatalf("attachments after rejected edit = %+v, want the original untouched", shown.Attachments)
	}

	// With --replace it succeeds.
	replaced := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", doc.ID, "--attach", second, "--replace", "--json"))
	if len(replaced.Attachments) != 1 || replaced.Attachments[0].OID != secondOID {
		t.Fatalf("attachments after --replace = %+v, want oid %s", replaced.Attachments, secondOID)
	}

	// --rm-attachment coexists with --attach in one invocation.
	third, thirdOID := writeAttachable(t, "extra.txt", []byte("extra"))
	both := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", doc.ID, "--rm-attachment", "report.txt", "--attach", third, "--json"))
	if len(both.Attachments) != 1 || both.Attachments[0].Name != "extra.txt" || both.Attachments[0].OID != thirdOID {
		t.Fatalf("attachments after rm+attach = %+v, want only extra.txt", both.Attachments)
	}
}

func TestNoteEditAttach(t *testing.T) {
	dir := initRepo(t)
	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Fact", "--body", "v1", "--json"))
	first, firstOID := writeAttachable(t, "report.txt", []byte("first"))
	edited := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", note.ID, "--attach", first, "--json"))
	if len(edited.Attachments) != 1 || edited.Attachments[0].OID != firstOID {
		t.Fatalf("attachments = %+v, want report.txt oid %s", edited.Attachments, firstOID)
	}

	second, secondOID := writeAttachable(t, "report.txt", []byte("second longer"))
	_, _, err := runCLI(t, dir, "note", "edit", note.ID, "--attach", second)
	if cli.ExitCode(err) != 2 || !strings.Contains(err.Error(), "report.txt") || !strings.Contains(cli.Hint(err), "--replace") {
		t.Fatalf("colliding edit = %v (exit %d, hint %q), want an exit-2 error naming report.txt with a --replace hint", err, cli.ExitCode(err), cli.Hint(err))
	}

	replaced := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", note.ID, "--attach", second, "--replace", "--json"))
	if len(replaced.Attachments) != 1 || replaced.Attachments[0].OID != secondOID {
		t.Fatalf("attachments after --replace = %+v, want oid %s", replaced.Attachments, secondOID)
	}

	third, thirdOID := writeAttachable(t, "extra.txt", []byte("extra"))
	both := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", note.ID, "--rm-attachment", "report.txt", "--attach", third, "--json"))
	if len(both.Attachments) != 1 || both.Attachments[0].Name != "extra.txt" || both.Attachments[0].OID != thirdOID {
		t.Fatalf("attachments after rm+attach = %+v, want only extra.txt", both.Attachments)
	}
}

// TestEditApplyShortenedCommitAnchorSurvives proves that re-spelling a commit
// anchor's full sha as a short prefix in an edit buffer is a no-op, not an
// anchor drop: DiffDoc emits add-short + remove-full, and apply must canonicalize
// both to the same sha and cancel them, leaving the anchor intact and committing
// nothing.
func TestEditApplyShortenedCommitAnchorSurvives(t *testing.T) {
	dir := initRepo(t)
	head := commitFile(t, dir, "seed.go", "package main")
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Anchored", "--body", "b", "--commit", "HEAD", "--json"))
	ref := "refs/cc-notes/docs/" + added.ID
	before := gittest.Git(t, dir, "rev-list", "--count", ref)

	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	replaceInBuf(t, path, "commits: ["+head+"]", "commits: ["+head[:7]+"]")

	_, stderr, err := runCLI(t, dir, "doc", "edit", added.ID, "--apply")
	if err != nil {
		t.Fatalf("apply of a re-spelled sha: %v (stderr %q)", err, stderr)
	}
	if !strings.Contains(stderr, "no changes") {
		t.Fatalf("stderr = %q, want a no-changes note (re-spelling a sha is not an edit)", stderr)
	}
	if after := gittest.Git(t, dir, "rev-list", "--count", ref); after != before {
		t.Fatalf("chain = %s commits, want unchanged %s (a spelling-only diff commits nothing)", after, before)
	}
	shown := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", added.ID, "--json"))
	var got string
	for _, a := range shown.Anchors {
		if a.Kind == "commit" {
			got = a.Value
		}
	}
	if got != head {
		t.Fatalf("commit anchor = %q, want the full sha %q to survive", got, head)
	}
}

// TestEditApplyGenuineCommitAnchorChange proves the cancellation is narrow: a
// buffer that swaps the anchor to a different commit (given as a short prefix)
// still applies, resolving the new value to its full sha.
func TestEditApplyGenuineCommitAnchorChange(t *testing.T) {
	dir := initRepo(t)
	first := commitFile(t, dir, "a.go", "package main")
	added := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Anchored", "--body", "b", "--commit", first, "--json"))
	second := commitFile(t, dir, "b.go", "package b")

	path := mustCheckout(t, dir, "doc", "edit", added.ID, "--checkout")
	replaceInBuf(t, path, "commits: ["+first+"]", "commits: ["+second[:8]+"]")

	applied := mustJSON[docJSON](t, mustRun(t, dir, "doc", "edit", added.ID, "--apply", "--json"))
	var got string
	for _, a := range applied.Anchors {
		if a.Kind == "commit" {
			got = a.Value
		}
	}
	if got != second {
		t.Fatalf("commit anchor = %q, want the new full sha %q (a genuine change applies)", got, second)
	}
}
