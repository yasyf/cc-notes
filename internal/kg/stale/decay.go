package stale

import (
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/model"
)

// maxNamedRefs caps how many dead identifiers a penalty detail spells out.
const maxNamedRefs = 5

// penalties computes the S7-S9 rank demotions for a record. A promoted record —
// a confirmed root cause — is exempt from time decay but still pays for churn
// and dead references, which describe the code, not the record's age.
func (e *Evaluator) penalties(r record, promoted bool, tree *Tree, touches []touch) []Penalty {
	var out []Penalty
	if lines := churn(touches, r.Anchors, r.Attested); lines > 0 {
		out = append(out, Penalty{
			Signal: SignalChurn,
			Weight: halve(float64(lines), float64(e.policy.ChurnHalfLife)),
			Detail: fmt.Sprintf("%d lines churned on the anchored paths since the record was attested", lines),
		})
	}
	if dead := deadRefs(r, tree); len(dead) > 0 {
		out = append(out, Penalty{
			Signal: SignalDeadRef,
			Weight: halve(float64(len(dead)), float64(e.policy.DeadRefHalfLife)),
			Detail: fmt.Sprintf("names %d code elements absent from the tree: %s", len(dead), joinComma(elide(dead, maxNamedRefs))),
		})
	}
	if h, ok := e.policy.HalfLives[r.Kind]; ok && !promoted {
		if delta := e.policy.Now.Sub(time.Unix(r.Attested, 0)); delta > 0 {
			out = append(out, Penalty{
				Signal: SignalDecay,
				Weight: halve(float64(delta), float64(h)),
				Detail: fmt.Sprintf("last attested %d days ago", int(delta.Hours()/24)),
			})
		}
	}
	return out
}

// halve is the exponential decay 2^(-x/h) both the churn and the time signals
// weigh with: the weight halves every h units of x, and never reaches zero.
func halve(x, h float64) float64 { return math.Exp2(-x / h) }

// deadRefs applies S8 only to the kinds that assert facts about the current
// tree. A task or a project names the code it intends to write, and a log names
// the code as it stood; neither is stale for citing an identifier HEAD lacks.
func deadRefs(r record, tree *Tree) []string {
	switch r.Kind {
	case model.KindNote, model.KindDoc, model.KindRunbook, model.KindInvestigation:
		return tree.DeadRefs(r.Text())
	}
	return nil
}

// churn sums the lines committed to a record's path and directory anchors since
// it was attested. Commit and branch anchors have no file extent, so they
// contribute nothing.
func churn(touches []touch, anchors []model.Anchor, since int64) int {
	total := 0
	for _, t := range touches {
		if t.TS <= since {
			continue
		}
		if slices.ContainsFunc(anchors, func(a model.Anchor) bool { return anchorCovers(a, t.Path) }) {
			total += t.Lines
		}
	}
	return total
}

// anchorCovers reports whether a committed path falls under an anchor: exactly
// for a path anchor, by directory prefix for a dir anchor.
func anchorCovers(a model.Anchor, p string) bool {
	switch a.Kind {
	case model.AnchorPath:
		return path.Clean(a.Value) == p
	case model.AnchorDir:
		dir := path.Clean(a.Value)
		return dir == "." || strings.HasPrefix(p, dir+"/")
	}
	return false
}

// elide truncates a display list, replacing the tail with its count.
func elide(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(slices.Clone(items[:n]), fmt.Sprintf("and %d more", len(items)-n))
}
