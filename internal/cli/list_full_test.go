package cli_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

var derivedShowFields = []string{"supersedes", "live_head", "drift", "attachments"}

func extendChain(t *testing.T, dir, label string, chain []string, n int) []string {
	t.Helper()
	for range n {
		i := len(chain)
		id := jsonID(t, mustRun(t, dir, "doc", "add", fmt.Sprintf("Step %d", i), "--body", fmt.Sprintf("body %d", i), "--label", label, "--json"))
		if i > 0 {
			mustRun(t, dir, "doc", "supersede", chain[i-1], "--by", id)
		}
		chain = append(chain, id)
	}
	return chain
}

func listRecords(t *testing.T, dir string, args ...string) []map[string]any {
	t.Helper()
	return mustJSON[[]map[string]any](t, mustRun(t, dir, slices.Concat([]string{"doc", "list"}, args, []string{"--full", "--json"})...))
}

func recordIDs(records []map[string]any) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r["id"].(string)
	}
	return out
}

func TestDocListFullMatchesShowRecords(t *testing.T) {
	dir := initRepo(t)
	chain := extendChain(t, dir, "progress:x", nil, 3)
	mustRun(t, dir, "doc", "edit", chain[2], "--append", "appended")
	gone := jsonID(t, mustRun(t, dir, "doc", "add", "Gone", "--body", "x", "--label", "progress:x", "--json"))
	mustRun(t, dir, "doc", "supersede", gone, "--by", chain[2])
	mustRun(t, dir, "doc", "rm", gone)
	mustRun(t, dir, "doc", "add", "Other", "--body", "x", "--label", "other")

	summary := docIDs(mustJSON[[]docJSON](t, mustRun(t, dir, "doc", "list", "--label", "progress:x", "--include-superseded", "--json")))
	records := listRecords(t, dir, "--label", "progress:x", "--include-superseded")
	if got := recordIDs(records); !slices.Equal(got, summary) {
		t.Fatalf("--full ids = %v, want the summary listing %v", got, summary)
	}
	if got, want := slices.Sorted(slices.Values(summary)), slices.Sorted(slices.Values(chain)); !slices.Equal(got, want) {
		t.Fatalf("listing = %v, want exactly the live chain %v", got, want)
	}

	for _, rec := range records {
		id := rec["id"].(string)
		show := mustJSON[map[string]any](t, mustRun(t, dir, "doc", "show", id, "--json"))
		for _, field := range derivedShowFields {
			if _, ok := rec[field]; ok {
				t.Errorf("record %s carries derived %q: %v", id[:7], field, rec[field])
			}
			delete(show, field)
		}
		if !reflect.DeepEqual(rec, show) {
			t.Errorf("record %s = %v, want show minus derived fields %v", id[:7], rec, show)
		}
	}

	byID := make(map[string]map[string]any, len(records))
	for _, rec := range records {
		byID[rec["id"].(string)] = rec
	}
	for i, id := range chain[:2] {
		if got := byID[id]["superseded_by"]; !reflect.DeepEqual(got, []any{chain[i+1]}) {
			t.Errorf("superseded_by of step %d = %v, want [%s]", i, got, chain[i+1])
		}
	}
	if _, ok := byID[chain[2]]["superseded_by"]; ok {
		t.Errorf("head carries superseded_by %v, want none", byID[chain[2]]["superseded_by"])
	}
	if body := byID[chain[2]]["body"].(string); !strings.HasSuffix(body, "appended") {
		t.Errorf("head body = %q, want the appended fold", body)
	}
	if created := byID[chain[0]]["created_at"]; created == nil || created == "" {
		t.Errorf("record created_at = %v, want a timestamp", created)
	}
	if supersedes := mustJSON[docJSON](t, mustRun(t, dir, "doc", "show", chain[2], "--json")).Supersedes; !slices.Equal(supersedes, []string{chain[1]}) {
		t.Errorf("show head supersedes = %v, want [%s] without the tombstoned doc", supersedes, chain[1])
	}

	if got := recordIDs(listRecords(t, dir, "--label", "progress:x")); !slices.Equal(got, []string{chain[2]}) {
		t.Errorf("default --full = %v, want only the live head %s", got, chain[2])
	}
	all := listRecords(t, dir, "--label", "progress:x", "--include-superseded", "--all")
	if i := slices.Index(recordIDs(all), gone); i < 0 || all[i]["deleted"] != true {
		t.Errorf("--all --full = %v, want the tombstoned %s marked deleted", recordIDs(all), gone)
	}
}

