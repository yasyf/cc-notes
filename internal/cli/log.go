package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func newLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Append-only journals — an incident timeline, a rollout log, a debugging-session record",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newLogAddCmd(),
		newLogAppendCmd(),
		newLogListCmd(),
		newLogShowCmd(),
		newLogEditCmd(),
		newLogRmCmd(),
		newLogSearchCmd(),
		newLogHistoryCmd(),
	)
	return cmd
}

func newLogAddCmd() *cobra.Command {
	var entry string
	var labels, attach []string
	var anchors anchorSets
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a log",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTitle(args[0], titleHintLog); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			commits, err := resolveCommits(ctx, s.Git, anchors.commits)
			if err != nil {
				return err
			}
			create := model.CreateLog{
				Nonce:   model.NewNonce(),
				Title:   args[0],
				Tags:    labels,
				Anchors: buildAnchors(commits, anchors.paths, anchors.dirs, anchors.branches),
			}
			ops := []model.Op{create}
			attOps, err := attachOps(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			snapshot, err := createEntity(ctx, cmd, s, append(ops, attOps...))
			if err != nil {
				return err
			}
			log := snapshot.(model.Log)
			if cmd.Flags().Changed("entry") {
				text, err := bodyArg(cmd, entry)
				if err != nil {
					return err
				}
				appended, err := s.Append(ctx, refs.For(model.KindLog, log.ID), []model.Op{model.AppendEntry{Text: text}})
				if err != nil {
					return err
				}
				log = appended.(model.Log)
			}
			return printLog(cmd, s, log, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&entry, "entry", "", "optional first entry; - reads stdin")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	bindLabels(flags, &labels, "label (repeatable)")
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLogAppendCmd() *cobra.Command {
	var entry string
	var attach []string
	var replace, jsonOut bool
	cmd := &cobra.Command{
		Use:   "append ID [TEXT]",
		Short: "Append one entry to a log; entries are append-only",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("log append requires a log ID")}
			}
			var ops []model.Op
			if len(args) > 1 || cmd.Flags().Changed("entry") {
				text, err := entryText(cmd, args, entry)
				if err != nil {
					return err
				}
				ops = append(ops, model.AppendEntry{Text: text})
			}
			if len(ops) == 0 && len(attach) == 0 {
				return &UsageError{Err: errors.New("log append requires entry text (a positional TEXT, --entry, or - for stdin) or --attach")}
			}
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, log, err := logSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			if !replace {
				if err := checkAttachCollisions(log.Attachments, attach); err != nil {
					return err
				}
			}
			attOps, err := attachOps(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, append(ops, attOps...))
			if err != nil {
				return err
			}
			return printLog(cmd, s, snapshot.(model.Log), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&entry, "entry", "", "entry text")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	flags.BoolVar(&replace, "replace", false, "allow --attach to overwrite a live attachment with the same name")
	bindJSON(flags, &jsonOut)
	return cmd
}

// entryText resolves the entry text for log append from exactly one of: the
// positional TEXT (args[1]), the --entry flag, or - read from stdin.
// Zero or more than one source is a UsageError.
func entryText(cmd *cobra.Command, args []string, entry string) (string, error) {
	positional := len(args) > 1
	flagged := cmd.Flags().Changed("entry")
	stdin := positional && args[1] == "-"
	sources := 0
	if positional && !stdin {
		sources++
	}
	if flagged {
		sources++
	}
	if stdin {
		sources++
	}
	switch sources {
	case 0:
		return "", &UsageError{Err: errors.New("log append requires entry text: a positional TEXT, --entry, or - for stdin")}
	case 1:
		switch {
		case stdin:
			return bodyArg(cmd, "-")
		case positional:
			return args[1], nil
		default:
			return entry, nil
		}
	default:
		return "", &UsageError{Err: errors.New("log append takes entry text from exactly one of a positional TEXT, --entry, or - for stdin")}
	}
}

func newLogListCmd() *cobra.Command {
	return logList.listVerb()
}

func newLogShowCmd() *cobra.Command {
	return logSpec.showVerb("Show one log with its entries in chronological order", showLog)
}

func newLogEditCmd() *cobra.Command {
	var title string
	var rmAttachments []string
	var labels labelEdits
	var anchors anchorEdits
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a log's title, tags, and anchors (entries are append-only)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var ops []model.Op
			if cmd.Flags().Changed("title") {
				if err := validateTitle(title, titleHintLog); err != nil {
					return err
				}
				ops = append(ops, model.SetTitle{Title: title})
			}
			for _, l := range labels.add {
				ops = append(ops, model.AddTag{Tag: l})
			}
			for _, l := range labels.rm {
				ops = append(ops, model.RemoveTag{Tag: l})
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			addCommits, err := resolveCommits(ctx, s.Git, anchors.addCommits)
			if err != nil {
				return err
			}
			for _, a := range buildAnchors(addCommits, anchors.addPaths, anchors.addDirs, anchors.addBranches) {
				ops = append(ops, model.AddAnchor{Anchor: a})
			}
			for _, a := range buildAnchors(anchors.rmCommits, anchors.rmPaths, anchors.rmDirs, anchors.rmBranches) {
				ops = append(ops, model.RemoveAnchor{Anchor: a})
			}
			for _, name := range rmAttachments {
				ops = append(ops, model.RemoveAttachment{Name: name})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("log edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := logSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printLog(cmd, s, snapshot.(model.Log), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	labels.bind(flags)
	anchors.bind(flags)
	flags.StringArrayVar(&rmAttachments, "rm-attachment", nil, "remove attachment by name (repeatable)")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLogRmCmd() *cobra.Command {
	return logSpec.rmVerb()
}

func newLogSearchCmd() *cobra.Command {
	return logList.searchVerb()
}

// rankLogs filters logs by tag, author, and anchors, keeps those matching
// query in their title, a tag, or any entry text, then orders by match tier
// (title > tag > entry), UpdatedAt descending, id ascending, truncated to
// limit.
