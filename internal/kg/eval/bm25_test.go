package eval

import (
	"math"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// goldenCorpus has hand-computable BM25 statistics: N=3, token counts
// [4, 5, 3], avgLen=4, df{note:3, alpha:1, beta:2, gamma:2, delta:1}.
func goldenCorpus() []Entity {
	return []Entity{
		{ID: "a", Kind: model.KindNote, Title: "alpha", Body: "alpha beta"},
		{ID: "b", Kind: model.KindNote, Title: "beta", Body: "beta gamma gamma"},
		{ID: "c", Kind: model.KindNote, Title: "gamma", Body: "delta"},
	}
}

func TestBM25GoldenScores(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		wantID []model.EntityID
		want   []float64
	}{
		{
			name: "single term in one document", query: "alpha",
			wantID: []model.EntityID{"a"},
			want:   []float64{1.40118464715960890},
		},
		{
			name: "term in two documents ranks by saturation", query: "beta",
			wantID: []model.EntityID{"b", "a"},
			want:   []float64{0.62149240230841074, 0.47000362924573563},
		},
		{
			name: "two terms sum", query: "alpha beta",
			wantID: []model.EntityID{"a", "b"},
			want:   []float64{1.87118827640534446, 0.62149240230841074},
		},
		{
			name: "unknown term retrieves nothing", query: "epsilon",
		},
		{
			name: "empty query retrieves nothing", query: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewBM25(goldenCorpus(), 1).Retrieve(t.Context(), tc.query, 10)
			if err != nil {
				t.Fatalf("Retrieve(%q): %v", tc.query, err)
			}
			if len(got) != len(tc.wantID) {
				t.Fatalf("Retrieve(%q) = %d results, want %d: %+v", tc.query, len(got), len(tc.wantID), got)
			}
			for i, r := range got {
				if r.ID != tc.wantID[i] {
					t.Errorf("result %d id = %s, want %s", i, r.ID, tc.wantID[i])
				}
				if math.Abs(r.Score-tc.want[i]) > 1e-12 {
					t.Errorf("result %d score = %.17f, want %.17f", i, r.Score, tc.want[i])
				}
				if r.Lane != BM25Lane {
					t.Errorf("result %d lane = %q, want %q", i, r.Lane, BM25Lane)
				}
			}
		})
	}
}

func TestBM25TruncatesToK(t *testing.T) {
	got, err := NewBM25(goldenCorpus(), 1).Retrieve(t.Context(), "alpha beta gamma", 1)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("Retrieve(k=1) = %+v, want just entity a", got)
	}
}

func TestBM25TiesDependOnSeed(t *testing.T) {
	corpus := []Entity{
		{ID: "a", Kind: model.KindNote, Title: "same", Body: "same"},
		{ID: "b", Kind: model.KindNote, Title: "same", Body: "same"},
	}
	cases := []struct {
		seed int64
		want []model.EntityID
	}{
		{seed: 1, want: []model.EntityID{"a", "b"}},
		{seed: 2, want: []model.EntityID{"b", "a"}},
	}
	for _, tc := range cases {
		got, err := NewBM25(corpus, tc.seed).Retrieve(t.Context(), "same", 2)
		if err != nil {
			t.Fatalf("Retrieve(seed=%d): %v", tc.seed, err)
		}
		if len(got) != 2 || got[0].Score != got[1].Score {
			t.Fatalf("seed %d: want two equally-scored results, got %+v", tc.seed, got)
		}
		if got[0].ID != tc.want[0] || got[1].ID != tc.want[1] {
			t.Errorf("seed %d order = [%s %s], want %v", tc.seed, got[0].ID, got[1].ID, tc.want)
		}
	}
}

func TestBM25EmptyCorpus(t *testing.T) {
	got, err := NewBM25(nil, 1).Retrieve(t.Context(), "alpha", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Retrieve over an empty corpus = %+v, want no results", got)
	}
}
