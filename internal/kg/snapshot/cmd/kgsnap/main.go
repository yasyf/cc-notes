// Command kgsnap freezes the retrieval harness's evaluation corpus. A capture
// folds every repository a question set names into a snapshot directory and
// reports what moved since the previous capture; a stamp records that the
// question set's gold labels were re-checked against that snapshot, which is
// what kgeval and kgrank refuse to run without. Capture never stamps: the
// delta between two corpora is exactly the judgement no digest can make.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/snapshot"
	"github.com/yasyf/cc-notes/model"
)

func main() {
	questions := flag.String("questions", "", "path to the question set JSON")
	root := flag.String("snapshot", "", "snapshot directory to capture into or stamp")
	stamp := flag.Bool("stamp", false, "record that the gold labels were re-validated against the snapshot")
	flag.Parse()

	if err := run(*questions, *root, *stamp); err != nil {
		fmt.Fprintln(os.Stderr, "kgsnap:", err)
		os.Exit(1)
	}
}

func run(path, root string, stamp bool) error {
	if root == "" {
		return errors.New("-snapshot is required")
	}
	qs, err := eval.LoadQuestions(path)
	if err != nil {
		return err
	}
	if stamp {
		return stampAll(qs, path, root)
	}
	return captureAll(context.Background(), qs, root)
}

// captureAll refolds every repository the question set names and reports, per
// repository, what the refresh changed and which labels it broke.
func captureAll(ctx context.Context, qs eval.QuestionSet, root string) error {
	broken := 0
	for _, dir := range qs.Repos() {
		before, err := snapshot.Load(root, dir)
		refresh := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		start := time.Now()
		c, err := snapshot.Capture(ctx, dir, time.Now())
		if err != nil {
			return err
		}
		if _, err := snapshot.Write(root, c); err != nil {
			return err
		}
		fmt.Printf("=== %s: %d entities, %d nodes, %d edges, %d events, %d assessments in %s\n",
			dir, len(c.Entities), len(c.Graph.Nodes), len(c.Graph.Edges), len(c.Graph.Events),
			len(c.Assessments), time.Since(start).Round(time.Millisecond))
		if refresh {
			reportDelta(snapshot.Diff(before, c))
		}
		defects := snapshot.Labels(c, qs.ForRepo(dir))
		broken += len(defects)
		for _, d := range defects {
			fmt.Printf("    BROKEN  %s\n", d)
		}
	}
	fmt.Printf("\n%d broken labels. Re-adjudicate the gold sets against the delta above, then run kgsnap -stamp.\n", broken)
	return nil
}

// stampAll validates every repository before it fails, so one rotted gold set
// does not hide the next one.
func stampAll(qs eval.QuestionSet, path, root string) error {
	broken := 0
	for _, dir := range qs.Repos() {
		questions := qs.ForRepo(dir)
		defects, err := snapshot.Stamp(root, dir, path, questions, time.Now())
		if err != nil && !errors.Is(err, snapshot.ErrBrokenLabels) {
			return err
		}
		fmt.Printf("=== %s: %d questions, %d broken labels\n", dir, len(questions), len(defects))
		for _, d := range defects {
			fmt.Printf("    BROKEN  %s\n", d)
		}
		broken += len(defects)
	}
	if broken > 0 {
		return fmt.Errorf("%w: %d across the set — fix them in %s and re-run", snapshot.ErrBrokenLabels, broken, path)
	}
	return nil
}

func reportDelta(d snapshot.Delta) {
	if d.Empty() {
		fmt.Println("    unchanged since the previous capture")
		return
	}
	reportIDs("added", d.Added)
	reportIDs("removed", d.Removed)
	reportIDs("changed", d.Changed)
	reportIDs("re-gated", d.Regated)
}

func reportIDs(label string, ids []model.EntityID) {
	if len(ids) == 0 {
		return
	}
	short := make([]string, len(ids))
	for i, id := range ids {
		short[i] = id.Short()
	}
	fmt.Printf("    %-8s %3d: %s\n", label, len(ids), strings.Join(short, " "))
}
