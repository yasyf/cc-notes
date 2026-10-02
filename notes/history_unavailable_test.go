package notes

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

const (
	absentSHA          = "0123456789abcdef0123456789abcdef01234567"
	unresolvablePrefix = "21aab439"
)

func commitContent(t *testing.T, dir, path, content string) model.SHA {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	gittest.Git(t, dir, "add", path)
	gittest.Git(t, dir, "commit", "-q", "-m", "commit "+path)
	return model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
}

func witnessedNote(verifiedAt time.Time, witness ...model.AnchorWitness) model.Note {
	n := model.Note{VerifiedAt: verifiedAt.Unix()}
	for _, w := range witness {
		n.Anchors = append(n.Anchors, w.Anchor)
		n.Witness = append(n.Witness, w)
	}
	return n
}

func commitWitness(value string) model.AnchorWitness {
	return model.AnchorWitness{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: value}, OID: model.SHA(value)}
}

func pathWitness(path string, oid model.SHA) model.AnchorWitness {
	return model.AnchorWitness{Anchor: model.Anchor{Kind: model.AnchorPath, Value: path}, OID: oid}
}

type verdictCase struct {
	name       string
	note       model.Note
	staleAfter time.Duration
	worktree   bool
	want       Verdict
}

func checkVerdicts(t *testing.T, c *Client, cases []verdictCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			staleAfter := tc.staleAfter
			if staleAfter == 0 {
				staleAfter = time.Hour
			}
			got, err := c.NoteVerdict(t.Context(), tc.note, staleAfter, tc.worktree)
			if err != nil {
				t.Fatalf("NoteVerdict: %v", err)
			}
			if got != tc.want {
				t.Errorf("NoteVerdict = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVerdictHistoryUnavailable grafts a four-commit history at its third
// commit, every object still present, and pins each anchor shape against the
// truncated graph and then against the same handle once the graft is gone. An
// anchor the graph cannot decide is HISTORY-UNAVAILABLE; a proven change still
// wins, whichever anchor comes first; an orphan head whose own walk never meets
// the boundary, and a complete graph, prove the answer.
func TestVerdictHistoryUnavailable(t *testing.T) {
	c, dir := newWBClient(t)
	c1 := commitContent(t, dir, "a.go", "v1\n")
	commitContent(t, dir, "b.go", "v1\n")
	c3 := commitContent(t, dir, "c.go", "v1\n")
	gittest.Git(t, dir, "checkout", "-q", "-b", "side", string(c1))
	sibling := commitContent(t, dir, "side.go", "v1\n")
	gittest.Git(t, dir, "checkout", "-q", "main")
	c4 := commitContent(t, dir, "d.go", "v1\n")
	blob := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD:a.go"))
	now := time.Now()

	gittest.Shallow(t, dir, string(c3))
	checkVerdicts(t, c, []verdictCase{
		{name: "grafted: commit beyond the boundary", note: witnessedNote(now, commitWitness(string(c1))), want: VerdictHistoryUnavailable},
		{name: "grafted: commit inside the window", note: witnessedNote(now, commitWitness(string(c4))), want: ""},
		{name: "grafted: the boundary commit", note: witnessedNote(now, commitWitness(string(c3))), want: ""},
		{name: "grafted: unknown commit, then path drift", note: witnessedNote(now, commitWitness(string(c1)), pathWitness("a.go", absentSHA)), want: VerdictDrifted},
		{name: "grafted: path drift, then unknown commit", note: witnessedNote(now, pathWitness("a.go", absentSHA), commitWitness(string(c1))), want: VerdictDrifted},
		{name: "grafted: unknown commit beside an intact path", note: witnessedNote(now, commitWitness(string(c1)), pathWitness("a.go", blob)), want: VerdictHistoryUnavailable},
		{name: "grafted: unresolvable abbreviation", note: witnessedNote(now, commitWitness(unresolvablePrefix)), want: VerdictHistoryUnavailable},
		{name: "grafted: absent full sha", note: witnessedNote(now, commitWitness(absentSHA)), want: VerdictHistoryUnavailable},
		{name: "grafted: outranks STALE", note: witnessedNote(now.Add(-2*time.Hour), commitWitness(string(c1))), want: VerdictHistoryUnavailable},
		{name: "grafted: EXPIRED outranks it", note: func() model.Note {
			n := witnessedNote(now, commitWitness(string(c1)))
			n.StaleAt = now.Unix()
			return n
		}(), want: VerdictExpired},
	})

	gittest.Git(t, dir, "checkout", "-q", "--orphan", "lone")
	gittest.Git(t, dir, "commit", "-q", "-m", "lone")
	checkVerdicts(t, c, []verdictCase{
		{name: "grafted, orphan head: commit beyond the boundary", note: witnessedNote(now, commitWitness(string(c1))), want: VerdictDrifted},
		{name: "grafted, orphan head: unresolvable abbreviation", note: witnessedNote(now, commitWitness(unresolvablePrefix)), want: VerdictDrifted},
		{name: "grafted, orphan head: absent full sha", note: witnessedNote(now, commitWitness(absentSHA)), want: VerdictDrifted},
	})
	gittest.Git(t, dir, "checkout", "-q", "main")

	gittest.Unshallow(t, dir)
	checkVerdicts(t, c, []verdictCase{
		{name: "complete: the old commit is proven reachable", note: witnessedNote(now, commitWitness(string(c1))), want: ""},
		{name: "complete: a sibling branch commit is proven drift", note: witnessedNote(now, commitWitness(string(sibling))), want: VerdictDrifted},
		{name: "complete: unresolvable abbreviation", note: witnessedNote(now, commitWitness(unresolvablePrefix)), want: VerdictDrifted},
		{name: "complete: absent full sha", note: witnessedNote(now, commitWitness(absentSHA)), want: VerdictDrifted},
		{name: "complete: stale once proven", note: witnessedNote(now.Add(-2*time.Hour), commitWitness(string(c1))), want: VerdictStale},
	})
}

// TestVerdictUnbornHeadWorktree checks a commit anchor against an orphan
// branch's unborn HEAD in worktree mode: there is no head to walk from, so the
// anchor is skipped rather than handed to the graph walk as an empty sha.
func TestVerdictUnbornHeadWorktree(t *testing.T) {
	c, dir := newWBClient(t)
	anchored := commitContent(t, dir, "a.go", "v1\n")
	gittest.Git(t, dir, "checkout", "-q", "--orphan", "fresh")
	now := time.Now()
	checkVerdicts(t, c, []verdictCase{
		{name: "worktree: commit anchor skipped", note: witnessedNote(now, commitWitness(string(anchored))), worktree: true, want: ""},
		{name: "committed view: drift skipped", note: witnessedNote(now, commitWitness(string(anchored))), want: ""},
	})
}

// TestStatusNeedsReviewSkipsHistoryUnavailable creates a note, a doc, and an
// answer on an old commit plus one note on a path, grafts the history past the
// old commit, and changes the path. The reviews flag all four; only the proven
// drift needs review.
func TestStatusNeedsReviewSkipsHistoryUnavailable(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	old := commitContent(t, dir, "a.go", "v1\n")
	commitContent(t, dir, "b.go", "v1\n")
	boundary := commitContent(t, dir, "c.go", "v1\n")
	onOld := AnchorSpec{Commits: []string{string(old)}}

	unknownNote, _, err := c.CreateNote(ctx, NoteSpec{Title: "old commit", Body: "b", Anchors: onOld})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	driftedNote, _, err := c.CreateNote(ctx, NoteSpec{Title: "path", Body: "b", Anchors: AnchorSpec{Paths: []string{"c.go"}}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	unknownDoc, _, err := c.CreateDoc(ctx, DocSpec{Title: "old commit", Body: "b", Anchors: onOld})
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	unknownAnswer, _, err := c.CreateAnswer(ctx, NoteSpec{Title: "old commit?", Body: "b", Anchors: onOld})
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	gittest.Shallow(t, dir, string(boundary))
	commitContent(t, dir, "c.go", "v2\n")

	noteReviews, err := c.ReviewNotes(ctx, time.Hour)
	if err != nil {
		t.Fatalf("ReviewNotes: %v", err)
	}
	gotNotes := map[model.EntityID]Verdict{}
	for _, r := range noteReviews {
		gotNotes[r.Note.ID] = r.Verdict
	}
	wantNotes := map[model.EntityID]Verdict{unknownNote.ID: VerdictHistoryUnavailable, driftedNote.ID: VerdictDrifted}
	if len(gotNotes) != len(wantNotes) || gotNotes[unknownNote.ID] != wantNotes[unknownNote.ID] || gotNotes[driftedNote.ID] != wantNotes[driftedNote.ID] {
		t.Fatalf("ReviewNotes verdicts = %v, want %v", gotNotes, wantNotes)
	}
	docReviews, err := c.ReviewDocs(ctx, time.Hour)
	if err != nil {
		t.Fatalf("ReviewDocs: %v", err)
	}
	if len(docReviews) != 1 || docReviews[0].Doc.ID != unknownDoc.ID || docReviews[0].Verdict != VerdictHistoryUnavailable {
		t.Fatalf("ReviewDocs = %+v, want %s HISTORY-UNAVAILABLE", docReviews, unknownDoc.ID)
	}
	answerReviews, err := c.ReviewAnswers(ctx, time.Hour)
	if err != nil {
		t.Fatalf("ReviewAnswers: %v", err)
	}
	if len(answerReviews) != 1 || answerReviews[0].Answer.ID != unknownAnswer.ID || answerReviews[0].Verdict != VerdictHistoryUnavailable {
		t.Fatalf("ReviewAnswers = %+v, want %s HISTORY-UNAVAILABLE", answerReviews, unknownAnswer.ID)
	}

	report, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := map[string]SummaryCount{
		"notes":   {Total: 2, NeedsReview: 1},
		"docs":    {Total: 1, NeedsReview: 0},
		"answers": {Total: 1, NeedsReview: 0},
	}
	got := map[string]SummaryCount{"notes": report.Notes, "docs": report.Docs, "answers": report.Answers}
	for kind, w := range want {
		if got[kind] != w {
			t.Errorf("Status %s = %+v, want %+v", kind, got[kind], w)
		}
	}
}

// TestVerdictBoundReadsContext binds a depth-1 clone that has one commit of
// its own to the full source it was cloned from, after the source moved on,
// and checks every context read the verdict path makes: HEAD, path witnesses
// and live oids, commit resolution, ancestry, and the shallow probe all come
// from the clone, while the sync remote comes from the records repository.
func TestVerdictBoundReadsContext(t *testing.T) {
	source := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	oldSource := commitContent(t, source, "a.go", "v1\n")
	commitContent(t, source, "b.go", "v1\n")
	commitContent(t, source, "a.go", "v2\n")
	thin := gittest.ShallowClone(t, source, 1)
	thinOnly := commitContent(t, thin, "a.go", "thin\n")
	sourceHead := commitContent(t, source, "a.go", "source later\n")
	thinBlob := model.SHA(gittest.Git(t, thin, "rev-parse", "HEAD:a.go"))
	sourceBlob := model.SHA(gittest.Git(t, source, "rev-parse", "HEAD:a.go"))

	gittest.Git(t, source, "remote", "add", "up", "https://example.com/x.git")
	if _, err := ccsync.Install(t.Context(), gitcmd.Git{Dir: source}, "up"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := store.Bind(t.Context(), thin, source); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	c, err := Open(thin)
	if err != nil {
		t.Fatalf("Open(%s): %v", thin, err)
	}
	if _, bound := c.s.Binding(); !bound {
		t.Fatal("fixture invalid: the clone opened unbound")
	}
	ctx := t.Context()

	head, err := c.head(ctx)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head != thinOnly {
		t.Fatalf("head = %s, want the clone's HEAD %s (source HEAD is %s)", head, thinOnly, sourceHead)
	}
	witness, err := c.buildWitness(ctx, head, []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}})
	if err != nil {
		t.Fatalf("buildWitness: %v", err)
	}
	if len(witness) != 1 || witness[0].OID != thinBlob {
		t.Fatalf("buildWitness = %+v, want a.go at the clone's blob %s", witness, thinBlob)
	}
	remote, err := c.deriveRemote(ctx)
	if err != nil {
		t.Fatalf("deriveRemote: %v", err)
	}
	if remote != "up" {
		t.Errorf("deriveRemote = %q, want the records repository's wired remote up", remote)
	}

	now := time.Now()
	checkVerdicts(t, c, []verdictCase{
		{name: "clone-only commit is reachable", note: witnessedNote(now, commitWitness(string(thinOnly))), want: ""},
		{name: "old source commit past the clone's depth", note: witnessedNote(now, commitWitness(string(oldSource))), want: VerdictHistoryUnavailable},
		{name: "source commit the clone never saw", note: witnessedNote(now, commitWitness(string(sourceHead))), want: VerdictHistoryUnavailable},
		{name: "unresolvable abbreviation in the shallow clone", note: witnessedNote(now, commitWitness(unresolvablePrefix)), want: VerdictHistoryUnavailable},
		{name: "path witnessed at the clone's blob", note: witnessedNote(now, pathWitness("a.go", thinBlob)), want: ""},
		{name: "path witnessed at the source's blob", note: witnessedNote(now, pathWitness("a.go", sourceBlob)), want: VerdictDrifted},
	})
}
