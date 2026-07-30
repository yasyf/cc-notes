// Package snapshot freezes one repository's retrieval-evaluation inputs to
// disk: the flattened records the ranker indexes, the knowledge graph over
// them, and the staleness assessments, all folded once against a fixed clock
// and a fixed working tree. The harness reads a snapshot rather than
// refs/cc-notes/*, so a corpus that grew between two runs cannot move the
// numbers, and a question set's gold labels are bound by digest to the exact
// corpus they were validated against.
package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// Version is the only snapshot layout version this package reads or writes.
const Version = 1

// Corpus is one repository's frozen evaluation inputs: the three arguments
// rank.New takes, plus the manifest describing where they came from.
type Corpus struct {
	Manifest    Manifest
	Entities    []eval.Entity
	Graph       *kg.Graph
	Assessments []stale.Assessment
}

// Manifest is a snapshot's provenance and integrity record. Head and Policy
// are what the staleness pass ran against — Policy.Now is the clock the freeze
// pins, so a verdict that would decay tomorrow does not — and Parts carries
// the sha256 of every data file, making an edited part a load error rather
// than a silent change of measurement.
type Manifest struct {
	Version      int          `json:"version"`
	Repo         string       `json:"repo"`
	CapturedAt   time.Time    `json:"captured_at"`
	Head         model.SHA    `json:"head"`
	GraphSource  string       `json:"graph_source"`
	GraphBuiltAt int64        `json:"graph_built_at"`
	Policy       stale.Policy `json:"policy"`
	Parts        []Part       `json:"parts"`
}

// Part is one JSONL data file in a snapshot: its name, the records it holds,
// and the sha256 of its bytes.
type Part struct {
	Name   string `json:"name"`
	Lines  int    `json:"lines"`
	SHA256 string `json:"sha256"`
}

// Capture folds one repository's evaluation inputs as of now. It is the only
// function the harness reaches refs/cc-notes/* or the working tree through.
func Capture(ctx context.Context, dir string, now time.Time) (Corpus, error) {
	at := now.UTC()
	c, err := notes.Open(dir) //nolint:contextcheck // notes.Open takes no context by design; ctx reaches the store on every call that blocks.
	if err != nil {
		return Corpus{}, fmt.Errorf("open %s: %w", dir, err)
	}
	entities, err := eval.LoadCorpus(ctx, c)
	if err != nil {
		return Corpus{}, fmt.Errorf("load corpus %s: %w", dir, err)
	}
	s, err := store.Open(dir) //nolint:contextcheck // store.Open takes no context by design; ctx reaches it on every call that blocks.
	if err != nil {
		return Corpus{}, fmt.Errorf("open store %s: %w", dir, err)
	}
	g, err := kg.Build(ctx, s)
	if err != nil {
		return Corpus{}, fmt.Errorf("build graph %s: %w", dir, err)
	}
	policy, err := stale.DefaultPolicy(ctx, c, at)
	if err != nil {
		return Corpus{}, fmt.Errorf("policy %s: %w", dir, err)
	}
	assessments, err := stale.New(c, dir, policy).Assess(ctx)
	if err != nil {
		return Corpus{}, fmt.Errorf("assess %s: %w", dir, err)
	}
	head, err := gitcmd.Git{Dir: dir}.CommitSHA(ctx, "HEAD")
	if err != nil {
		return Corpus{}, fmt.Errorf("resolve HEAD %s: %w", dir, err)
	}
	return Corpus{
		Manifest: Manifest{
			Version:      Version,
			Repo:         filepath.Clean(dir),
			CapturedAt:   at,
			Head:         head,
			GraphSource:  g.Source,
			GraphBuiltAt: g.BuiltAt,
			Policy:       policy,
		},
		Entities:    entities,
		Graph:       g,
		Assessments: assessments,
	}, nil
}
