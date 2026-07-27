package stale

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// retrieveFixture builds a corpus holding one superseded note, its live
// successor, and one note whose witnessed anchor has drifted — the three states
// the retrieval policy has to tell apart.
type retrieveFixture struct {
	corpus            []eval.Entity
	as                []Assessment
	old, fresh, drift model.EntityID
}

func newRetrieveFixture(t *testing.T) retrieveFixture {
	t.Helper()
	c, dir := openRepo(t)
	old, fresh := supersededPair(t, c)
	drift := verifiedNote(t, c, "Spin returns one from pkg widget", "pkg/widget.go")
	writeFile(t, dir, "pkg/widget.go", widgetSource+"\nfunc (w Widget) Stop() {}\n")
	gittest.Git(t, dir, "commit", "-qam", "rewrite the widget")

	corpus, err := eval.LoadCorpus(t.Context(), c)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	return retrieveFixture{corpus: corpus, as: assess(t, c, dir, testPolicy(time.Now())), old: old, fresh: fresh, drift: drift}
}

func (f retrieveFixture) retrieve(t *testing.T, p Retrieval, query string) []eval.Result {
	t.Helper()
	r := NewRanker(f.corpus, f.as, p).Wrap(eval.NewBM25(f.corpus, 1), 1)
	got, err := r.Retrieve(t.Context(), query, 10)
	if err != nil {
		t.Fatalf("Retrieve(%q): %v", query, err)
	}
	return got
}

