package cli_test

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
)

// searchHitJSON mirrors the top-level search DTO: the kind discriminator plus
// exactly one populated entity field.
type searchHitJSON struct {
	Kind    string    `json:"kind"`
	Note    *noteJSON `json:"note"`
	Doc     *docJSON  `json:"doc"`
	Log     *logJSON  `json:"log"`
	Runbook *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"runbook"`
}

// searchFixture seeds one entity per kind, all matching "token" at distinct
// tiers where possible: the note in its title (tier 3), the doc in a label
// (tier 2), the log in an entry and the runbook in its description (tier 1).
func searchFixture(t *testing.T) (dir string, note noteJSON, doc docJSON, log logJSON, runbookID string) {
	t.Helper()
	dir = initRepo(t)
	note = mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "token rotation plan", "--label", "infra", "--path", "auth.go", "--json"))
	doc = mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", "Auth guide", "--body", "b", "--label", "token", "--json"))
	log = mustJSON[logJSON](t, mustRun(t, dir, "log", "add", "ops journal", "--entry", "rotated the token today", "--json"))
	rb := mustJSON[struct {
		ID string `json:"id"`
	}](t, mustRun(t, dir, "runbook", "add", "deploy service", "--body", "rotate the token safely", "--label", "infra", "--json"))
	return dir, note, doc, log, rb.ID
}

// TestGlobalSearchMergesKinds proves the top-level search fans out across every
// kind and merges kind-tagged: tier order puts the title hit (note) before the
// label hit (doc) before the body hits (log, runbook); the two same-tier body
// hits land in the final positions in either order.
func TestGlobalSearchMergesKinds(t *testing.T) {
	dir, note, doc, log, rbID := searchFixture(t)

	hits := mustJSON[[]searchHitJSON](t, mustRun(t, dir, "search", "token", "--json"))
	if len(hits) != 4 {
		t.Fatalf("search token returned %d hits, want 4: %+v", len(hits), hits)
	}
	if hits[0].Kind != "note" || hits[0].Note == nil || hits[0].Note.ID != note.ID {
		t.Errorf("hit 0 = %+v, want the title-tier note %s", hits[0], note.ID)
	}
	if hits[0].Doc != nil || hits[0].Log != nil || hits[0].Runbook != nil {
		t.Errorf("hit 0 populates a foreign entity field: %+v", hits[0])
	}
	if hits[1].Kind != "doc" || hits[1].Doc == nil || hits[1].Doc.ID != doc.ID {
		t.Errorf("hit 1 = %+v, want the label-tier doc %s", hits[1], doc.ID)
	}
	got := map[string]string{}
	for _, h := range hits[2:] {
		switch {
		case h.Kind == "log" && h.Log != nil:
			got["log"] = h.Log.ID
		case h.Kind == "runbook" && h.Runbook != nil:
			got["runbook"] = h.Runbook.ID
		default:
			t.Errorf("body-tier hit = %+v, want a populated log or runbook", h)
		}
	}
	if got["log"] != log.ID || got["runbook"] != rbID {
		t.Errorf("body-tier hits = %v, want log %s and runbook %s", got, log.ID, rbID)
	}

	out := mustRun(t, dir, "search", "token")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("lean output has %d lines, want 4:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "note\t") || !strings.Contains(lines[0], "token rotation plan") {
		t.Errorf("lean line 0 = %q, want a note\\t-tagged line with the note title", lines[0])
	}
	if !strings.HasPrefix(lines[1], "doc\t") || !strings.Contains(lines[1], "Auth guide") {
		t.Errorf("lean line 1 = %q, want a doc\\t-tagged line with the doc title", lines[1])
	}
	tags := map[string]bool{}
	for _, line := range lines[2:] {
		kind, _, ok := strings.Cut(line, "\t")
		if !ok {
			t.Errorf("lean line %q has no kind tag column", line)
			continue
		}
		tags[kind] = true
	}
	if !tags["log"] || !tags["runbook"] {
		t.Errorf("body-tier lean tags = %v, want log and runbook", tags)
	}
}

// TestGlobalSearchLimit pins the merged-set cap: --limit truncates after the
// interleave (top tiers survive) and --limit 0 means all.
func TestGlobalSearchLimit(t *testing.T) {
	dir, note, doc, _, _ := searchFixture(t)

	limited := mustJSON[[]searchHitJSON](t, mustRun(t, dir, "search", "token", "--limit", "2", "--json"))
	if len(limited) != 2 || limited[0].Note == nil || limited[0].Note.ID != note.ID || limited[1].Doc == nil || limited[1].Doc.ID != doc.ID {
		t.Fatalf("search --limit 2 = %+v, want exactly the note then the doc", limited)
	}

	all := mustJSON[[]searchHitJSON](t, mustRun(t, dir, "search", "token", "--limit", "0", "--json"))
	if len(all) != 4 {
		t.Fatalf("search --limit 0 returned %d hits, want all 4", len(all))
	}
}

// TestGlobalSearchFilters proves --label and the anchor filters apply uniformly
// across kinds: each keeps only the entities carrying the label or anchor.
func TestGlobalSearchFilters(t *testing.T) {
	dir, note, _, _, rbID := searchFixture(t)

	labeled := mustJSON[[]searchHitJSON](t, mustRun(t, dir, "search", "token", "--label", "infra", "--json"))
	if len(labeled) != 2 || labeled[0].Note == nil || labeled[0].Note.ID != note.ID || labeled[1].Runbook == nil || labeled[1].Runbook.ID != rbID {
		t.Fatalf("search --label infra = %+v, want the note then the runbook", labeled)
	}

	anchored := mustJSON[[]searchHitJSON](t, mustRun(t, dir, "search", "token", "--path", "auth.go", "--json"))
	if len(anchored) != 1 || anchored[0].Note == nil || anchored[0].Note.ID != note.ID {
		t.Fatalf("search --path auth.go = %+v, want only the note", anchored)
	}
}

// TestGlobalSearchNoHits proves a query matching nothing yields empty output,
// consistent with a per-kind search: no lean lines and an empty JSON set.
func TestGlobalSearchNoHits(t *testing.T) {
	dir, _, _, _, _ := searchFixture(t)

	global := mustRun(t, dir, "search", "zzzznomatch")
	perKind := mustRun(t, dir, "note", "search", "zzzznomatch")
	if global != "" || perKind != "" {
		t.Fatalf("no-hit lean output: global=%q per-kind=%q, want both empty", global, perKind)
	}

	hits := mustJSON[[]searchHitJSON](t, mustRun(t, dir, "search", "zzzznomatch", "--json"))
	if len(hits) != 0 {
		t.Fatalf("no-hit --json = %+v, want an empty set", hits)
	}
}

// TestGlobalSearchArity pins that the top-level search resolves as a real
// command (no more noun-scoped redirect hint) and a missing QUERY is a usage
// error.
func TestGlobalSearchArity(t *testing.T) {
	dir := initRepo(t)

	_, _, err := runCLI(t, dir, "search")
	if err == nil {
		t.Fatal("bare 'search' succeeded, want an arity usage error")
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("exit = %d, want 2 (usage); err = %v", got, err)
	}
	if strings.Contains(err.Error(), "noun-scoped") {
		t.Fatalf("err = %q, want the search arity error, not the removed rootNounVerbs hint", err.Error())
	}
	if !strings.Contains(err.Error(), "accepts 1 arg(s), received 0") {
		t.Fatalf("err = %q, want the exactArgs arity message", err.Error())
	}
}
