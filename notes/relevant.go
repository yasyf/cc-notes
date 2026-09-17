package notes

import (
	"cmp"
	"context"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// Relevance reason labels, in the fixed priority order they are rendered.
const (
	reasonInvestigationOpen = "investigation-open"
	reasonPlanExecuting     = "plan-executing"
	reasonPath              = "path"
	reasonDir               = "dir"
	reasonBranch            = "branch"
	reasonMergedCommit      = "merged-commit"
	reasonMergedBranch      = "merged-branch"
	reasonSibling           = "sibling"
	reasonCrossAuthor       = "cross-author"
)

// Relevance signal weights summed into an entity's score. scoreInvestigationOpen
// is the boost a non-terminal investigation anchored near the target earns, so
// "you are editing code under active investigation" outranks a plain path match;
// scorePlanExecuting is its counterpart for the plan currently being executed.
const (
	scoreInvestigationOpen = 50
	scorePlanExecuting     = 50
	scorePath              = 100
	scoreDir               = 60
	scoreBranch            = 40
	scoreMergedCommit      = 25
	scoreMergedBranch      = 20
	scoreSibling           = 15
	scoreCrossAuthor       = 30
)

// reasonOrder is the fixed render order for an entity's matched reasons.
var reasonOrder = []string{
	reasonInvestigationOpen, reasonPlanExecuting,
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
// and exactly one of the note, doc, log, runbook, investigation, plan, or answer
// it carries (the field matching Kind is set, the others zero), the summed
// relevance Score, the matched Reasons in fixed priority order, and the drift
// Verdict. Notes, docs, and answers carry their content verdict; a log, runbook,
// investigation, or plan never drifts, so its Verdict is empty. The full entity
// is carried so a caller can build its own DTOs and lean lines.
type RelevantEntry struct {
	Kind          model.Kind
	Note          model.Note
	Doc           model.Doc
	Log           model.Log
	Runbook       model.Runbook
	Investigation model.Investigation
	Plan          model.Plan
	Answer        model.Answer
	Ledger        model.Ledger
	Score         int
	Reasons       []string
	Verdict       Verdict
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
	case model.KindInvestigation:
		return e.Investigation.ID
	case model.KindPlan:
		return e.Plan.ID
	case model.KindAnswer:
		return e.Answer.ID
	case model.KindLedger:
		return e.Ledger.ID
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
	case model.KindInvestigation:
		return e.Investigation.UpdatedAt
	case model.KindPlan:
		return e.Plan.UpdatedAt
	case model.KindAnswer:
		return e.Answer.UpdatedAt
	case model.KindLedger:
		return e.Ledger.UpdatedAt
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

// Relevant scores every live note, doc, log, active runbook, investigation,
// plan, and answer against target and returns those with a positive score, each carrying its
// drift verdict, sorted by score descending, then UpdatedAt descending, then id
// ascending. filter.Branch and filter.Base override the resolved branch and
// merge-base base (taken verbatim, assumed already validated); an empty Branch
// resolves the current branch, degrading to no branch signals on a detached HEAD.
// filter.Attached drops entities not anchored to the path or a parent directory;
// filter.Worktree threads through to each entity's verdict. A log, runbook,
// investigation, or plan never drifts, so its verdict is empty.
func (c *Client) Relevant(ctx context.Context, target string, filter RelevantFilter) ([]RelevantEntry, error) {
	scored, clock, err := c.relevantScored(ctx, target, filter)
	if err != nil {
		return nil, err
	}
	if err := c.relevantVerdicts(ctx, scored, clock, filter.Worktree); err != nil {
		return nil, err
	}
	return scored, nil
}

type relevantClock struct {
	head       model.SHA
	now        time.Time
	staleAfter time.Duration
}

func (c *Client) relevantScored(ctx context.Context, target string, filter RelevantFilter) ([]RelevantEntry, relevantClock, error) {
	p, err := c.relevantPath(ctx, target)
	if err != nil {
		return nil, relevantClock{}, err
	}

	branch, err := c.resolveRelevantBranch(ctx, filter.Branch)
	if err != nil {
		return nil, relevantClock{}, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return nil, relevantClock{}, err
	}
	_, me, err := c.s.Git.AuthorIdent(ctx)
	if err != nil {
		return nil, relevantClock{}, err
	}
	crossAuthorPaths, err := c.crossAuthorSet(ctx, filter.Base, head, me)
	if err != nil {
		return nil, relevantClock{}, err
	}
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return nil, relevantClock{}, err
	}
	now := time.Now()

	all, err := c.s.ListNotes(ctx, false, false)
	if err != nil {
		return nil, relevantClock{}, err
	}
	var scored []RelevantEntry
	for _, n := range all {
		if filter.Attached && !anchorsNear(n.Anchors, p) {
			continue
		}
		match, err := c.scoreNote(ctx, n, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if match.score == 0 {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindNote, Note: match.note, Score: match.score, Reasons: match.reasons})
	}

	docs, err := c.s.ListDocs(ctx, false, false)
	if err != nil {
		return nil, relevantClock{}, err
	}
	for _, d := range docs {
		if filter.Attached && !anchorsNear(d.Anchors, p) {
			continue
		}
		score, reasons, err := c.scoreAnchors(ctx, d.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if score == 0 {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindDoc, Doc: d, Score: score, Reasons: reasons})
	}

	answers, err := c.s.ListAnswers(ctx, false, false)
	if err != nil {
		return nil, relevantClock{}, err
	}
	for _, a := range answers {
		if filter.Attached && !anchorsNear(a.Anchors, p) {
			continue
		}
		score, reasons, err := c.scoreAnchors(ctx, a.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if score == 0 {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindAnswer, Answer: a, Score: score, Reasons: reasons})
	}

	ledgers, err := c.Ledgers(ctx, LedgerFilter{})
	if err != nil {
		return nil, relevantClock{}, err
	}
	for _, l := range ledgers {
		if filter.Attached && !anchorsNear(l.Anchors, p) {
			continue
		}
		score, reasons, err := c.scoreAnchors(ctx, l.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if score == 0 {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindLedger, Ledger: l, Score: score, Reasons: reasons})
	}

	logs, err := c.s.ListLogs(ctx, false)
	if err != nil {
		return nil, relevantClock{}, err
	}
	for _, l := range logs {
		if filter.Attached && !anchorsNear(l.Anchors, p) {
			continue
		}
		score, reasons, err := c.scoreAnchors(ctx, l.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if score == 0 {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindLog, Log: l, Score: score, Reasons: reasons})
	}

	runbooks, err := c.Runbooks(ctx, RunbookFilter{})
	if err != nil {
		return nil, relevantClock{}, err
	}
	for _, rb := range runbooks {
		if filter.Attached && !anchorsNear(rb.Anchors, p) {
			continue
		}
		score, reasons, err := c.scoreAnchors(ctx, rb.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if score == 0 {
			continue
		}
		scored = append(scored, RelevantEntry{Kind: model.KindRunbook, Runbook: rb, Score: score, Reasons: reasons})
	}

	investigations, err := c.s.ListInvestigations(ctx)
	if err != nil {
		return nil, relevantClock{}, err
	}
	for _, inv := range investigations {
		if filter.Attached && !anchorsNear(inv.Anchors, p) {
			continue
		}
		score, reasons, err := c.scoreAnchors(ctx, inv.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if score == 0 {
			continue
		}
		if nonTerminalInvestigation(inv.Status) && anchoredNear(reasons) {
			score += scoreInvestigationOpen
			reasons = append(reasons, reasonInvestigationOpen)
			sortReasons(reasons)
		}
		scored = append(scored, RelevantEntry{Kind: model.KindInvestigation, Investigation: inv, Score: score, Reasons: reasons})
	}

	plans, err := c.Plans(ctx, PlanFilter{})
	if err != nil {
		return nil, relevantClock{}, err
	}
	for _, plan := range plans {
		if filter.Attached && !anchorsNear(plan.Anchors, p) {
			continue
		}
		score, reasons, err := c.scoreAnchors(ctx, plan.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, relevantClock{}, err
		}
		if score == 0 {
			continue
		}
		if plan.Status == model.PlanExecuting && anchoredNear(reasons) {
			score += scorePlanExecuting
			reasons = append(reasons, reasonPlanExecuting)
			sortReasons(reasons)
		}
		scored = append(scored, RelevantEntry{Kind: model.KindPlan, Plan: plan, Score: score, Reasons: reasons})
	}

	slices.SortFunc(scored, compareScored)
	return scored, relevantClock{head: head, now: now, staleAfter: staleAfter}, nil
}

func (c *Client) relevantVerdicts(ctx context.Context, scored []RelevantEntry, clock relevantClock, worktree bool) error {
	for i := range scored {
		verdict, err := c.entryVerdict(ctx, scored[i], clock.head, clock.now, clock.staleAfter, worktree)
		if err != nil {
			return err
		}
		scored[i].Verdict = verdict
	}
	return nil
}

func anchorsNear(anchors []model.Anchor, p string) bool {
	return hasAnchorIn(anchors, model.AnchorPath, p) || deepestDirAnchor(anchors, p) != ""
}

func (c *Client) relevantPath(ctx context.Context, target string) (string, error) {
	if !filepath.IsAbs(target) {
		return path.Clean(target), nil
	}
	root, err := c.s.Git.Root(ctx)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolveSymlinks(root), resolveSymlinks(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return path.Clean(target), nil
	}
	return filepath.ToSlash(rel), nil
}

func resolveSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Join(resolveSymlinks(filepath.Dir(p)), filepath.Base(p))
}

// anchoredNear reports whether reasons include a path or dir match, which gates
// the open-investigation and executing-plan boosts.
func anchoredNear(reasons []string) bool {
	return slices.ContainsFunc(reasons, func(r string) bool {
		return r == reasonPath || r == reasonDir
	})
}

// entryVerdict computes the drift verdict for a kept entity against a single
// head/now snapshot shared across the whole ranked batch, dispatching to the
// note/doc/answer verdict core by kind. A log, runbook, investigation, or plan never
// drifts — none has a freshness lifecycle — so each short-circuits to an empty
// verdict.
func (c *Client) entryVerdict(ctx context.Context, e RelevantEntry, head model.SHA, now time.Time, staleAfter time.Duration, worktree bool) (Verdict, error) {
	switch e.Kind {
	case model.KindDoc:
		return c.verdictOf(ctx, head, freshFromDoc(e.Doc), now, staleAfter, worktree)
	case model.KindAnswer:
		return c.verdictOf(ctx, head, freshFromAnswer(e.Answer), now, staleAfter, worktree)
	case model.KindLog, model.KindRunbook, model.KindInvestigation, model.KindPlan, model.KindLedger:
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
