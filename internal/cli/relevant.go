package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/store"
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

// Relevance signal weights summed into a note's score.
const (
	scorePath         = 100
	scoreDir          = 60
	scoreBranch       = 40
	scoreMergedCommit = 25
	scoreMergedBranch = 20
	scoreSibling      = 15
	scoreCrossAuthor  = 30
)

// reasonOrder is the fixed render order for a note's matched reasons.
var reasonOrder = []string{
	reasonPath, reasonDir, reasonBranch, reasonMergedCommit, reasonMergedBranch, reasonSibling, reasonCrossAuthor,
}

// relevantDTO is one ranked note in the JSON output of relevant: its note DTO
// (carrying the drift verdict), the summed relevance score, and the matched
// reasons in fixed priority order.
type relevantDTO struct {
	Note    noteDTO  `json:"note"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

// scoredNote pairs a kept note with its summed score and matched reasons.
type scoredNote struct {
	note    model.Note
	score   int
	reasons []string
}

func newRelevantCmd() *cobra.Command {
	var branchFlag, baseFlag string
	var limit int
	var jsonOut, attached, worktree bool
	cmd := &cobra.Command{
		Use:   "relevant PATH",
		Short: "Surface the notes most relevant to a path, ranked with reasons",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			scored, verdicts, err := relevantNotes(ctx, s, args[0], branchFlag, baseFlag, attached, worktree)
			if err != nil {
				return err
			}
			if limit >= 0 && len(scored) > limit {
				scored = scored[:limit]
			}
			return printRelevant(cmd, scored, verdicts, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&branchFlag, "branch", "", "branch to weigh against (default: current HEAD branch)")
	flags.StringVar(&baseFlag, "base", "", "merge-base reference for cross-author signals (default: remote default branch)")
	flags.IntVar(&limit, "limit", 10, "maximum results (negative: unlimited)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	flags.BoolVar(&attached, "attached", false, "keep only notes anchored to the path or a parent directory")
	flags.BoolVar(&worktree, "worktree", false, "drift-check path anchors against uncommitted working-tree edits")
	return cmd
}

// relevantNotes scores every live note against target and returns those with a
// positive score, sorted by score descending, then UpdatedAt descending, then
// id ascending, along with each kept note's drift verdict keyed by id.
// branchFlag and baseFlag override the resolved branch and merge-base base;
// attached drops notes not anchored to the path or a parent directory; worktree
// threads through to the drift verdict.
func relevantNotes(ctx context.Context, s *store.Store, target, branchFlag, baseFlag string, attached, worktree bool) ([]scoredNote, map[model.EntityID]string, error) {
	p := path.Clean(target)

	branch, err := resolveRelevantBranch(ctx, s, branchFlag)
	if err != nil {
		return nil, nil, err
	}
	head, err := resolveHead(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	_, me, err := s.Git.AuthorIdent(ctx)
	if err != nil {
		return nil, nil, err
	}
	crossAuthorPaths, err := crossAuthorSet(ctx, s, baseFlag, head, me)
	if err != nil {
		return nil, nil, err
	}
	staleAfter, err := noteStaleAfter(ctx, s.Git)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()

	notes, err := s.ListNotes(ctx, false, false)
	if err != nil {
		return nil, nil, err
	}
	var scored []scoredNote
	for _, n := range notes {
		match, err := scoreNote(ctx, s, n, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, nil, err
		}
		if match.score == 0 {
			continue
		}
		if attached && !slices.ContainsFunc(match.reasons, func(r string) bool {
			return r == reasonPath || r == reasonDir
		}) {
			continue
		}
		match.note = n
		scored = append(scored, match)
	}
	verdicts := make(map[model.EntityID]string, len(scored))
	for _, m := range scored {
		verdict, err := noteVerdict(ctx, s, head, m.note, now, staleAfter, worktree)
		if err != nil {
			return nil, nil, err
		}
		verdicts[m.note.ID] = verdict
	}
	slices.SortFunc(scored, compareScored)
	return scored, verdicts, nil
}

// compareScored is the total ranking order for scored notes: higher score
// first, then newer UpdatedAt first, then lower id first. The order is total
// (ids are unique), so the ranking is fully deterministic regardless of the
// sort's stability.
func compareScored(a, b scoredNote) int {
	if c := cmp.Compare(b.score, a.score); c != 0 {
		return c
	}
	if c := cmp.Compare(b.note.UpdatedAt, a.note.UpdatedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.note.ID, b.note.ID)
}

// resolveRelevantBranch returns the branch to weigh against: the flag verbatim
// when set, otherwise the branch HEAD points at, or "" on a detached HEAD
// (branch signals are then skipped rather than an error).
func resolveRelevantBranch(ctx context.Context, s *store.Store, flag string) (model.Branch, error) {
	if flag != "" {
		if err := s.Git.CheckRefFormat(ctx, flag); err != nil {
			return "", &UsageError{Err: err}
		}
		return model.Branch(flag), nil
	}
	branch, err := s.Git.HeadBranch(ctx)
	if errors.Is(err, gitcmd.ErrDetachedHead) {
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
func crossAuthorSet(ctx context.Context, s *store.Store, baseFlag string, head model.SHA, me string) (map[string]struct{}, error) {
	if head == "" {
		return nil, nil
	}
	base, err := resolveRelevantBase(ctx, s, baseFlag)
	if err != nil {
		return nil, err
	}
	mergeBase, err := s.Git.MergeBase(ctx, string(base), "HEAD")
	if errors.Is(err, gitcmd.ErrRevNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	authors, err := s.Git.RevRangeFileAuthors(ctx, string(mergeBase), "HEAD")
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

// resolveRelevantBase returns the base reference for cross-author detection:
// the flag verbatim when set, otherwise the remote default branch, falling back
// to "main" when origin/HEAD is unset.
func resolveRelevantBase(ctx context.Context, s *store.Store, flag string) (model.Branch, error) {
	if flag != "" {
		return model.Branch(flag), nil
	}
	base, err := s.Git.DefaultBranch(ctx)
	if errors.Is(err, gitcmd.ErrNoDefaultBranch) {
		return "main", nil
	}
	if err != nil {
		return "", err
	}
	return base, nil
}

// scoreNote sums every relevance signal a note matches against the target path
// p, the branch, and head, returning the summed score with reasons in fixed
// priority order. The cross-author boost only fires on a note already matched
// near p (via a path, dir, or sibling anchor whose value a teammate touched);
// it never creates a match on its own.
func scoreNote(ctx context.Context, s *store.Store, n model.Note, p string, branch model.Branch, head model.SHA, crossAuthorPaths map[string]struct{}) (scoredNote, error) {
	var m scoredNote
	var nearPaths []string

	if hasAnchor(n, model.AnchorPath, p) {
		m.add(scorePath, reasonPath)
		nearPaths = append(nearPaths, p)
	}
	if d := deepestDirAnchor(n, p); d != "" {
		m.add(scoreDir, reasonDir)
		nearPaths = append(nearPaths, d)
	}
	if branch != "" && hasAnchor(n, model.AnchorBranch, string(branch)) {
		m.add(scoreBranch, reasonBranch)
	}
	if head != "" {
		merged, err := commitAnchorMerged(ctx, s, n, head)
		if err != nil {
			return scoredNote{}, err
		}
		if merged {
			m.add(scoreMergedCommit, reasonMergedCommit)
		}
		mergedBranch, err := branchAnchorMerged(ctx, s, n, branch, head)
		if err != nil {
			return scoredNote{}, err
		}
		if mergedBranch {
			m.add(scoreMergedBranch, reasonMergedBranch)
		}
	}
	if sib := siblingAnchors(n, p); len(sib) > 0 {
		m.add(scoreSibling, reasonSibling)
		nearPaths = append(nearPaths, sib...)
	}
	if len(nearPaths) > 0 && anyCrossAuthor(nearPaths, crossAuthorPaths) {
		m.add(scoreCrossAuthor, reasonCrossAuthor)
	}
	m.orderReasons()
	return m, nil
}

// add accumulates a signal's weight and reason.
func (m *scoredNote) add(weight int, reason string) {
	m.score += weight
	m.reasons = append(m.reasons, reason)
}

// orderReasons sorts the matched reasons into the fixed priority order.
func (m *scoredNote) orderReasons() {
	slices.SortFunc(m.reasons, func(a, b string) int {
		return cmp.Compare(slices.Index(reasonOrder, a), slices.Index(reasonOrder, b))
	})
}

// deepestDirAnchor returns the deepest dir anchor on n that contains p (p equals
// the dir or sits under it), or "" when none does. Picking the single deepest
// match keeps overlapping dir anchors from stacking their score.
func deepestDirAnchor(n model.Note, p string) string {
	var deepest string
	for _, a := range n.Anchors {
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

// siblingAnchors returns the path anchors on n that share p's parent directory
// without being p itself.
func siblingAnchors(n model.Note, p string) []string {
	parent := path.Dir(p)
	var out []string
	for _, a := range n.Anchors {
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

// commitAnchorMerged reports whether any commit anchor on n is an ancestor of
// (or equal to) head — its work has merged into the current line of history.
func commitAnchorMerged(ctx context.Context, s *store.Store, n model.Note, head model.SHA) (bool, error) {
	for _, a := range n.Anchors {
		if a.Kind != model.AnchorCommit {
			continue
		}
		reachable, err := s.Repo.IsAncestor(ctx, model.SHA(a.Value), head)
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

// branchAnchorMerged reports whether any branch anchor on n names a branch
// other than the target branch whose tip has merged into head. A branch whose
// ref is gone is skipped, not an error.
func branchAnchorMerged(ctx context.Context, s *store.Store, n model.Note, branch model.Branch, head model.SHA) (bool, error) {
	for _, a := range n.Anchors {
		if a.Kind != model.AnchorBranch || model.Branch(a.Value) == branch {
			continue
		}
		tip, err := s.Repo.Tip(ctx, "refs/heads/"+a.Value)
		if errors.Is(err, gitobj.ErrRefNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		reachable, err := s.Repo.IsAncestor(ctx, tip, head)
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

// printRelevant writes the ranked notes as relevantDTOs in JSON, or as lean
// note lines with the matched reasons (and any drift verdict) appended after
// tabs. verdicts carries each note's drift verdict keyed by id.
func printRelevant(cmd *cobra.Command, scored []scoredNote, verdicts map[model.EntityID]string, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]relevantDTO, len(scored))
		for i, m := range scored {
			dtos[i] = relevantDTO{
				Note:    newNoteDTO(m.note, verdicts[m.note.ID]),
				Score:   m.score,
				Reasons: m.reasons,
			}
		}
		return printJSON(out, dtos)
	}
	for _, m := range scored {
		line := leanNoteLine(m.note) + "\t" + csvOrDash(m.reasons)
		if v := verdicts[m.note.ID]; v != "" {
			line += "\t" + v
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}
