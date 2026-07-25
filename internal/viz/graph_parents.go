package viz

import (
	"context"

	"github.com/yasyf/cc-notes/model"
)

// nesting is one candidate parentage edge: lane p may parent lane s, with mb
// their merge base — the point on s where p diverged.
type nesting struct {
	s  *branchState
	p  *branchState
	mb model.SHA
}

// assignParents infers each branch's parent lane. Every branch defaults to the
// trunk. When the branch count is within maxParentageBranches, parent(B) is the
// lane P whose merge-base with B has the greatest commit time among lanes whose
// merge-base with B strictly descends from B's fork off the trunk; a tie for
// that maximum, or no candidate, falls back to the trunk. A candidate that
// descends from B is skipped so a branch never adopts its own descendant, with
// an identical-tip pair broken by name so exactly one direction parents. Above
// the cap the pairwise scan is quadratic, so parentage stays flat.
//
// Ancestry is read off merge bases rather than walked: a is an ancestor of b
// exactly when merge-base(a, b) == a. That keeps the whole scan on batched git
// execs — the pairwise merge bases, then their fork-base checks, each
// prefetched in one bounded fan-out.
func (b *Builder) assignParents(ctx context.Context, trunk *branchState, others []*branchState, r *topoRun) error {
	for _, s := range others {
		s.parent = trunk.name
	}
	if len(others) > maxParentageBranches {
		return nil
	}
	links, err := b.nestingLinks(ctx, others)
	if err != nil {
		return err
	}

	pairs := make([][2]model.SHA, 0, len(links))
	for _, l := range links {
		pairs = append(pairs, [2]model.SHA{l.s.forkBase, l.mb})
	}
	if err := b.prefetchMergeBases(ctx, pairs); err != nil {
		return err
	}

	best := make(map[*branchState]string, len(others))
	bestTime := make(map[*branchState]int64, len(others))
	tied := make(map[*branchState]bool, len(others))
	for _, l := range links {
		base, found, err := b.mergeBaseOf(ctx, l.s.forkBase, l.mb)
		if err != nil {
			return err
		}
		if !found || base != l.s.forkBase {
			continue
		}
		mbTime, err := r.commitTime(l.mb)
		if err != nil {
			return err
		}
		switch {
		case best[l.s] == "" || mbTime > bestTime[l.s]:
			best[l.s], bestTime[l.s], tied[l.s] = l.p.name, mbTime, false
		case mbTime == bestTime[l.s]:
			tied[l.s] = true
		}
	}
	for _, s := range others {
		if best[s] != "" && !tied[s] {
			s.parent = best[s]
		}
	}
	return nil
}

// nestingLinks resolves the merge base of every forked pair, prefetched in one
// fan-out, and keeps the pairs that can parent: those sharing a real merge base
// that is not simply s's own fork off the trunk, and where p does not descend
// from s — a branch never adopts its own descendant, an identical-tip pair
// broken by name so exactly one direction parents. The scan is deliberately
// unfiltered: any shortcut over fork-base ancestry is unsound when a criss-cross
// history gives a pair several best common ancestors and git's single-answer
// merge-base picks one arbitrarily.
func (b *Builder) nestingLinks(ctx context.Context, others []*branchState) ([]nesting, error) {
	var pairs [][2]model.SHA
	for _, s := range others {
		if !s.hasFork {
			continue
		}
		for _, p := range others {
			if p != s && p.hasFork {
				pairs = append(pairs, [2]model.SHA{s.tip, p.tip})
			}
		}
	}
	if err := b.prefetchMergeBases(ctx, pairs); err != nil {
		return nil, err
	}
	var links []nesting
	for _, s := range others {
		if !s.hasFork {
			continue
		}
		for _, p := range others {
			if p == s || !p.hasFork {
				continue
			}
			mb, found, err := b.mergeBaseOf(ctx, s.tip, p.tip)
			if err != nil {
				return nil, err
			}
			if !found || mb == s.forkBase {
				continue
			}
			if mb == s.tip && (s.tip != p.tip || s.name < p.name) {
				continue
			}
			links = append(links, nesting{s: s, p: p, mb: mb})
		}
	}
	return links, nil
}
