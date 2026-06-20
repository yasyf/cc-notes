package fold_test

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func mk(sha string, parents []string, author string, at int64, lamport uint64, ops ...model.Op) model.PackCommit {
	ps := make([]model.SHA, len(parents))
	for i, p := range parents {
		ps[i] = model.SHA(p)
	}
	return model.PackCommit{
		SHA:        model.SHA(sha),
		Parents:    ps,
		Author:     model.Actor(author),
		AuthorTime: at,
		Pack:       model.Pack{Lamport: model.Lamport(lamport), Ops: ops},
	}
}

func shas(commits []model.PackCommit) []model.SHA {
	out := make([]model.SHA, len(commits))
	for i, c := range commits {
		out[i] = c.SHA
	}
	return out
}

// permutations returns every ordering of commits (heap's algorithm); keep
// inputs at n <= 5.
func permutations(commits []model.PackCommit) [][]model.PackCommit {
	var out [][]model.PackCommit
	var rec func(k int, s []model.PackCommit)
	rec = func(k int, s []model.PackCommit) {
		if k == 1 {
			out = append(out, slices.Clone(s))
			return
		}
		for i := range k {
			rec(k-1, s)
			if k%2 == 0 {
				s[i], s[k-1] = s[k-1], s[i]
			} else {
				s[0], s[k-1] = s[k-1], s[0]
			}
		}
	}
	rec(len(commits), slices.Clone(commits))
	return out
}

func shuffles(commits []model.PackCommit, n int) [][]model.PackCommit {
	//nolint:gosec // G404: deterministic PRNG seeds a reproducible linearize fuzz; not security-relevant.
	r := rand.New(rand.NewPCG(1, 2))
	out := make([][]model.PackCommit, n)
	for i := range out {
		s := slices.Clone(commits)
		r.Shuffle(len(s), func(a, b int) { s[a], s[b] = s[b], s[a] })
		out[i] = s
	}
	return out
}

func TestLinearizeLinearChain(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "t"}),
		mk("bbb", []string{"aaa"}, "alice", 200, 2, model.SetTitle{Title: "t2"}),
		mk("ccc", []string{"bbb"}, "alice", 300, 3, model.SetBody{Body: "b"}),
	}
	want := []model.SHA{"aaa", "bbb", "ccc"}
	for i, input := range permutations(chain) {
		got, err := fold.Linearize(input)
		if err != nil {
			t.Fatalf("permutation %d: Linearize() error = %v", i, err)
		}
		if !slices.Equal(shas(got), want) {
			t.Fatalf("permutation %d: order = %v, want %v", i, shas(got), want)
		}
	}
}

func TestLinearizeFrontierOrder(t *testing.T) {
	cases := []struct {
		name               string
		bLamport, cLamport uint64
		bTime, cTime       int64
		want               []model.SHA
	}{
		{
			name:     "lower lamport first",
			bLamport: 3, bTime: 100, cLamport: 2, cTime: 200,
			want: []model.SHA{"aaa", "ccc", "bbb", "ddd"},
		},
		{
			name:     "lamport tie broken by author time",
			bLamport: 2, bTime: 300, cLamport: 2, cTime: 200,
			want: []model.SHA{"aaa", "ccc", "bbb", "ddd"},
		},
		{
			name:     "lamport and time tie broken by sha",
			bLamport: 2, bTime: 200, cLamport: 2, cTime: 200,
			want: []model.SHA{"aaa", "bbb", "ccc", "ddd"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n"}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, tc.bLamport, model.SetTitle{Title: "b"}),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, tc.cLamport, model.SetTitle{Title: "c"}),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 400, 4),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Linearize(input)
				if err != nil {
					t.Fatalf("permutation %d: Linearize() error = %v", i, err)
				}
				if !slices.Equal(shas(got), tc.want) {
					t.Fatalf("permutation %d: order = %v, want %v", i, shas(got), tc.want)
				}
			}
		})
	}
}

func TestLinearizeMultiMergeDeterminism(t *testing.T) {
	dag := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "b"}),
		mk("ccc", []string{"aaa"}, "carol", 200, 2, model.AddLabel{Label: "c"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
		mk("eee", []string{"ddd"}, "eve", 400, 4, model.Claim{Assignee: "eve"}),
		mk("fff", []string{"ddd"}, "frank", 350, 4, model.AddComment{Body: "hi"}),
		mk("ggg", []string{"eee", "fff"}, "gail", 500, 5),
		mk("hhh", []string{"ggg"}, "hank", 600, 6, model.SetStatus{Status: model.StatusDone}),
	}
	canonical, err := fold.Linearize(dag)
	if err != nil {
		t.Fatalf("Linearize() error = %v", err)
	}
	position := make(map[model.SHA]int, len(canonical))
	for i, c := range canonical {
		position[c.SHA] = i
	}
	for _, c := range canonical {
		for _, p := range c.Parents {
			if position[p] >= position[c.SHA] {
				t.Fatalf("parent %s at %d not before child %s at %d", p, position[p], c.SHA, position[c.SHA])
			}
		}
	}
	for i, input := range shuffles(dag, 50) {
		got, err := fold.Linearize(input)
		if err != nil {
			t.Fatalf("shuffle %d: Linearize() error = %v", i, err)
		}
		if !slices.Equal(shas(got), shas(canonical)) {
			t.Fatalf("shuffle %d: order = %v, want %v", i, shas(got), shas(canonical))
		}
	}
}

func TestLinearizeErrors(t *testing.T) {
	cases := []struct {
		name    string
		commits []model.PackCommit
		want    error
	}{
		{
			name:    "empty chain",
			commits: nil,
			want:    fold.ErrEmptyChain,
		},
		{
			name: "missing parent",
			commits: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1),
				mk("bbb", []string{"zzz"}, "bob", 200, 2),
			},
			want: fold.ErrMissingParent,
		},
		{
			name: "two roots",
			commits: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1),
				mk("bbb", nil, "bob", 100, 1),
				mk("ccc", []string{"aaa", "bbb"}, "carol", 200, 2),
			},
			want: fold.ErrMultipleRoots,
		},
		{
			name: "two heads",
			commits: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1),
				mk("bbb", []string{"aaa"}, "bob", 200, 2),
				mk("ccc", []string{"aaa"}, "carol", 300, 2),
			},
			want: fold.ErrMultipleHeads,
		},
		{
			name: "duplicate sha",
			commits: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1),
				mk("aaa", nil, "alice", 100, 1),
			},
			want: fold.ErrCorruptChain,
		},
		{
			name: "parent cycle",
			commits: []model.PackCommit{
				mk("rrr", nil, "alice", 100, 1),
				mk("aaa", []string{"rrr", "bbb"}, "bob", 200, 2),
				mk("bbb", []string{"aaa"}, "carol", 300, 3),
				mk("hhh", []string{"aaa"}, "dave", 400, 4),
			},
			want: fold.ErrCorruptChain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fold.Linearize(tc.commits)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Linearize() error = %v, want %v", err, tc.want)
			}
		})
	}
}
