package notes

import (
	"cmp"
	"context"
	"errors"
	"path"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// Relevance reason labels, in the fixed priority order they are rendered.
const (
	reasonPath         = "path"
	reasonDir          = "dir"
	reasonBranch       = "branch"
	reasonMergedCommit = "merged-commit"
	reasonMergedBranch = "merged-branch"
	reasonSibling      = "sibling"
	reasonCrossAuthor  = "cross-author"
)

// Relevance signal weights summed into an entity's score.
const (
	scorePath         = 100
	scoreDir          = 60
	scoreBranch       = 40
	scoreMergedCommit = 25
	scoreMergedBranch = 20
	scoreSibling      = 15
	scoreCrossAuthor  = 30
)

// reasonOrder is the fixed render order for an entity's matched reasons.
var reasonOrder = []string{
	reasonPath, reasonDir, reasonBranch, reasonMergedCommit, reasonMergedBranch, reasonSibling, reasonCrossAuthor,
}

// RelevantFilter narrows and shapes a Relevant scan. Branch is the branch to
// weigh branch signals against; an empty Branch resolves the current HEAD
// branch, degrading to no branch signals on a detached HEAD. Base is the
// merge-base reference for cross-author signals; an empty Base uses the remote
// default branch (falling back to "main"). Both are taken verbatim and assumed
// already validated by the caller. Attached keeps only entities anchored to the
// path or a parent directory. Worktree drift-checks path anchors against
// uncommitted working-tree edits when computing each entity's verdict.
type RelevantFilter struct {
	Branch   string
	Base     string
	Attached bool
	Worktree bool
}

// RelevantEntry is one ranked entity surfaced by Relevant: a kind discriminator
// and exactly one of the note, doc, log, or runbook it carries (the field
// matching Kind is set, the others zero), the summed relevance Score, the
// matched Reasons in fixed priority order, and the drift Verdict. Notes and
// docs carry their content verdict; a log or runbook never drifts, so its
// Verdict is empty. The full entity is carried so a caller can build its own
// DTOs and lean lines.
type RelevantEntry struct {
	Kind    model.Kind
	Note    model.Note
	Doc     model.Doc
	Log     model.Log
	Runbook model.Runbook
	Score   int
	Reasons []string
	Verdict Verdict
}

// id returns the entry's entity id, regardless of kind.
func (e RelevantEntry) id() model.EntityID {
	switch e.Kind {
	case model.KindDoc:
		return e.Doc.ID
	case model.KindLog:
		return e.Log.ID
	case model.KindRunbook:
		return e.Runbook.ID
	default:
		return e.Note.ID
	}
}

// updatedAt returns the entry's last-update time, regardless of kind.
func (e RelevantEntry) updatedAt() int64 {
	switch e.Kind {
	case model.KindDoc:
		return e.Doc.UpdatedAt
	case model.KindLog:
		return e.Log.UpdatedAt
	case model.KindRunbook:
		return e.Runbook.UpdatedAt
	default:
		return e.Note.UpdatedAt
	}
}

// scoredNote pairs a kept note with its summed score and matched reasons.
type scoredNote struct {
	note    model.Note
	score   int
	reasons []string
}

// Relevant scores every live note, doc, log, and active runbook against target
// and returns those with a positive score, each carrying its drift verdict,
// sorted by score descending, then UpdatedAt descending, then id ascending.
// filter.Branch and filter.Base override the resolved branch and merge-base base
// (taken verbatim, assumed already validated); an empty Branch resolves the
// current branch, degrading to no branch signals on a detached HEAD.
// filter.Attached drops entities not anchored to the path or a parent directory;
// filter.Worktree threads through to each entity's verdict. A log or runbook
// never drifts, so its verdict is empty.
func (c *Client) Relevant(ctx context.Context, target string, filter RelevantFilter) ([]RelevantEntry, error) {
	p := path.Clean(target)

	branch, err := c.resolveRelevantBranch(ctx, filter.Branch)
	if err != nil {
		return nil, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return nil, err
	}
	_, me, err := c.s.Git.AuthorIdent(ctx)
	if err != nil {
		return nil, err
	}
	crossAuthorPaths, err := c.crossAuthorSet(ctx, filter.Base, head, me)
	if err != nil {
		return nil, err
	}
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()

	all, err := c.s.ListNotes(ctx, false, false)
	if err != nil {
		return nil, err
	}
	var scored []RelevantEntry
	for _, n := range all {
		match, err := c.scoreNote(ctx, n, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, err
		}
		if match.score == 0 {
			continue
		}
		if filter.Attached && !anchoredNear(match.reasons) {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindNote, Note: match.note, Score: match.score, Reasons: match.reasons})
	}

	docs, err := c.s.ListDocs(ctx, false, false)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		score, reasons, err := c.scoreAnchors(ctx, d.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, err
		}
		if score == 0 {
			continue
		}
		if filter.Attached && !anchoredNear(reasons) {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindDoc, Doc: d, Score: score, Reasons: reasons})
	}

	logs, err := c.s.ListLogs(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, l := range logs {
		score, reasons, err := c.scoreAnchors(ctx, l.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, err
		}
		if score == 0 {
			continue
		}
		if filter.Attached && !anchoredNear(reasons) {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindLog, Log: l, Score: score, Reasons: reasons})
	}

	runbooks, err := c.Runbooks(ctx, RunbookFilter{})
	if err != nil {
		return nil, err
	}
	for _, rb := range runbooks {
		score, reasons, err := c.scoreAnchors(ctx, rb.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, err
		}
		if score == 0 {
			continue
		}
		if filter.Attached && !anchoredNear(reasons) {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindRunbook, Runbook: rb, Score: score, Reasons: reasons})
	}

	for i := range scored {
		verdict, err := c.entryVerdict(ctx, scored[i], head, now, staleAfter, filter.Worktree)
		if err != nil {
			return nil, err
		}
		scored[i].Verdict = verdict
	}
	slices.SortFunc(scored, compareScored)
	return scored, nil
}

