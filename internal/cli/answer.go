package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const answerMatchLimit = 5

func newAnswerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "answer",
		Short: "Questions a user answered, recorded verbatim with the full note freshness lifecycle",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newAnswerAddCmd(),
		newAnswerListCmd(),
		newAnswerShowCmd(),
		newAnswerEditCmd(),
		newAnswerRmCmd(),
		newAnswerSearchCmd(),
		newAnswerMatchCmd(),
		newAnswerVerifyCmd(),
		newAnswerSupersedeCmd(),
		newAnswerExpireCmd(),
		newAnswerReviewCmd(),
		newAnswerHistoryCmd(),
	)
	return cmd
}

func newAnswerAddCmd() *cobra.Command { return answerDocument.addVerb() }

func newAnswerListCmd() *cobra.Command { return answerDocument.listVerb() }

func newAnswerShowCmd() *cobra.Command {
	return answerSpec.showVerb("Show one answer", showAnswer)
}

func newAnswerEditCmd() *cobra.Command { return answerDocument.editVerb() }

func newAnswerRmCmd() *cobra.Command {
	return answerSpec.rmCmd("Tombstone an answer", (*notes.Client).ResolveAnswer, (*notes.Client).RemoveAnswer)
}

func newAnswerSearchCmd() *cobra.Command { return answerDocument.searchVerb() }

// newAnswerMatchCmd builds "answer match TEXT": the answers whose own words a
// text uses, for a caller that has a text and no idea what was ruled on it.
func newAnswerMatchCmd() *cobra.Command {
	var labels []string
	var limit int
	var nearest, jsonOut bool
	cmd := &cobra.Command{
		Use:   "match TEXT",
		Short: "Rank answers by the rare words they share with TEXT; - reads stdin",
		Args:  minArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			text := strings.Join(args, " ")
			if text == "-" {
				read, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(read)
			}
			rank := c.MatchAnswers
			if nearest {
				rank = c.NearestAnswers
			}
			found, err := rank(cmd.Context(), text, notes.SearchFilter{Labels: labels, Limit: limit})
			if err != nil {
				return err
			}
			if jsonOut {
				dtos := make([]answerMatchDTO, len(found))
				for i, match := range found {
					dtos[i] = answerMatchDTO{newAnswerSummaryDTO(match.Answer, ""), match.Phrase, match.Terms, match.Score}
				}
				return printJSON(cmd.OutOrStdout(), dtos)
			}
			for _, match := range found {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), leanAnswerLine(match.Answer)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	flags.IntVar(&limit, "limit", answerMatchLimit, "maximum results")
	flags.BoolVar(&nearest, "nearest", false, "rank every answer sharing a rare word of TEXT, without the floors, for a reader who checks each one")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newAnswerVerifyCmd() *cobra.Command { return answerDocument.verifyVerb() }

func newAnswerSupersedeCmd() *cobra.Command { return answerDocument.supersedeVerb() }

func newAnswerExpireCmd() *cobra.Command { return answerDocument.expireVerb() }

func newAnswerReviewCmd() *cobra.Command { return answerDocument.reviewVerb() }

var answerDocument = documentSpec[model.Answer]{
	kind:        answerSpec,
	noun:        "answer",
	searchShort: "Ranked search across answer questions, labels, and answers",
	adapter:     answerAdapter,
	addLong: "Record an answer from flags — TITLE is the question verbatim and --body the\n" +
		"chosen answer — or as a file: --checkout writes a template prefilled from any\n" +
		"TITLE and anchor/label flags to an editable file and prints its path; fill it\n" +
		"in, then --apply <path> to create the answer (--abort <path> discards it).",
	editLong: "Edit an answer by flags, or as a file: --checkout writes the answer to an\n" +
		"editable Markdown file and prints its path; edit that file with your normal\n" +
		"tools, then --apply to commit the change (or --abort to discard).",
	newSummaryDTO: func(a model.Answer, drift string) any { return newAnswerSummaryDTO(a, drift) },
	newRecordDTO:  func(a model.Answer) any { return newAnswerDTO(a, "", nil, nil, nil) },
	lean:          leanAnswerLine,
	resolve: func(ctx context.Context, c *notes.Client, prefix string) (model.EntityID, error) {
		return c.ResolveAnswer(ctx, prefix)
	},
	create: func(ctx context.Context, c *notes.Client, title, body, _ string, tags []string, anchors notes.AnchorSpec, atts []model.Attachment) (model.Answer, bool, error) {
		return c.CreateAnswer(ctx, notes.NoteSpec{Title: title, Body: body, Tags: tags, Anchors: anchors, Attachments: atts})
	},
	edit: func(ctx context.Context, c *notes.Client, id model.EntityID, in documentEdit) (model.Answer, error) {
		return c.EditAnswer(ctx, id, notes.NoteEdit{
			Title: in.title, Body: in.body,
			AddTags: in.addTags, RemoveTags: in.rmTags,
			AddAnchors: in.addAnchors, RemoveAnchors: in.rmAnchors,
			Attachments: in.attachments, ReplaceAttachments: in.replaceAttachments, RemoveAttachments: in.rmAttachments,
		})
	},
	verify: func(ctx context.Context, c *notes.Client, id model.EntityID) (model.Answer, error) {
		return c.VerifyAnswer(ctx, id)
	},
	supersede: func(ctx context.Context, c *notes.Client, id, by model.EntityID, clearFlag bool) (model.Answer, error) {
		if clearFlag {
			return c.UnsupersedeAnswer(ctx, id, by)
		}
		return c.SupersedeAnswer(ctx, id, by)
	},
	expire: func(ctx context.Context, c *notes.Client, id model.EntityID, reason string, clearFlag bool) (model.Answer, error) {
		if clearFlag {
			return c.UnexpireAnswer(ctx, id)
		}
		return c.ExpireAnswer(ctx, id, reason)
	},
	review: func(ctx context.Context, c *notes.Client, staleAfter time.Duration) ([]reviewed[model.Answer], error) {
		rs, err := c.ReviewAnswers(ctx, staleAfter)
		if err != nil {
			return nil, err
		}
		out := make([]reviewed[model.Answer], len(rs))
		for i, r := range rs {
			out[i] = reviewed[model.Answer]{entity: r.Answer, verdict: string(r.Verdict)}
		}
		return out, nil
	},
	list: func(ctx context.Context, c *notes.Client, f notes.DocumentFilter) ([]model.Answer, error) {
		return c.Answers(ctx, f)
	},
	search: func(ctx context.Context, c *notes.Client, query string, f notes.SearchFilter) ([]model.Answer, error) {
		return c.SearchAnswers(ctx, query, f)
	},
}
