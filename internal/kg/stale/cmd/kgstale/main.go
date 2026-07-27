// Command kgstale runs the cc-notes staleness signals over a repository's own
// corpus and prints the verdict distribution: gate and penalty counts per kind,
// and the records the signals would withhold from injection.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func main() {
	dir := flag.String("repo", ".", "repository to assess")
	list := flag.Bool("list", false, "list every gated record")
	flag.Parse()

	if err := run(*dir, *list); err != nil {
		fmt.Fprintln(os.Stderr, "kgstale:", err)
		os.Exit(1)
	}
}

func run(dir string, list bool) error {
	ctx := context.Background()
	client, err := notes.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	policy, err := stale.DefaultPolicy(ctx, client, time.Now())
	if err != nil {
		return err
	}
	assessments, err := stale.New(client, dir, policy).Assess(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("=== %s: %d records\n\n", dir, len(assessments))
	printKinds(assessments)
	printSignals(assessments)
	if list {
		printGated(assessments)
	}
	fmt.Printf("re-verify queue: %d records\n", len(stale.ReverifyQueue(assessments)))
	return nil
}

func printKinds(as []stale.Assessment) {
	byKind := map[model.Kind][]stale.Assessment{}
	for _, a := range as {
		byKind[a.Kind] = append(byKind[a.Kind], a)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "kind\tn\tgated\tfresh\tpenalized\treverify\tpromoted\tmean weight")
	for _, kind := range model.Kinds() {
		group := byKind[kind]
		if len(group) == 0 {
			_, _ = fmt.Fprintf(w, "%s\t0\t-\t-\t-\t-\t-\t-\n", kind)
			continue
		}
		var gated, fresh, penalized, reverify, promoted int
		total := 0.0
		for _, a := range group {
			total += a.Weight
			switch {
			case a.Gated:
				gated++
			case len(a.Penalties) == 0:
				fresh++
			default:
				penalized++
			}
			if a.Reverify {
				reverify++
			}
			if a.Promoted {
				promoted++
			}
		}
		_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%.2f\n",
			kind, len(group), gated, fresh, penalized, reverify, promoted, total/float64(len(group)))
	}
	flush(w)
}

func printSignals(as []stale.Assessment) {
	counts := map[stale.Signal]int{}
	for _, a := range as {
		if a.Gated {
			counts[a.Signal]++
			continue
		}
		for _, p := range a.Penalties {
			counts[p.Signal]++
		}
	}
	signals := make([]string, 0, len(counts))
	for s := range counts {
		signals = append(signals, string(s))
	}
	sort.Strings(signals)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "\nsignal\tn\trole")
	for _, s := range signals {
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\n", s, counts[stale.Signal(s)], role(stale.Signal(s)))
	}
	flush(w)
}

func role(s stale.Signal) string {
	switch s {
	case stale.SignalChurn, stale.SignalDeadRef, stale.SignalDecay:
		return "penalty"
	}
	return "gate"
}

func printGated(as []stale.Assessment) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "\nid\tkind\tgated\tweight\tsignal\tdetail")
	for _, a := range as {
		if a.Signal == "" {
			continue
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%t\t%.2f\t%s\t%s\n", a.ID.Short(), a.Kind, a.Gated, a.Weight, a.Signal, a.Detail)
	}
	flush(w)
}

func flush(w *tabwriter.Writer) {
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "kgstale:", err)
	}
	fmt.Println()
}
