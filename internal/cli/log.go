package cli

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
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
	var tags, commits, paths, dirs, branches, attach []string
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
			commits, err := resolveCommits(ctx, s.Git, commits)
			if err != nil {
				return err
			}
			create := model.CreateLog{
				Nonce:   model.NewNonce(),
				Title:   args[0],
				Tags:    tags,
				Anchors: buildAnchors(commits, paths, dirs, branches),
			}
			ops := []model.Op{create}
			attOps, err := attachOps(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			snapshot, err := s.Create(ctx, append(ops, attOps...))
			if err != nil {
				return err
			}
			log := snapshot.(model.Log)
			if cmd.Flags().Changed("entry") {
				text, err := bodyArg(cmd, entry)
				if err != nil {
					return err
				}
				appended, err := s.Append(ctx, refs.Log(log.ID), []model.Op{model.AppendEntry{Text: text}})
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
	flags.StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
	flags.StringArrayVar(&commits, "commit", nil, "commit anchor (repeatable)")
	flags.StringArrayVar(&paths, "path", nil, "path anchor (repeatable)")
	flags.StringArrayVar(&dirs, "dir", nil, "directory anchor (repeatable)")
	flags.StringArrayVar(&branches, "branch", nil, "branch anchor (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newLogAppendCmd() *cobra.Command {
	var message string
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
			if len(args) > 1 || cmd.Flags().Changed("message") {
				text, err := entryText(cmd, args, message)
				if err != nil {
					return err
				}
				ops = append(ops, model.AppendEntry{Text: text})
			}
			if len(ops) == 0 && len(attach) == 0 {
				return &UsageError{Err: errors.New("log append requires entry text (a positional TEXT, -m, or - for stdin) or --attach")}
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
	flags.StringVarP(&message, "message", "m", "", "entry text")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	flags.BoolVar(&replace, "replace", false, "allow --attach to overwrite a live attachment with the same name")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// checkAttachCollisions rejects an --attach whose base name collides with a
// live attachment: replacing content silently would orphan the old bytes
// behind the same name, so the caller must opt in with --replace.
func checkAttachCollisions(live []model.Attachment, paths []string) error {
	names := make(map[string]bool, len(live))
	for _, a := range live {
		names[a.Name] = true
	}
	for _, p := range paths {
		if name := filepath.Base(p); names[name] {
			return &UsageError{Err: fmt.Errorf("attachment %q already exists on this log; pass --replace to overwrite it", name)}
		}
	}
	return nil
}

// entryText resolves the entry text for log append from exactly one of: the
// positional TEXT (args[1]), the -m/--message flag, or - read from stdin.
// Zero or more than one source is a UsageError.
func entryText(cmd *cobra.Command, args []string, message string) (string, error) {
	positional := len(args) > 1
	flagged := cmd.Flags().Changed("message")
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
		return "", &UsageError{Err: errors.New("log append requires entry text: a positional TEXT, -m, or - for stdin")}
	case 1:
		switch {
		case stdin:
			return bodyArg(cmd, "-")
		case positional:
			return args[1], nil
		default:
			return message, nil
		}
	default:
		return "", &UsageError{Err: errors.New("log append takes entry text from exactly one of a positional TEXT, -m, or - for stdin")}
	}
}

func newLogListCmd() *cobra.Command {
	var tags []string
	var path, commit, dir, branch string
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
				return !hasAll(l.Tags, tags) ||
					(commit != "" && !hasAnchorIn(l.Anchors, model.AnchorCommit, commit)) ||
					(path != "" && !hasAnchorIn(l.Anchors, model.AnchorPath, path)) ||
					(dir != "" && !hasAnchorIn(l.Anchors, model.AnchorDir, dir)) ||
					(branch != "" && !hasAnchorIn(l.Anchors, model.AnchorBranch, branch))
			})
			sortLogs(logs)
			return printLogList(cmd, s, logs, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&tags, "tag", nil, "require tag (repeatable, ANDed)")
	flags.StringVar(&path, "path", "", "require path anchor")
	flags.StringVar(&commit, "commit", "", "require commit anchor")
	flags.StringVar(&dir, "dir", "", "require directory anchor")
	flags.StringVar(&branch, "branch", "", "require branch anchor")
	flags.BoolVar(&all, "all", false, "include tombstoned logs")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newLogShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one log with its entries in chronological order",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			_, log, err := loadLog(ctx, s, args[0])
			if err != nil {
				return err
			}
			atts, err := entityAttachments(ctx, s, log.Attachments)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newLogDTO(log, atts))
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), renderLogShow(log, atts))
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newLogEditCmd() *cobra.Command {
	var title string
	var addTags, rmTags, addPaths, rmPaths, addDirs, rmDirs, addCommits, rmCommits, addBranches, rmBranches, rmAttachments []string
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
			for _, tag := range addTags {
				ops = append(ops, model.AddTag{Tag: tag})
			}
			for _, tag := range rmTags {
				ops = append(ops, model.RemoveTag{Tag: tag})
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			addCommits, err := resolveCommits(ctx, s.Git, addCommits)
			if err != nil {
				return err
			}
			for _, a := range buildAnchors(addCommits, addPaths, addDirs, addBranches) {
				ops = append(ops, model.AddAnchor{Anchor: a})
			}
			for _, a := range buildAnchors(rmCommits, rmPaths, rmDirs, rmBranches) {
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
	flags.StringArrayVar(&addTags, "add-tag", nil, "add tag (repeatable)")
	flags.StringArrayVar(&rmTags, "rm-tag", nil, "remove tag (repeatable)")
	flags.StringArrayVar(&addPaths, "add-path", nil, "add path anchor (repeatable)")
	flags.StringArrayVar(&rmPaths, "rm-path", nil, "remove path anchor (repeatable)")
	flags.StringArrayVar(&addDirs, "add-dir", nil, "add directory anchor (repeatable)")
	flags.StringArrayVar(&rmDirs, "rm-dir", nil, "remove directory anchor (repeatable)")
	flags.StringArrayVar(&addCommits, "add-commit", nil, "add commit anchor (repeatable)")
	flags.StringArrayVar(&rmCommits, "rm-commit", nil, "remove commit anchor (repeatable)")
	flags.StringArrayVar(&addBranches, "add-branch", nil, "add branch anchor (repeatable)")
	flags.StringArrayVar(&rmBranches, "rm-branch", nil, "remove branch anchor (repeatable)")
	flags.StringArrayVar(&rmAttachments, "rm-attachment", nil, "remove attachment by name (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
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
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newLogSearchCmd() *cobra.Command {
	var tags []string
	var author, anchorPath, anchorDir, anchorBranch, anchorCommit string
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across log titles, tags, and entry text",
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
			logs = rankLogs(logs, args[0], tags, author, anchorPath, anchorDir, anchorBranch, anchorCommit, limit)
			return printLogList(cmd, s, logs, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&tags, "tag", nil, "require tag (repeatable, ANDed)")
	flags.IntVar(&limit, "limit", 20, "maximum results")
	flags.StringVar(&author, "author", "", "require author")
	flags.StringVar(&anchorPath, "anchor-path", "", "require path anchor")
	flags.StringVar(&anchorDir, "anchor-dir", "", "require directory anchor")
	flags.StringVar(&anchorBranch, "anchor-branch", "", "require branch anchor")
	flags.StringVar(&anchorCommit, "anchor-commit", "", "require commit anchor")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
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