func TestListFullRecordKinds(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "note", "add", "N", "--body", "note body")
	mustRun(t, dir, "answer", "add", "--body", "Yes", "--", "Ship it?")
	for _, kind := range []string{"note", "answer"} {
		records := mustJSON[[]map[string]any](t, mustRun(t, dir, kind, "list", "--full", "--json"))
		if len(records) != 1 || records[0]["body"] == nil || records[0]["created_at"] == nil {
			t.Errorf("%s list --full = %v, want one record with body and created_at", kind, records)
		}
	}
}

func TestListFullRequiresJSON(t *testing.T) {
	dir := initRepo(t)
	for _, kind := range []string{"note", "doc", "answer"} {
		_, _, err := runCLI(t, dir, kind, "list", "--full")
		if cli.ExitCode(err) != 2 {
			t.Errorf("%s list --full = %v (exit %d), want a usage error", kind, err, cli.ExitCode(err))
		}
	}
}

type gitTrace string

func traceGit(t *testing.T) gitTrace {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath git: %v", err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf '%s\\037' \"$@\" >> \"$CC_NOTES_GIT_TRACE\"\nprintf '\\n' >> \"$CC_NOTES_GIT_TRACE\"\nexec \"$CC_NOTES_REAL_GIT\" \"$@\"\n"), 0o700); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	trace := gitTrace(filepath.Join(t.TempDir(), "git.log"))
	t.Setenv("CC_NOTES_REAL_GIT", realGit)
	t.Setenv("CC_NOTES_GIT_TRACE", string(trace))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return trace
}

func (g gitTrace) of(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	if err := os.WriteFile(string(g), nil, 0o600); err != nil {
		t.Fatalf("reset git trace: %v", err)
	}
	mustRun(t, dir, args...)
	data, err := os.ReadFile(string(g))
	if err != nil {
		t.Fatalf("read git trace: %v", err)
	}
	return slices.Sorted(slices.Values(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")))
}

func docEnumerations(lines []string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, "for-each-ref\x1f") && strings.Contains(line, refs.Root(model.KindDoc)) {
			n++
		}
	}
	return n
}

func TestDocListFullReadsOneListing(t *testing.T) {
	dir := initRepo(t)
	chain := extendChain(t, dir, "progress:x", nil, 2)
	trace := traceGit(t)
	summary := []string{"doc", "list", "--label", "progress:x", "--include-superseded", "--json"}
	full := slices.Concat(summary, []string{"--full"})
	mustRun(t, dir, summary...)

	short := trace.of(t, dir, full...)
	if n := docEnumerations(short); n != 1 {
		t.Fatalf("doc list --full enumerated the docs namespace %d times, want 1: %q", n, short)
	}
	if got := trace.of(t, dir, summary...); !slices.Equal(short, got) {
		t.Fatalf("--full git calls %q differ from the summary listing's %q", short, got)
	}

	extendChain(t, dir, "progress:x", chain, 4)
	if long := trace.of(t, dir, full...); !slices.Equal(long, short) {
		t.Fatalf("git calls grew with the chain: 2 docs %q, 6 docs %q", short, long)
	}
	if show := trace.of(t, dir, "doc", "show", chain[1], "--json"); docEnumerations(show) <= 1 {
		t.Fatalf("doc show enumerated the docs namespace %d times, want the per-record reverse walk this listing replaces", docEnumerations(show))
	}
}
