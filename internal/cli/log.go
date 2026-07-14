package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
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
		Use:   "add TITLE [BODY]",
		Short: "Create a log",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("log add requires a title")}
			}
			if err := validateTitle(args[0], titleHintLog); err != nil {
				return err
			}
			posGiven := len(args) > 1
			var pos string
			if posGiven {
				pos = args[1]
			}
			first, err := freeText(cmd, "entry", entry, pos, posGiven, false)
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
			log, reused, err := c.CreateLog(ctx, notes.LogSpec{
				Title:       args[0],
				Entry:       first,
				Tags:        labels,
				Anchors:     anchorSetsSpec(anchors),
				Attachments: atts,
			})
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, "log", log.ID)
			}
			return printLog(cmd, c, log, jsonOut)
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
			posGiven := len(args) > 1
			var pos string
			if posGiven {
				pos = args[1]
			}
			hasEntry := posGiven || cmd.Flags().Changed("entry")
			text, err := freeText(cmd, "entry", entry, pos, posGiven, false)
			if err != nil {
				return err
			}
			if !hasEntry && len(attach) == 0 {
				return &UsageError{Err: errors.New("log append requires entry text (a positional TEXT, --entry, or - for stdin) or --attach")}
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveLog(ctx, args[0])
			if err != nil {
				return err
			}
			if !replace {
				log, err := c.Log(ctx, id)
				if err != nil {
					return err
				}
				if err := checkAttachCollisions(log.Attachments, attach); err != nil {
					return err
				}
			}
			atts, err := attachFiles(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			log, err := c.AppendLog(ctx, id, notes.LogAppend{Text: text, Attachments: atts, ReplaceAttachments: replace})
			if err != nil {
				return err
			}
			return printLog(cmd, c, log, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&entry, "entry", "", "entry text")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	flags.BoolVar(&replace, "replace", false, "allow --attach to overwrite a live attachment with the same name")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLogListCmd() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List logs",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			logs, err := c.Logs(cmd.Context(), notes.LogFilter{
				IncludeDeleted: all,
				Labels:         labels,
				Anchors:        anchorFiltersToNotes(filters),
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, s, logs, jsonOut, logListDTO, leanLogLine)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	filters.bind(flags)
	flags.BoolVar(&all, "all", false, "include tombstoned logs")
	bindJSON(flags, &jsonOut)
	return cmd
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
			var in notes.LogEdit
			if cmd.Flags().Changed("title") {
				if err := validateTitle(title, titleHintLog); err != nil {
					return err
				}
				in.Title = &title
			}
			in.AddTags, in.RemoveTags = labels.add, labels.rm
			in.AddAnchors = notes.AnchorSpec{Commits: anchors.addCommits, Paths: anchors.addPaths, Dirs: anchors.addDirs, Branches: anchors.addBranches}
			in.RemoveAnchors = notes.AnchorSpec{Commits: anchors.rmCommits, Paths: anchors.rmPaths, Dirs: anchors.rmDirs, Branches: anchors.rmBranches}
			in.RemoveAttachments = rmAttachments
			if logEditEmpty(in) {
				return &UsageError{Err: errors.New("log edit requires at least one flag")}
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveLog(ctx, args[0])
			if err != nil {
				return err
			}
			log, err := c.EditLog(ctx, id, in)
			if err != nil {
				return err
			}
			return printLog(cmd, c, log, jsonOut)
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

// logEditEmpty reports whether a log edit mask sets nothing — the CLI's "at
// least one flag" guard, which raises a UsageError a pinned test asserts, so it
// stays CLI-side rather than deferring to EditLog's ErrEmptyEdit.
func logEditEmpty(in notes.LogEdit) bool {
	return in.Title == nil &&
		len(in.AddTags) == 0 && len(in.RemoveTags) == 0 &&
		anchorSpecEmpty(in.AddAnchors) && anchorSpecEmpty(in.RemoveAnchors) &&
		len(in.RemoveAttachments) == 0
}

func newLogRmCmd() *cobra.Command {
	return logSpec.rmCmd("Tombstone a log", (*notes.Client).ResolveLog, (*notes.Client).RemoveLog)
}

func newLogSearchCmd() *cobra.Command {
	var labels []string
	var author string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across log titles, labels, and entry text",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			// bindLimit's "0 = all" maps to SearchFilter's negative "no cap".
			kindLimit := limit
			if kindLimit == 0 {
				kindLimit = -1
			}
			logs, err := c.SearchLogs(cmd.Context(), args[0], notes.SearchFilter{
				Labels:  labels,
				Author:  author,
				Anchors: anchorFiltersToNotes(filters),
				Limit:   kindLimit,
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, s, logs, jsonOut, logListDTO, leanLogLine)
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

// logListDTO is the list/search projection for a log: its lean JSON DTO with
// attachment presence.
func logListDTO(l model.Log, atts []attachmentDTO) any { return newLogDTO(l, atts) }