// anchoredNear reports whether reasons include a path or dir match — the test
// --attached applies to drop entities matched only by looser signals.
func anchoredNear(reasons []string) bool {
	return slices.ContainsFunc(reasons, func(r string) bool {
		return r == reasonPath || r == reasonDir
	})
}

// entryVerdict computes the drift verdict for a kept entity against a single
// head/now snapshot shared across the whole ranked batch, dispatching to the
// note/doc verdict core by kind. A log or runbook never drifts — neither has a
// freshness lifecycle — so both short-circuit to an empty verdict.
func (c *Client) entryVerdict(ctx context.Context, e RelevantEntry, head model.SHA, now time.Time, staleAfter time.Duration, worktree bool) (Verdict, error) {
	switch e.Kind {
	case model.KindDoc:
		return c.verdictOf(ctx, head, freshFromDoc(e.Doc), now, staleAfter, worktree)
	case model.KindLog, model.KindRunbook:
		return "", nil
	default:
		return c.verdictOf(ctx, head, freshFromNote(e.Note), now, staleAfter, worktree)
	}
}

// compareScored is the total ranking order for scored entries: higher score
// first, then newer UpdatedAt first, then lower id first. The order is total
// (ids are unique across kinds), so the ranking is fully deterministic
// regardless of the sort's stability.
func compareScored(a, b RelevantEntry) int {
	if c := cmp.Compare(b.Score, a.Score); c != 0 {
		return c
	}
	if c := cmp.Compare(b.updatedAt(), a.updatedAt()); c != 0 {
		return c
	}
	return cmp.Compare(a.id(), b.id())
}

// resolveRelevantBranch returns the branch to weigh against: flag verbatim when
// set (assumed already validated), otherwise the current branch, or "" on a
// detached HEAD with no resolvable branch (branch signals are then skipped
// rather than an error).
func (c *Client) resolveRelevantBranch(ctx context.Context, flag string) (model.Branch, error) {
	if flag != "" {
		return model.Branch(flag), nil
	}
	branch, err := c.s.Git.CurrentBranch(ctx)
	if errors.Is(err, ErrDetachedHead) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return branch, nil
}

