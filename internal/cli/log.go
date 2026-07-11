package cli

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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
			ref, log, err := loadLog(ctx, s, args[0])
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
	var labels []string
	var filters anchorFilters
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List logs",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			logs, err := s.ListLogs(cmd.Context(), all)
			if err != nil {
				return err
			}
			logs = slices.DeleteFunc(logs, func(l model.Log) bool {
				return !hasAll(l.Tags, labels) ||
					(filters.commit != "" && !hasAnchorIn(l.Anchors, model.AnchorCommit, filters.commit)) ||
					(filters.path != "" && !hasAnchorIn(l.Anchors, model.AnchorPath, filters.path)) ||
					(filters.dir != "" && !hasAnchorIn(l.Anchors, model.AnchorDir, filters.dir)) ||
					(filters.branch != "" && !hasAnchorIn(l.Anchors, model.AnchorBranch, filters.branch))
			})
			sortLogs(logs)
			return printLogList(cmd, s, logs, jsonOut)
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
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one log with its entries in chronological order",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			return showLog(cmd, s, args[0], jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
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
			ref, _, err := loadLog(ctx, s, args[0])
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
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID",
		Short: "Tombstone a log",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := loadLog(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}})
			if err != nil {
				return err
			}
			return printLog(cmd, s, snapshot.(model.Log), jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
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
			s, err := openStore()
			if err != nil {
				return err
			}
			logs, err := s.ListLogs(cmd.Context(), false)
			if err != nil {
				return err
			}
			logs = rankLogs(logs, args[0], labels, author, filters.path, filters.dir, filters.branch, filters.commit, limit)
			return printLogList(cmd, s, logs, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	flags.IntVar(&limit, "limit", 20, "maximum results")
	flags.StringVar(&author, "author", "", "require author")
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

// rankLogs filters logs by tag, author, and anchors, keeps those matching
// query in their title, a tag, or any entry text, then orders by match tier
// (title > tag > entry), UpdatedAt descending, id ascending, truncated to
// limit.
func rankLogs(logs []model.Log, query string, tags []string, author, anchorPath, anchorDir, anchorBranch, anchorCommit string, limit int) []model.Log {
	q := strings.ToLower(query)
	type scored struct {
		log  model.Log
		tier int
	}
	var ranked []scored
	for _, l := range logs {
		if !hasAll(l.Tags, tags) ||
			(author != "" && string(l.Author) != author) ||
			(anchorPath != "" && !hasAnchorIn(l.Anchors, model.AnchorPath, anchorPath)) ||
			(anchorDir != "" && !hasAnchorIn(l.Anchors, model.AnchorDir, anchorDir)) ||
			(anchorBranch != "" && !hasAnchorIn(l.Anchors, model.AnchorBranch, anchorBranch)) ||
			(anchorCommit != "" && !hasAnchorIn(l.Anchors, model.AnchorCommit, anchorCommit)) {
			continue
		}
		tier := logTier(l, q)
		if tier == 0 {
			continue
		}
		ranked = append(ranked, scored{log: l, tier: tier})
	}
	slices.SortFunc(ranked, func(a, b scored) int {
		if c := cmp.Compare(b.tier, a.tier); c != 0 {
			return c
		}
		if c := cmp.Compare(b.log.UpdatedAt, a.log.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.log.ID, b.log.ID)
	})
	if limit >= 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]model.Log, len(ranked))
	for i, r := range ranked {
		out[i] = r.log
	}
	return out
}

// logTier ranks how l matches q: a title substring is tier 3, a tag substring
// tier 2, any entry text substring tier 1, and no match tier 0. The comparison
// is case-insensitive; q must already be lowercased.
func logTier(l model.Log, q string) int {
	if strings.Contains(strings.ToLower(l.Title), q) {
		return 3
	}
	for _, tag := range l.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return 2
		}
	}
	for _, e := range l.Entries {
		if strings.Contains(strings.ToLower(e.Text), q) {
			return 1
		}
	}
	return 0
}

func printLogList(cmd *cobra.Command, s *store.Store, logs []model.Log, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]logDTO, len(logs))
		for i, l := range logs {
			atts, err := entityAttachments(cmd.Context(), s, l.Attachments)
			if err != nil {
				return err
			}
			dtos[i] = newLogDTO(l, atts)
		}
		return printJSON(out, dtos)
	}
	for _, l := range logs {
		if _, err := fmt.Fprintln(out, leanLogLine(l)); err != nil {
			return err
		}
	}
	return nil
}
