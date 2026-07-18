package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

var defaultInvestigationStatuses = []model.InvestigationStatus{
	model.InvestigationOpen,
	model.InvestigationRootCaused,
	model.InvestigationFixed,
}

func newInvestigationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "investigation",
		Short: "Investigations: falsifiable premises, evidence timelines, findings, and verdicts",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newInvestigationOpenCmd(),
		newInvestigationListCmd(),
		newInvestigationShowCmd(),
		newInvestigationAppendCmd(),
		newInvestigationFindingCmd(),
		newInvestigationRootCauseCmd(),
		newInvestigationFixCmd(),
		newInvestigationConfirmCmd(),
		newInvestigationExonerateCmd(),
		newInvestigationAbandonCmd(),
		newInvestigationReopenCmd(),
		newInvestigationEditCmd(),
		newInvestigationSearchCmd(),
		newInvestigationHistoryCmd(),
		newInvestigationRmCmd(),
	)
	return cmd
}

func newInvestigationOpenCmd() *cobra.Command {
	var body string
	var findings, labels, attach []string
	var anchors anchorSets
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "open TITLE [BODY]",
		Aliases: []string{"add"},
		Short:   "Open an investigation around an immutable premise",
		Args:    maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("investigation open requires a title")}
			}
			if err := validateInvestigationTitle(cmd, args[0]); err != nil {
				return err
			}
			var pos string
			if len(args) > 1 {
				pos = args[1]
			}
			premise, err := freeText(cmd, "body", body, pos, len(args) > 1, true)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			if anchors.commits, err = resolveCommits(ctx, s.Git, anchors.commits); err != nil {
				return err
			}
			atts, err := attachFiles(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			inv, reused, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{
				Title:       args[0],
				Premise:     premise,
				Tags:        labels,
				Anchors:     anchorSetsSpec(anchors),
				Findings:    findings,
				Attachments: atts,
			})
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, "investigation", inv.ID)
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "investigation premise; - reads stdin")
	flags.StringArrayVar(&findings, "finding", nil, "initial finding text (repeatable)")
	bindLabels(flags, &labels, "label (repeatable)")
	anchors.bind(flags)
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newInvestigationListCmd() *cobra.Command {
	var statusCSV string
	var labels []string
	var filters anchorFilters
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active investigations (open, root-caused, or fixed unless --all)",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			statuses := defaultInvestigationStatuses
			switch {
			case all:
				statuses = nil
			case cmd.Flags().Changed("status"):
				statuses = nil
				for _, part := range strings.Split(statusCSV, ",") {
					status, err := parseInvestigationStatus(part)
					if err != nil {
						return err
					}
					statuses = append(statuses, status)
				}
			}
			invs, err := c.Investigations(cmd.Context(), notes.InvestigationFilter{
				Statuses: statuses,
				Labels:   labels,
				Anchors:  anchorFiltersToNotes(filters),
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, s, invs, jsonOut, investigationListDTO, leanInvestigationLine)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&statusCSV, "status", "", "status filter, comma-separated (default open,root_caused,fixed)")
	flags.BoolVar(&all, "all", false, "every status")
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	cmd.MarkFlagsMutuallyExclusive("all", "status")
	return cmd
}

func newInvestigationShowCmd() *cobra.Command {
	return investigationSpec.showVerb("Show one investigation with its findings and timeline", showInvestigation)
}

func newInvestigationAppendCmd() *cobra.Command {
	var attach []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "append ID [TEXT]",
		Short: "Append evidence to an investigation's timeline",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("investigation append requires an investigation ID")}
			}
			var text string
			var err error
			if len(args) > 1 {
				text, err = bodyArg(cmd, args[1])
				if err != nil {
					return err
				}
			}
			if len(args) == 1 && len(attach) == 0 {
				return &UsageError{Err: errors.New("investigation append requires entry text (a positional TEXT or - for stdin) or --attach")}
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := c.Investigation(ctx, id)
			if err != nil {
				return err
			}
			if err := checkAttachCollisions(inv.Attachments, attach); err != nil {
				return err
			}
			atts, err := attachFiles(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			inv, err = c.AppendInvestigation(ctx, id, notes.InvestigationAppend{Text: text, Attachments: atts})
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newInvestigationFindingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finding",
		Short: "Structured hypotheses and review findings under investigation",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newFindingAddCmd(),
		newFindingEditCmd(),
		newFindingStatusCmd("clear", model.FindingCleared),
		newFindingStatusCmd("confirm", model.FindingConfirmed),
		newFindingRemoveCmd(),
		newFindingListCmd(),
	)
	return cmd
}