// crossAuthorSet returns the set of paths in the range base..HEAD touched by a
// teammate but never by me — the files whose recent changes I have not seen. It
// is empty when HEAD is unborn or no merge-base resolves. base defaults to the
// remote default branch, falling back to "main".
func (c *Client) crossAuthorSet(ctx context.Context, baseFlag string, head model.SHA, me string) (map[string]struct{}, error) {
	if head == "" {
		return nil, nil
	}
	base, err := c.resolveRelevantBase(ctx, baseFlag)
	if err != nil {
		return nil, err
	}
	mergeBase, err := c.s.Git.MergeBase(ctx, string(base), "HEAD")
	if errors.Is(err, gitcmd.ErrRevNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	authors, err := c.s.Git.RevRangeFileAuthors(ctx, string(mergeBase), "HEAD")
	if err != nil {
		return nil, err
	}
	cross := make(map[string]struct{})
	for p, emails := range authors {
		if !slices.Contains(emails, me) && hasOther(emails, me) {
			cross[p] = struct{}{}
		}
	}
	return cross, nil
}

// hasOther reports whether emails contains an address other than me.
func hasOther(emails []string, me string) bool {
	for _, e := range emails {
		if e != me {
			return true
		}
	}
	return false
}

// resolveRelevantBase returns the base reference for cross-author detection: the
// flag verbatim when set, otherwise the remote default branch, falling back to
// "main" when origin/HEAD is unset.
func (c *Client) resolveRelevantBase(ctx context.Context, flag string) (model.Branch, error) {
	if flag != "" {
		return model.Branch(flag), nil
	}
	base, err := c.s.Git.DefaultBranch(ctx)
	if errors.Is(err, gitcmd.ErrNoDefaultBranch) {
		return "main", nil
	}
	if err != nil {
		return "", err
	}
	return base, nil
}

// scoreNote sums every relevance signal note n matches against the target path
// p, the branch, and head, returning a scoredNote with the summed score and
// reasons in fixed priority order. It is a thin projection over n.Anchors onto
// the shared scoreAnchors core, so notes and docs score identically.
func (c *Client) scoreNote(ctx context.Context, n model.Note, p string, branch model.Branch, head model.SHA, crossAuthorPaths map[string]struct{}) (scoredNote, error) {
	score, reasons, err := c.scoreAnchors(ctx, n.Anchors, p, branch, head, crossAuthorPaths)
	if err != nil {
		return scoredNote{}, err
	}
	return scoredNote{note: n, score: score, reasons: reasons}, nil
}

// scoreAnchors sums every relevance signal the anchors match against the target
// path p, the branch, and head, returning the summed score with reasons in fixed
// priority order. Note and Doc share the same Anchors, so the weights and reason
// strings are reused verbatim across both kinds. The cross-author boost only
// fires on anchors already matched near p (via a path, dir, or sibling anchor
// whose value a teammate touched); it never creates a match on its own.
func (c *Client) scoreAnchors(ctx context.Context, anchors []model.Anchor, p string, branch model.Branch, head model.SHA, crossAuthorPaths map[string]struct{}) (int, []string, error) {
	var score int
	var reasons []string
	var nearPaths []string
	add := func(weight int, reason string) {
		score += weight
		reasons = append(reasons, reason)
	}

	if hasAnchorIn(anchors, model.AnchorPath, p) {
		add(scorePath, reasonPath)
		nearPaths = append(nearPaths, p)
	}
	if d := deepestDirAnchor(anchors, p); d != "" {
		add(scoreDir, reasonDir)
		nearPaths = append(nearPaths, d)
	}
	if branch != "" && hasAnchorIn(anchors, model.AnchorBranch, string(branch)) {
		add(scoreBranch, reasonBranch)
	}
	if head != "" {
		merged, err := c.commitAnchorMerged(ctx, anchors, head)
		if err != nil {
			return 0, nil, err
		}
		if merged {
			add(scoreMergedCommit, reasonMergedCommit)
		}
		mergedBranch, err := c.branchAnchorMerged(ctx, anchors, branch, head)
		if err != nil {
			return 0, nil, err
		}
		if mergedBranch {
			add(scoreMergedBranch, reasonMergedBranch)
		}
	}
	if sib := siblingAnchors(anchors, p); len(sib) > 0 {
		add(scoreSibling, reasonSibling)
		nearPaths = append(nearPaths, sib...)
	}
	if len(nearPaths) > 0 && anyCrossAuthor(nearPaths, crossAuthorPaths) {
		add(scoreCrossAuthor, reasonCrossAuthor)
	}
	sortReasons(reasons)
	return score, reasons, nil
}

// hasAnchorIn reports whether anchors contains an anchor of the given kind and
// value.
func hasAnchorIn(anchors []model.Anchor, kind model.AnchorKind, value string) bool {
	return slices.Contains(anchors, model.Anchor{Kind: kind, Value: value})
}

// sortReasons orders the matched reasons into the fixed priority order.
func sortReasons(reasons []string) {
	slices.SortFunc(reasons, func(a, b string) int {
		return cmp.Compare(slices.Index(reasonOrder, a), slices.Index(reasonOrder, b))
	})
}

// deepestDirAnchor returns the deepest dir anchor in anchors that contains p (p
// equals the dir or sits under it), or "" when none does. Picking the single
// deepest match keeps overlapping dir anchors from stacking their score.
func deepestDirAnchor(anchors []model.Anchor, p string) string {
	var deepest string
	for _, a := range anchors {
		if a.Kind != model.AnchorDir {
			continue
		}
		d := path.Clean(a.Value)
		if !pathUnderDir(p, d) {
			continue
		}
		if len(d) > len(deepest) {
			deepest = d
		}
	}
	return deepest
}

// pathUnderDir reports whether p equals dir or is nested under it.
func pathUnderDir(p, dir string) bool {
	if p == dir {
		return true
	}
	if dir == "." {
		return true
	}
	return len(p) > len(dir) && p[len(dir)] == '/' && p[:len(dir)] == dir
}

// siblingAnchors returns the path anchors in anchors that share p's parent
// directory without being p itself.
func siblingAnchors(anchors []model.Anchor, p string) []string {
	parent := path.Dir(p)
	var out []string
	for _, a := range anchors {
		if a.Kind != model.AnchorPath {
			continue
		}
		q := path.Clean(a.Value)
		if q != p && path.Dir(q) == parent {
			out = append(out, q)
		}
	}
	return out
}

// commitAnchorMerged reports whether any commit anchor in anchors is an ancestor
// of (or equal to) head — its work has merged into the current line of history.
func (c *Client) commitAnchorMerged(ctx context.Context, anchors []model.Anchor, head model.SHA) (bool, error) {
	for _, a := range anchors {
		if a.Kind != model.AnchorCommit {
			continue
		}
		sha, err := c.s.Git.ResolveCommit(ctx, a.Value)
		if errors.Is(err, gitcmd.ErrRevNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		reachable, err := c.s.Repo.IsAncestor(ctx, sha, head)
		if errors.Is(err, gitobj.ErrCommitNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if reachable {
			return true, nil
		}
	}
	return false, nil
}

// branchAnchorMerged reports whether any branch anchor in anchors names a branch
// other than the target branch whose tip has merged into head. A branch whose
// ref is gone is skipped, not an error.
func (c *Client) branchAnchorMerged(ctx context.Context, anchors []model.Anchor, branch model.Branch, head model.SHA) (bool, error) {
	for _, a := range anchors {
		if a.Kind != model.AnchorBranch || model.Branch(a.Value) == branch {
			continue
		}
		tip, err := c.s.Repo.Tip(ctx, "refs/heads/"+a.Value)
		if errors.Is(err, gitobj.ErrRefNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		reachable, err := c.s.Repo.IsAncestor(ctx, tip, head)
		if errors.Is(err, gitobj.ErrCommitNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if reachable {
			return true, nil
		}
	}
	return false, nil
}

// anyCrossAuthor reports whether any of paths is in the cross-author set.
func anyCrossAuthor(paths []string, cross map[string]struct{}) bool {
	for _, p := range paths {
		if _, ok := cross[p]; ok {
			return true
		}
	}
	return false
}
