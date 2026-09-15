package cli_test

import (
	"os"
	"slices"
	"strings"
	"testing"
)

type answerSummaryJSON struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Tags           []string `json:"tags"`
	Author         string   `json:"author"`
	UpdatedAt      string   `json:"updated_at"`
	VerifiedCommit string   `json:"verified_commit"`
	SupersededBy   []string `json:"superseded_by"`
	Drift          string   `json:"drift"`
}

func answerSummaryIDs(answers []answerSummaryJSON) []string {
	out := make([]string, len(answers))
	for i, a := range answers {
		out[i] = a.ID
	}
	return out
}

func TestAnswerHookContract(t *testing.T) {
	dir := initRepo(t)
	head := commitFile(t, dir, "cache.go", "v1\n")
	question := "Which cache backend?"

	first := mustJSON[answerSummaryJSON](t, mustRun(t, dir, "answer", "add", "--json",
		"--label", "scope:durable", "--label", "header:Cache",
		"--branch", "main", "--path", "cache.go",
		"--body", "Redis\nOptions: Redis | Memcached", "--", question))
	if len(first.ID) != 40 || first.Title != question || first.Body != "Redis\nOptions: Redis | Memcached" {
		t.Fatalf("answer add ack = %+v, want a 40-char id, the question, and the body", first)
	}
	if !slices.Equal(first.Tags, []string{"header:Cache", "scope:durable"}) || first.VerifiedCommit != head {
		t.Fatalf("answer add ack tags/verified_commit = %v/%q, want both labels and %q", first.Tags, first.VerifiedCommit, head)
	}
	mustRun(t, dir, "answer", "add", "--json", "--label", "scope:ephemeral", "--body", "No", "--", "Run the slow suite?")
	second := mustJSON[answerSummaryJSON](t, mustRun(t, dir, "answer", "add", "--json",
		"--label", "scope:durable", "--body", "Memcached", "--", question))

	durable := mustJSON[[]answerSummaryJSON](t, mustRun(t, dir, "answer", "list", "--json", "--label", "scope:durable"))
	if len(durable) != 2 || !slices.Contains(answerSummaryIDs(durable), first.ID) || !slices.Contains(answerSummaryIDs(durable), second.ID) {
		t.Fatalf("durable list = %v, want exactly %s and %s", answerSummaryIDs(durable), first.ID, second.ID)
	}
	limited := mustJSON[[]answerSummaryJSON](t, mustRun(t, dir, "answer", "list", "--json", "--label", "scope:durable", "--limit", "1"))
	if !slices.Equal(answerSummaryIDs(limited), answerSummaryIDs(durable)[:1]) {
		t.Fatalf("--limit 1 = %v, want the head of the full list %v", answerSummaryIDs(limited), answerSummaryIDs(durable))
	}

	superseded := mustJSON[answerSummaryJSON](t, mustRun(t, dir, "answer", "supersede", first.ID, "--by", second.ID, "--json"))
	if !slices.Equal(superseded.SupersededBy, []string{second.ID}) {
		t.Fatalf("supersede ack superseded_by = %v, want [%s]", superseded.SupersededBy, second.ID)
	}
	live := mustJSON[[]answerSummaryJSON](t, mustRun(t, dir, "answer", "list", "--json", "--label", "scope:durable"))
	if !slices.Equal(answerSummaryIDs(live), []string{second.ID}) {
		t.Fatalf("live durable list = %v, want only the replacement %s", answerSummaryIDs(live), second.ID)
	}
	all := mustJSON[[]answerSummaryJSON](t, mustRun(t, dir, "answer", "list", "--json", "--label", "scope:durable", "--include-superseded"))
	if len(all) != 2 {
		t.Fatalf("--include-superseded = %v, want both durable answers", answerSummaryIDs(all))
	}

	mustRun(t, dir, "answer", "supersede", first.ID, "--by", second.ID, "--clear")
	relevant := mustJSON[[]struct {
		Kind    string             `json:"kind"`
		Answer  *answerSummaryJSON `json:"answer"`
		Reasons []string           `json:"reasons"`
	}](t, mustRun(t, dir, "relevant", "cache.go", "--json"))
	if len(relevant) == 0 || relevant[0].Kind != "answer" || relevant[0].Answer == nil ||
		relevant[0].Answer.ID != first.ID || relevant[0].Answer.Body != first.Body || !slices.Contains(relevant[0].Reasons, "path") {
		t.Fatalf("relevant cache.go = %+v, want the path-anchored answer first with its body", relevant)
	}

	hits := mustJSON[[]struct {
		Kind   string             `json:"kind"`
		Answer *answerSummaryJSON `json:"answer"`
	}](t, mustRun(t, dir, "search", "memcached", "--json"))
	var found []string
	for _, h := range hits {
		if h.Kind == "answer" {
			found = append(found, h.Answer.ID)
		}
	}
	if !slices.Contains(found, second.ID) {
		t.Fatalf("search memcached answer hits = %v, want %s", found, second.ID)
	}

	shown := mustJSON[struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}](t, mustRun(t, dir, "show", second.ID, "--json"))
	if shown.Title != question || shown.Body != "Memcached" {
		t.Fatalf("show = %+v, want the question and Memcached", shown)
	}
}

func TestAnswerFileModeCreate(t *testing.T) {
	dir := initRepo(t)
	path := strings.TrimSpace(mustRun(t, dir, "answer", "add", "--checkout", "--label", "scope:durable", "Deploy on Fridays?"))
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read buffer: %v", err)
	}
	if err := os.WriteFile(path, append(buf, []byte("No")...), 0o600); err != nil {
		t.Fatalf("write buffer: %v", err)
	}
	ack := mustJSON[answerSummaryJSON](t, mustRun(t, dir, "answer", "add", "--apply", path, "--json"))
	if ack.Title != "Deploy on Fridays?" || ack.Body != "No" || !slices.Equal(ack.Tags, []string{"scope:durable"}) {
		t.Fatalf("file-mode answer = %+v, want the question, body No, and scope:durable", ack)
	}
}

func TestDocumentListLimitKeepsMostRecent(t *testing.T) {
	dir := initRepo(t)
	for _, title := range []string{"one", "two", "three"} {
		mustRun(t, dir, "note", "add", title)
	}
	full := mustJSON[[]struct {
		ID string `json:"id"`
	}](t, mustRun(t, dir, "note", "list", "--json"))
	limited := mustJSON[[]struct {
		ID string `json:"id"`
	}](t, mustRun(t, dir, "note", "list", "--json", "--limit", "2"))
	if len(full) != 3 || !slices.Equal(limited, full[:2]) {
		t.Fatalf("note list --limit 2 = %v, want the first two of %v", limited, full)
	}
}