func newFindingAddCmd() *cobra.Command {
	var body string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add INVESTIGATION [TEXT]",
		Short: "Add an open finding to an investigation",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("investigation finding add requires an investigation ID")}
			}
			var pos string
			if len(args) > 1 {
				pos = args[1]
			}
			text, err := freeText(cmd, "body", body, pos, len(args) > 1, true)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := c.AddFinding(ctx, id, text)
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	bindBody(cmd.Flags(), &body, "finding text; - reads stdin")
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newFindingEditCmd() *cobra.Command {
	var body string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit INVESTIGATION FINDING [TEXT]",
		Short: "Edit a finding's text",
		Args:  maxArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return &UsageError{Err: errors.New("investigation finding edit requires an investigation ID and finding ID")}
			}
			var pos string
			if len(args) > 2 {
				pos = args[2]
			}
			text, err := freeText(cmd, "body", body, pos, len(args) > 2, true)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := c.EditFinding(ctx, id, args[1], text)
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	bindBody(cmd.Flags(), &body, "new finding text; - reads stdin")
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newFindingStatusCmd(use string, status model.FindingStatus) *cobra.Command {
	var why string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " INVESTIGATION FINDING",
		Short: "Mark a finding " + string(status),
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why == "" {
				return &UsageError{Err: fmt.Errorf("%s requires --why with the evidence for the disposition", cmd.CommandPath())}
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			var inv model.Investigation
			switch status {
			case model.FindingCleared:
				inv, err = c.SetFindingCleared(ctx, id, args[1], why)
			case model.FindingConfirmed:
				inv, err = c.SetFindingConfirmed(ctx, id, args[1], why)
			}
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&why, "why", "", "evidence supporting the finding disposition")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newFindingRemoveCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm INVESTIGATION FINDING",
		Short: "Remove a finding from an investigation",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := c.RemoveFinding(ctx, id, args[1])
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newFindingListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list INVESTIGATION",
		Short: "List an investigation's findings",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := c.Investigation(ctx, id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, findingDTOs(inv.Findings))
			}
			for _, finding := range inv.Findings {
				if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", render.ShortWireID(finding.ID), finding.Status, finding.Text); err != nil {
					return err
				}
				if finding.Note != "" {
					if _, err := fmt.Fprintf(out, "\twhy: %s\n", finding.Note); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newInvestigationRootCauseCmd() *cobra.Command {
	return investigationTextTransitionCmd("root-cause ID TEXT", "Record the root cause", true, (*notes.Client).RootCause)
}

func newInvestigationConfirmCmd() *cobra.Command {
	return investigationTextTransitionCmd("confirm ID TEXT", "Confirm the fix with proof", true, (*notes.Client).Confirm)
}

func newInvestigationExonerateCmd() *cobra.Command {
	return investigationTextTransitionCmd("exonerate ID TEXT", "Falsify the investigation premise", true, (*notes.Client).Exonerate)
}

func newInvestigationAbandonCmd() *cobra.Command {
	return investigationTextTransitionCmd("abandon ID [TEXT]", "Abandon an investigation without a verdict", false, (*notes.Client).Abandon)
}

func newInvestigationReopenCmd() *cobra.Command {
	return investigationTextTransitionCmd("reopen ID TEXT", "Reopen an investigation with a reason", true, (*notes.Client).Reopen)
}

// investigationTextTransitionCmd builds a transition verb taking ID and a TEXT
// entry. When requireText, TEXT is a mandatory non-empty positional enforced
// before the store opens (usage error, exit 2); otherwise TEXT is optional and
// only the ID is required.
func investigationTextTransitionCmd(use, short string, requireText bool, transition func(*notes.Client, context.Context, model.EntityID, string) (model.Investigation, error)) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args: func(cmd *cobra.Command, args []string) error {
			if requireText {
				return exactArgs(2)(cmd, args)
			}
			if len(args) == 0 {
				return &UsageError{Err: fmt.Errorf("%s requires an investigation ID", cmd.CommandPath())}
			}
			return maxArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var text string
			var err error
			if len(args) > 1 {
				text, err = bodyArg(cmd, args[1])
				if err != nil {
					return err
				}
			}
			if requireText && text == "" {
				return &UsageError{Err: fmt.Errorf("%s requires text", cmd.CommandPath())}
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := transition(c, ctx, id, text)
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newInvestigationFixCmd() *cobra.Command {
	var commits []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "fix ID [TEXT]",
		Short: "Record fixing commits and mark an investigation fixed",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("investigation fix requires an investigation ID")}
			}
			if len(commits) == 0 {
				return &UsageError{Err: errors.New("investigation fix requires at least one --commit")}
			}
			var text string
			var err error
			if len(args) > 1 {
				text, err = bodyArg(cmd, args[1])
				if err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := c.Fix(ctx, id, text, commits)
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&commits, "commit", nil, "fixing commit (repeatable; at least one required)")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newInvestigationEditCmd() *cobra.Command {
	var title, body string
	var labels labelEdits
	var anchors anchorEdits
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit an investigation's title, resolution, labels, or anchors",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			var edit notes.InvestigationEdit
			if flags.Changed("title") {
				if err := validateInvestigationTitle(cmd, title); err != nil {
					return err
				}
				edit.Title = &title
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				edit.Body = &text
			}
			edit.AddTags, edit.RemoveTags = labels.add, labels.rm
			edit.AddAnchors = notes.AnchorSpec{Commits: anchors.addCommits, Paths: anchors.addPaths, Dirs: anchors.addDirs, Branches: anchors.addBranches}
			edit.RemoveAnchors = notes.AnchorSpec{Commits: anchors.rmCommits, Paths: anchors.rmPaths, Dirs: anchors.rmDirs, Branches: anchors.rmBranches}
			if investigationEditEmpty(edit) {
				return &UsageError{Err: errors.New("investigation edit requires at least one flag")}
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveInvestigation(ctx, args[0])
			if err != nil {
				return err
			}
			inv, err := c.EditInvestigation(ctx, id, edit)
			if err != nil {
				return err
			}
			return printInvestigation(cmd, c, inv, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new resolution summary; - reads stdin")
	labels.bind(flags)
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newInvestigationSearchCmd() *cobra.Command {
	var labels []string
	var author string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across investigation titles, premises, timelines, findings, and verdicts",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			kindLimit := limit
			if kindLimit == 0 {
				kindLimit = -1
			}
			invs, err := c.SearchInvestigations(cmd.Context(), args[0], notes.SearchFilter{
				Labels:  labels,
				Author:  author,
				Anchors: anchorFiltersToNotes(filters),
				Limit:   kindLimit,
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, s, invs, jsonOut, investigationListDTO, leanInvestigationLine)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	bindLimit(flags, &limit, 20)
	flags.StringVar(&author, "author", "", "require author")
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newInvestigationHistoryCmd() *cobra.Command {
	return kindHistoryCmd(model.KindInvestigation, "investigation")
}

func newInvestigationRmCmd() *cobra.Command {
	return investigationSpec.rmCmd("Tombstone an investigation", (*notes.Client).ResolveInvestigation, (*notes.Client).RemoveInvestigation)
}

func investigationEditEmpty(edit notes.InvestigationEdit) bool {
	return edit.Title == nil && edit.Body == nil &&
		len(edit.AddTags) == 0 && len(edit.RemoveTags) == 0 &&
		anchorSpecEmpty(edit.AddAnchors) && anchorSpecEmpty(edit.RemoveAnchors)
}

func investigationListDTO(inv model.Investigation, atts []attachmentDTO) any {
	return newInvestigationDTO(inv, atts)
}
