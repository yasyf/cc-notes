package cli_test

import (
	"testing"
)

// TestPerKindSearchLimitZeroReturnsAll pins the per-kind search "0 = all"
// contract: with two matching entities seeded, "<kind> search Q --limit 0"
// returns both, while --limit 1 caps to the top hit. Reverting the CLI's 0→-1
// mapping makes SearchFilter's literal 0 truncate to zero results, so --limit 0
// would return none and this test fails.
func TestPerKindSearchLimitZeroReturnsAll(t *testing.T) {
	dir := initRepo(t)
	// Two entities per kind, each matching "token" in its title (tier 3).
	mustRun(t, dir, "note", "add", "token rotation one", "--json")
	mustRun(t, dir, "note", "add", "token rotation two", "--json")
	mustRun(t, dir, "doc", "add", "token guide one", "--body", "b", "--json")
	mustRun(t, dir, "doc", "add", "token guide two", "--body", "b", "--json")
	mustRun(t, dir, "log", "add", "token journal one", "--entry", "e", "--json")
	mustRun(t, dir, "log", "add", "token journal two", "--entry", "e", "--json")

	for _, kind := range []string{"note", "doc", "log"} {
		all := mustJSON[[]struct {
			ID string `json:"id"`
		}](t, mustRun(t, dir, kind, "search", "token", "--limit", "0", "--json"))
		if len(all) != 2 {
			t.Errorf("%s search --limit 0 returned %d hits, want all 2", kind, len(all))
		}

		capped := mustJSON[[]struct {
			ID string `json:"id"`
		}](t, mustRun(t, dir, kind, "search", "token", "--limit", "1", "--json"))
		if len(capped) != 1 {
			t.Errorf("%s search --limit 1 returned %d hits, want 1 (capped)", kind, len(capped))
		}
	}
}