func ids(rs []eval.Result) []model.EntityID {
	out := make([]model.EntityID, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestDefaultRetrievalWithholdsSupersededAndKeepsDrifted(t *testing.T) {
	f := newRetrieveFixture(t)
	got := ids(f.retrieve(t, DefaultRetrieval(), "Spin returns one"))
	if slices.Contains(got, f.old) {
		t.Errorf("superseded %s was returned; the corpus holds its successor", f.old.Short())
	}
	if !slices.Contains(got, f.fresh) {
		t.Errorf("successor %s missing from %v", f.fresh.Short(), got)
	}
	if !slices.Contains(got, f.drift) {
		t.Errorf("drifted %s withheld; a moved anchor does not falsify content", f.drift.Short())
	}
}

// TestStrictRetrievalWithholdsTheDriftedRecord pins the over-suppression the
// question set's counter-test punishes: withholding on every gate signal drops
// a record whose content is still correct.
func TestStrictRetrievalWithholdsTheDriftedRecord(t *testing.T) {
	f := newRetrieveFixture(t)
	got := ids(f.retrieve(t, StrictRetrieval(), "Spin returns one"))
	if slices.Contains(got, f.drift) {
		t.Fatalf("drifted %s survived StrictRetrieval; the strict policy is meant to withhold it", f.drift.Short())
	}
	if slices.Contains(got, f.old) {
		t.Errorf("superseded %s survived StrictRetrieval", f.old.Short())
	}
}

func TestDemoteRetrievalRanksDriftedBelowClean(t *testing.T) {
	f := newRetrieveFixture(t)
	plain := f.retrieve(t, DefaultRetrieval(), "Spin returns one")
	demoted := f.retrieve(t, DemoteRetrieval(), "Spin returns one")
	before, after := scoreOf(t, plain, f.drift), scoreOf(t, demoted, f.drift)
	if want := before * GateDemotion; math.Abs(after-want) > 1e-9 {
		t.Errorf("drifted score = %.6f, want %.6f (one half-life below %.6f)", after, want, before)
	}
}

func scoreOf(t *testing.T, rs []eval.Result, id model.EntityID) float64 {
	t.Helper()
	for _, r := range rs {
		if r.ID == id {
			return r.Score
		}
	}
	t.Fatalf("%s not in %v", id.Short(), ids(rs))
	return 0
}

// TestCalibrationIdealIsQueryLengthIndependent is the property raw BM25 lacks:
// repeating a query's terms leaves the calibrated score put, because both the
// raw score and the ideal grow with the query.
func TestCalibrationIdealIsQueryLengthIndependent(t *testing.T) {
	f := newRetrieveFixture(t)
	short := f.retrieve(t, DefaultRetrieval(), "Spin")
	long := f.retrieve(t, DefaultRetrieval(), "Spin Spin Spin")
	if len(short) == 0 {
		t.Fatal("no results for the short query")
	}
	if a, b := scoreOf(t, short, short[0].ID), scoreOf(t, long, short[0].ID); math.Abs(a-b) > 1e-9 {
		t.Errorf("calibrated score moved with a repeated term: %.6f vs %.6f", a, b)
	}
	raw := eval.NewBM25(f.corpus, 1)
	one, err := raw.Retrieve(t.Context(), "Spin", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	three, err := raw.Retrieve(t.Context(), "Spin Spin Spin", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if one[0].Score == three[0].Score {
		t.Error("raw BM25 did not move with query length; the calibration test proves nothing")
	}
}

func TestCalibrationIdealMatchesSumOfIDF(t *testing.T) {
	f := newRetrieveFixture(t)
	cal := NewCalibration(f.corpus)
	n := float64(len(f.corpus))
	df := 0
	for _, e := range f.corpus {
		if slices.Contains(eval.Tokenize(e.Text()), "spin") {
			df++
		}
	}
	want := 3 * math.Log(1+(n-float64(df)+0.5)/(float64(df)+0.5))
	if got := cal.Ideal("Spin spin SPIN"); math.Abs(got-want) > 1e-9 {
		t.Errorf("Ideal = %.6f, want %.6f — one idf per term occurrence", got, want)
	}
}

// TestRetrieveAbstainsOnUnknownVocabulary is the one abstention a lexical
// retriever can decide: the corpus has never seen a word of the query.
func TestRetrieveAbstainsOnUnknownVocabulary(t *testing.T) {
	f := newRetrieveFixture(t)
	if got := f.retrieve(t, DefaultRetrieval(), "zzquux flimbrex"); len(got) != 0 {
		t.Errorf("Retrieve = %v, want abstention on vocabulary the corpus lacks", ids(got))
	}
}

func TestRetrieveTagsTheLaneWithTheSignal(t *testing.T) {
	f := newRetrieveFixture(t)
	for _, r := range f.retrieve(t, DefaultRetrieval(), "Spin returns one") {
		if r.ID != f.drift {
			continue
		}
		if want := eval.BM25Lane + "/" + string(SignalDrift); r.Lane != want {
			t.Errorf("lane = %q, want %q", r.Lane, want)
		}
		return
	}
	t.Fatalf("drifted %s missing from the results", f.drift.Short())
}

func TestDecayRetrievalAppliesThePenaltyProduct(t *testing.T) {
	f := newRetrieveFixture(t)
	plain := f.retrieve(t, DefaultRetrieval(), "Spin returns the tick count")
	decayed := f.retrieve(t, DecayRetrieval(), "Spin returns the tick count")
	a := find(t, f.as, f.fresh)
	if a.Weight == 1 {
		t.Skip("the successor carries no penalty in this fixture")
	}
	if want := scoreOf(t, plain, f.fresh) * a.Weight; math.Abs(scoreOf(t, decayed, f.fresh)-want) > 1e-9 {
		t.Errorf("decayed score = %.6f, want %.6f", scoreOf(t, decayed, f.fresh), want)
	}
}

func TestRetrieveTruncatesToK(t *testing.T) {
	f := newRetrieveFixture(t)
	r := NewRanker(f.corpus, f.as, DefaultRetrieval()).Wrap(eval.NewBM25(f.corpus, 1), 1)
	got, err := r.Retrieve(t.Context(), "Spin returns one", 1)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

// TestExpiredRecordIsSurfacedNotWithheld pins the fleet-corpus finding: a
// record marked stale carries its correction in its own stale reason, so
// withholding it withholds the answer.
func TestExpiredRecordIsSurfacedNotWithheld(t *testing.T) {
	c, dir := openRepo(t)
	n, _, err := c.CreateNote(t.Context(), notes.NoteSpec{
		Title: "code search is semble-backed",
		Body:  "the ccx entry describes a semble subprocess",
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := c.ExpireNote(t.Context(), n.ID, "done, the native engine landed"); err != nil {
		t.Fatalf("ExpireNote: %v", err)
	}
	corpus, err := eval.LoadCorpus(t.Context(), c)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	as := assess(t, c, dir, testPolicy(time.Now()))
	if a := find(t, as, n.ID); a.Signal != SignalExpired {
		t.Fatalf("signal = %q, want %q", a.Signal, SignalExpired)
	}
	got := NewRanker(corpus, as, DefaultRetrieval()).Wrap(eval.NewBM25(corpus, 1), 1)
	res, err := got.Retrieve(t.Context(), "is code search semble-backed", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !slices.Contains(ids(res), n.ID) {
		t.Errorf("expired %s withheld; its stale reason is the correction", n.ID.Short())
	}
}

var _ eval.Retriever = (*Gated)(nil)
