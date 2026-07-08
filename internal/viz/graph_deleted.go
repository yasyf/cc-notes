package viz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// deletedMergeDepth bounds how deep the carrier queue recurses into mined
// branches. The trunk and the live branches seed the queue at depth 0; a branch
// mined off a depth-d carrier joins the queue as a carrier at depth d+1 only
// while d+1 <= deletedMergeDepth, so a deleted branch merged into a deleted
// branch is still reconstructed but the recursion cannot run away.
const deletedMergeDepth = 3

// merge-summary patterns, anchored so an "into <target>" suffix is captured
// rather than folded into the branch name. reMergePR's head ref keeps every
// path segment past the first (the owner), so "owner/feat/nested" resolves to
// "feat/nested".
var (
	reMergeBranch = regexp.MustCompile(`^Merge branch '([^']+)'(?: into (.+))?$`)
	reMergeRemote = regexp.MustCompile(`^Merge remote-tracking branch '([^']+)'(?: into (.+))?$`)
	reMergePR     = regexp.MustCompile(`^Merge pull request #\d+ from (\S+)`)
)

// carrier is one branch whose first-parent merges are scanned for deleted
// branches: its name — the Parent and merge Into of any branch found on it —
// its tip, and its recursion depth off the depth-0 live carriers.
type carrier struct {
	name  string
	tip   model.SHA
	depth int
}

// mineDeletedBranches reconstructs the lanes of branches that were merged and
// then had their ref deleted, mined from the git DAG. It scans the first-parent
// merge commits of every carrier — the trunk, then the live branches, then each
// mined branch down to deletedMergeDepth — and for every merge parent past the
// first that no live ref or already-mined lane claims, synthesizes a deleted
// lane whose fork is that parent's merge base with the pre-merge carrier, whose
// tip is the parent, and whose merge is the carrier's merge commit. Merge shas
// are deduped across carriers, the first carrier to reach one winning, so a
// branch merged into the trunk is parented to the trunk even when a later branch
// carries the same merge transitively. Only the first extra parent takes the
// merge subject's parsed name; further octopus parents and unparseable subjects
// fall back to a sha placeholder. The result is memoized by (trunk tip, sorted
// live tips, since) so entity-ref churn does not re-mine.
func (b *Builder) mineDeletedBranches(ctx context.Context, trunk *branchState, others []*branchState, r *topoRun) ([]Lane, error) {
	key := minedKey(trunk.tip, others, r.since)
	if lanes, ok := b.cachedMined(key); ok {
		return lanes, nil
	}

	live := make(map[string]bool, len(others)+1)
	liveTip := make(map[model.SHA]bool, len(others))
	live[trunk.name] = true
	for _, s := range others {
		live[s.name] = true
		liveTip[s.tip] = true
	}

	queue := make([]carrier, 0, len(others)+1)
	queue = append(queue, carrier{name: trunk.name, tip: trunk.tip})
	for _, s := range others {
		queue = append(queue, carrier{name: s.name, tip: s.tip})
	}

	mined := make(map[string]bool)
	seenMerge := make(map[model.SHA]bool)
	var lanes []Lane
	for i := 0; i < len(queue); i++ {
		c := queue[i]
		merges, err := b.store.Repo.FirstParentMerges(ctx, c.tip, walkLimit, 0)
		if err != nil {
			return nil, fmt.Errorf("first-parent merges %s: %w", c.tip, err)
		}
		for _, m := range merges {
			if seenMerge[m.SHA] {
				continue
			}
			seenMerge[m.SHA] = true
			name, _, parsed := parseMergeSummary(m.Summary)
			for idx, p := range m.Parents[1:] {
				branch := name
				if idx > 0 || !parsed {
					branch = "~" + string(p)[:7]
				}
				if liveTip[p] || live[branch] || mined[branch] {
					continue
				}
				lane, err := b.minedLane(ctx, c.name, branch, p, m, r)
				if err != nil {
					return nil, err
				}
				mined[branch] = true
				lanes = append(lanes, lane)
				if c.depth+1 <= deletedMergeDepth {
					queue = append(queue, carrier{name: branch, tip: p, depth: c.depth + 1})
				}
			}
		}
	}

	b.putMined(key, lanes)
	return lanes, nil
}

