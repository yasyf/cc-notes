package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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

// relevantDTO is one ranked entity in the JSON output of relevant: a kind
// discriminator ("note"|"doc"|"log"), the matching entity DTO (the note DTO on
// a note entry, the doc DTO — carrying the free-text trigger — on a doc entry,
// the log DTO on a log entry; notes and docs carry the drift verdict, logs
// never drift), the summed relevance score, and the matched reasons in fixed
// priority order. Note, Doc, and Log are mutually exclusive; the unused ones
// are omitted so the float hook can index entry["note"]/entry["doc"]/
// entry["log"] by kind.
type relevantDTO struct {
	Kind    string   `json:"kind"`
	Note    *noteDTO `json:"note,omitempty"`
	Doc     *docDTO  `json:"doc,omitempty"`
	Log     *logDTO  `json:"log,omitempty"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

// scoredNote pairs a kept note with its summed score and matched reasons.
type scoredNote struct {
	note    model.Note
	score   int
	reasons []string
}

// scoredEntity is one kept entity in the merged ranking: a kind discriminator
// and exactly one of the note, doc, or log it carries, plus the summed score
// and matched reasons. Notes, docs, and logs are ranked together by
// compareScored.
type scoredEntity struct {
	kind    refs.Kind
	note    model.Note
	doc     model.Doc
	log     model.Log
	score   int
	reasons []string
}

// id returns the kept entity's id, regardless of kind.
func (e scoredEntity) id() model.EntityID {
	switch e.kind {
	case refs.KindDoc:
		return e.doc.ID
	case refs.KindLog:
		return e.log.ID
	default:
		return e.note.ID
	}
}

// updatedAt returns the kept entity's last-update time, regardless of kind.
func (e scoredEntity) updatedAt() int64 {
	switch e.kind {
	case refs.KindDoc:
		return e.doc.UpdatedAt
	case refs.KindLog:
		return e.log.UpdatedAt
	default:
		return e.note.UpdatedAt
	}
}

func newRelevantCmd() *cobra.Command {
	var branchFlag, baseFlag string
	var limit int
	var jsonOut, attached, worktree bool
	cmd := &cobra.Command{
		Use:   "relevant PATH",
		Short: "Surface the notes, docs, and logs most relevant to a path, ranked with reasons",
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
	flags.BoolVar(&attached, "attached", false, "keep only entities anchored to the path or a parent directory")
	flags.BoolVar(&worktree, "worktree", false, "drift-check path anchors against uncommitted working-tree edits")
	return cmd
}

// relevantNotes scores every live note, doc, and log against target and returns
// those with a positive score, sorted by score descending, then UpdatedAt descending,
// then id ascending, along with each kept entity's drift verdict keyed by id.
// branchFlag and baseFlag override the resolved branch and merge-base base;
// attached drops entities not anchored to the path or a parent directory;
// worktree threads through to the drift verdict.
func relevantNotes(ctx context.Context, s *store.Store, target, branchFlag, baseFlag string, attached, worktree bool) ([]scoredEntity, map[model.EntityID]string, error) {
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
	var scored []scoredEntity
	for _, n := range notes {
		match, err := scoreNote(ctx, s, n, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, nil, err
		}
		if match.score == 0 {
			continue
		}
		if attached && !anchoredNear(match.reasons) {
			continue
		}
		scored = append(scored, scoredEntity{kind: refs.KindNote, note: match.note, score: match.score, reasons: match.reasons})
	}

	docs, err := s.ListDocs(ctx, false, false)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range docs {
		score, reasons, err := scoreAnchors(ctx, s, d.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, nil, err
		}
		if score == 0 {
			continue
		}
		if attached && !anchoredNear(reasons) {
			continue
		}
		scored = append(scored, scoredEntity{kind: refs.KindDoc, doc: d, score: score, reasons: reasons})
	}

	logs, err := s.ListLogs(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	for _, l := range logs {
		score, reasons, err := scoreAnchors(ctx, s, l.Anchors, p, branch, head, crossAuthorPaths)
		if err != nil {
			return nil, nil, err
		}
		if score == 0 {
			continue
		}
		if attached && !anchoredNear(reasons) {
			continue
		}
		scored = append(scored, scoredEntity{kind: refs.KindLog, log: l, score: score, reasons: reasons})
	}

	verdicts := make(map[model.EntityID]string, len(scored))
	for _, e := range scored {
		verdict, err := entityVerdict(ctx, s, head, e, now, staleAfter, worktree)
		if err != nil {
			return nil, nil, err
		}
		verdicts[e.id()] = verdict
	}
	slices.SortFunc(scored, compareScored)
	return scored, verdicts, nil
}

// anchoredNear reports whether reasons include a path or dir match — the test
// --attached applies to drop entities matched only by looser signals.
func anchoredNear(reasons []string) bool {
	return slices.ContainsFunc(reasons, func(r string) bool {
		return r == reasonPath || r == reasonDir
	})
}

// entityVerdict computes the drift verdict for a kept entity against live
// content at head, dispatching to the shared note/doc verdict core by kind. A
// log never drifts — it has no freshness lifecycle — so it short-circuits to an
// empty verdict.
func entityVerdict(ctx context.Context, s *store.Store, head model.SHA, e scoredEntity, now time.Time, staleAfter time.Duration, worktree bool) (string, error) {
	switch e.kind {
	case refs.KindDoc:
		return docVerdict(ctx, s, head, e.doc, now, staleAfter, worktree)
	case refs.KindLog:
		return "", nil
	default:
		return noteVerdict(ctx, s, head, e.note, now, staleAfter, worktree)
	}
}

// compareScored is the total ranking order for scored entities: higher score
// first, then newer UpdatedAt first, then lower id first. The order is total
// (ids are unique across kinds), so the ranking is fully deterministic
// regardless of the sort's stability.
func compareScored(a, b scoredEntity) int {
	if c := cmp.Compare(b.score, a.score); c != 0 {
		return c
	}
	if c := cmp.Compare(b.updatedAt(), a.updatedAt()); c != 0 {
		return c
	}
	return cmp.Compare(a.id(), b.id())
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

// scoreNote sums every relevance signal note n matches against the target path
// p, the branch, and head, returning a scoredNote with the summed score and
// reasons in fixed priority order. It is a thin projection over n.Anchors onto
// the shared scoreAnchors core, so notes and docs score identically.
func scoreNote(ctx context.Context, s *store.Store, n model.Note, p string, branch model.Branch, head model.SHA, crossAuthorPaths map[string]struct{}) (scoredNote, error) {
	score, reasons, err := scoreAnchors(ctx, s, n.Anchors, p, branch, head, crossAuthorPaths)
	if err != nil {
		return scoredNote{}, err
	}
	return scoredNote{note: n, score: score, reasons: reasons}, nil
}

// scoreAnchors sums every relevance signal the anchors match against the target
// path p, the branch, and head, returning the summed score with reasons in
// fixed priority order. Note and Doc share the same Anchors, so the weights and
// reason strings are reused verbatim across both kinds. The cross-author boost
// only fires on anchors already matched near p (via a path, dir, or sibling
// anchor whose value a teammate touched); it never creates a match on its own.
func scoreAnchors(ctx context.Context, s *store.Store, anchors []model.Anchor, p string, branch model.Branch, head model.SHA, crossAuthorPaths map[string]struct{}) (int, []string, error) {
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
		merged, err := commitAnchorMerged(ctx, s, anchors, head)
		if err != nil {
			return 0, nil, err
		}
		if merged {
			add(scoreMergedCommit, reasonMergedCommit)
		}
		mergedBranch, err := branchAnchorMerged(ctx, s, anchors, branch, head)
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
func commitAnchorMerged(ctx context.Context, s *store.Store, anchors []model.Anchor, head model.SHA) (bool, error) {
	for _, a := range anchors {
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

// branchAnchorMerged reports whether any branch anchor in anchors names a branch
// other than the target branch whose tip has merged into head. A branch whose
// ref is gone is skipped, not an error.
func branchAnchorMerged(ctx context.Context, s *store.Store, anchors []model.Anchor, branch model.Branch, head model.SHA) (bool, error) {
	for _, a := range anchors {
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

// printRelevant writes the ranked entities as relevantDTOs in JSON, or as lean
// lines with the matched reasons (and any drift verdict) appended after tabs.
// A doc line additionally carries a bracketed verdict flag and a "doc show
// <short-id>" hint, and never the long body. verdicts carries each entity's
// drift verdict keyed by id.
func printRelevant(cmd *cobra.Command, scored []scoredEntity, verdicts map[model.EntityID]string, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]relevantDTO, len(scored))
		for i, e := range scored {
			dto := relevantDTO{Kind: string(e.kind), Score: e.score, Reasons: e.reasons}
			switch e.kind {
			case refs.KindDoc:
				d := newDocDTO(e.doc, verdicts[e.doc.ID])
				dto.Doc = &d
			case refs.KindLog:
				l := newLogDTO(e.log)
				dto.Log = &l
			default:
				n := newNoteDTO(e.note, verdicts[e.note.ID])
				dto.Note = &n
			}
			dtos[i] = dto
		}
		return printJSON(out, dtos)
	}
	for _, e := range scored {
		var line string
		switch e.kind {
		case refs.KindDoc:
			line = leanDocLine(e.doc) + "\t" + csvOrDash(e.reasons)
			if v := verdicts[e.doc.ID]; v != "" {
				line += "\t" + verdictFlag(v)
			}
			line += "\tdoc show " + e.doc.ID.Short()
		case refs.KindLog:
			line = leanLogLine(e.log) + "\t" + csvOrDash(e.reasons) + "\tlog show " + e.log.ID.Short()
		default:
			line = leanNoteLine(e.note) + "\t" + csvOrDash(e.reasons)
			if v := verdicts[e.note.ID]; v != "" {
				line += "\t" + v
			}
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

// verdictFlag renders a non-empty verdict as a lowercase bracketed flag, e.g.
// STALE -> "[stale]", so an out-of-date doc surfaces flagged when floated.
func verdictFlag(verdict string) string {
	return "[" + strings.ToLower(verdict) + "]"
}
