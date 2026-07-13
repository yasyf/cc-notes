package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if branchFlag != "" {
				if err := s.Git.CheckRefFormat(ctx, branchFlag); err != nil {
					return &UsageError{Err: err}
				}
			}
			entries, err := c.Relevant(ctx, args[0], notes.RelevantFilter{Branch: branchFlag, Base: baseFlag, Attached: attached, Worktree: worktree})
			if err != nil {
				return err
			}
			if limit >= 0 && len(entries) > limit {
				entries = entries[:limit]
			}
			return printRelevant(cmd, c, entries, jsonOut)
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

// printRelevant writes the ranked entries as relevantDTOs in JSON, or as lean
// lines with the matched reasons (and any drift verdict) appended after tabs.
// A doc line additionally carries a bracketed verdict flag and a "doc show
// <short-id>" hint, and never the long body. Each entry carries its own drift
// verdict; a log never drifts, so its verdict is empty.
func printRelevant(cmd *cobra.Command, c *notes.Client, entries []notes.RelevantEntry, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]relevantDTO, len(entries))
		for i, e := range entries {
			dto := relevantDTO{Kind: string(e.Kind), Score: e.Score, Reasons: e.Reasons}
			switch e.Kind {
			case model.KindDoc:
				infos, err := c.AttachmentInfos(cmd.Context(), e.Doc.Attachments)
				if err != nil {
					return err
				}
				d := newDocDTO(e.Doc, string(e.Verdict), attachmentInfoDTOs(infos))
				dto.Doc = &d
			case model.KindLog:
				infos, err := c.AttachmentInfos(cmd.Context(), e.Log.Attachments)
				if err != nil {
					return err
				}
				l := newLogDTO(e.Log, attachmentInfoDTOs(infos))
				dto.Log = &l
			default:
				infos, err := c.AttachmentInfos(cmd.Context(), e.Note.Attachments)
				if err != nil {
					return err
				}
				n := newNoteDTO(e.Note, string(e.Verdict), attachmentInfoDTOs(infos))
				dto.Note = &n
			}
			dtos[i] = dto
		}
		return printJSON(out, dtos)
	}
	for _, e := range entries {
		var line string
		switch e.Kind {
		case model.KindDoc:
			line = leanDocLine(e.Doc) + "\t" + csvOrDash(e.Reasons)
			if e.Verdict != "" {
				line += "\t" + verdictFlag(string(e.Verdict))
			}
			line += "\tdoc show " + e.Doc.ID.Short()
		case model.KindLog:
			line = leanLogLine(e.Log) + "\t" + csvOrDash(e.Reasons) + "\tlog show " + e.Log.ID.Short()
		default:
			line = leanNoteLine(e.Note) + "\t" + csvOrDash(e.Reasons)
			if e.Verdict != "" {
				line += "\t" + string(e.Verdict)
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

// hasAnchorIn reports whether anchors contains an anchor of the given kind and
// value.
func hasAnchorIn(anchors []model.Anchor, kind model.AnchorKind, value string) bool {
	return slices.Contains(anchors, model.Anchor{Kind: kind, Value: value})
}