// minedLane builds one deleted-branch lane: parent p, merged into carrierName at
// merge commit m. The fork is p's merge base with the pre-merge carrier (m's
// first parent); Start and the windowed commit count follow topoRun.attribute —
// the count is p's post-fork commits within the window, empty when the merge
// predates it. Status is deleted and Inferred is false: the lane is DAG-proven,
// not reconstructed from task rumor.
func (b *Builder) minedLane(ctx context.Context, carrierName, name string, p model.SHA, m gitobj.CodeCommit, r *topoRun) (Lane, error) {
	tipTime, err := r.commitTime(p)
	if err != nil {
		return Lane{}, err
	}
	s := &branchState{
		name:    name,
		tip:     p,
		tipTime: tipTime,
		parent:  carrierName,
		status:  statusDeleted,
		merge:   &mergeInfo{sha: m.SHA, time: m.CommitTime, into: carrierName, kind: kindMerge},
		end:     m.CommitTime,
	}
	tipWin, err := r.window(p)
	if err != nil {
		return Lane{}, err
	}
	base, found, err := b.mergeBaseOf(ctx, p, m.Parents[0])
	if err != nil {
		return Lane{}, err
	}
	if !found {
		s.commits = len(tipWin.shas)
		return s.toLane(), nil
	}
	forkTime, err := r.commitTime(base)
	if err != nil {
		return Lane{}, err
	}
	forkWin, err := r.window(base)
	if err != nil {
		return Lane{}, err
	}
	s.hasFork = true
	s.forkBase = base
	s.forkTime = forkTime
	s.start = forkTime
	n := 0
	for sha := range tipWin.shas {
		if _, ok := forkWin.shas[sha]; !ok {
			n++
		}
	}
	s.commits = n
	return s.toLane(), nil
}

// parseMergeSummary extracts the merged branch name and, when the subject names
// it, the branch merged into from a merge commit's first message line. It
// recognizes git's default "Merge branch 'x'" and its "into y" variant, the
// "Merge remote-tracking branch 'origin/x'" form (the remote name stripped), and
// GitHub's "Merge pull request #N from owner/x" (the owner stripped). ok is
// false for any other subject.
func parseMergeSummary(summary string) (name, into string, ok bool) {
	if m := reMergeBranch.FindStringSubmatch(summary); m != nil {
		return m[1], m[2], true
	}
	if m := reMergeRemote.FindStringSubmatch(summary); m != nil {
		return stripFirstSegment(m[1]), m[2], true
	}
	if m := reMergePR.FindStringSubmatch(summary); m != nil {
		return stripFirstSegment(m[1]), "", true
	}
	return "", "", false
}

// stripFirstSegment drops the first slash-delimited segment of s — the remote or
// owner prefix — keeping every later segment so a nested branch name survives.
func stripFirstSegment(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// minedKey digests the mining inputs — the trunk tip, the sorted live branch
// tips, and the window floor — into the minedCache key. Entity-ref tips are
// deliberately absent: they churn on every note or task edit but never change
// which branches were merged and deleted, so folding them in would defeat the
// cache the whole-graph digest already refreshes.
func minedKey(trunkTip model.SHA, others []*branchState, since int64) string {
	tips := make([]string, 0, len(others))
	for _, s := range others {
		tips = append(tips, string(s.tip))
	}
	sort.Strings(tips)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "trunk=%s\n", trunkTip)
	for _, t := range tips {
		h.Write([]byte(t))
		h.Write([]byte{'\n'})
	}
	_, _ = fmt.Fprintf(h, "since=%d", since)
	return hex.EncodeToString(h.Sum(nil))
}

func (b *Builder) cachedMined(key string) ([]Lane, bool) {
	b.minedMu.Lock()
	defer b.minedMu.Unlock()
	lanes, ok := b.minedCache[key]
	return lanes, ok
}

func (b *Builder) putMined(key string, lanes []Lane) {
	b.minedMu.Lock()
	defer b.minedMu.Unlock()
	b.minedCache[key] = lanes
}
