package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func newLedgerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Ledgers: keyed row sets an agent refreshes in place from an external system",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newLedgerAddCmd(),
		newLedgerListCmd(),
		newLedgerShowCmd(),
		newLedgerStatusCmd("activate", model.LedgerActive),
		newLedgerStatusCmd("archive", model.LedgerArchived),
		newLedgerEditCmd(),
		newLedgerRmCmd(),
		newLedgerSearchCmd(),
		newLedgerCommentCmd(),
		newLedgerHistoryCmd(),
		newLedgerSyncCmd(),
		newLedgerRowCmd(),
	)
	return cmd
}

func newLedgerAddCmd() *cobra.Command {
	var body string
	var labels, columns []string
	var anchors anchorSets
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE [BODY]",
		Short: "Create a ledger, optionally declaring its column order",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("ledger add requires a title")}
			}
			if err := validateTitle(args[0], titleHintDesc); err != nil {
				return err
			}
			var pos string
			if len(args) > 1 {
				pos = args[1]
			}
			text, err := freeText(cmd, "body", body, pos, len(args) > 1, false)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			if anchors.commits, err = resolveCommits(ctx, s.Git, anchors.commits); err != nil {
				return err
			}
			l, reused, err := c.CreateLedger(ctx, notes.LedgerSpec{
				Title:       args[0],
				Description: text,
				Columns:     columns,
				Labels:      labels,
				Anchors:     anchorSetsSpec(anchors),
			})
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, "ledger", l.ID)
			}
			return printLedger(cmd, l, jsonOut, writeAck{Reused: reused})
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "ledger description; - reads stdin")
	bindLabels(flags, &labels, "label (repeatable)")
	bindColumns(flags, &columns)
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLedgerListCmd() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List ledgers (active only unless --all)",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			ledgers, err := c.Ledgers(cmd.Context(), notes.LedgerFilter{
				IncludeArchived: all,
				Labels:          labels,
				Anchors:         anchorFiltersToNotes(filters),
			})
			if err != nil {
				return err
			}
			return printLedgerList(cmd, ledgers, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	flags.BoolVar(&all, "all", false, "include archived ledgers")
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLedgerShowCmd() *cobra.Command {
	var where []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one ledger with its rows",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			_, l, err := ledgerSpec.load(cmd.Context(), s, args[0])
			if err != nil {
				return err
			}
			match, err := parseFields(where)
			if err != nil {
				return err
			}
			l.Rows = notes.MatchRows(l, match)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newLedgerDTO(l))
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), renderLedgerShow(l))
			return err
		},
	}
	flags := cmd.Flags()
	bindWhere(flags, &where)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLedgerStatusCmd(use string, status model.LedgerStatus) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a ledger " + string(status),
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, id, err := openLedger(cmd, args[0])
			if err != nil {
				return err
			}
			var l model.Ledger
			switch status {
			case model.LedgerActive:
				l, err = c.ActivateLedger(ctx, id)
			case model.LedgerArchived:
				l, err = c.ArchiveLedger(ctx, id)
			}
			if err != nil {
				return ledgerErr(err)
			}
			return printLedger(cmd, l, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newLedgerEditCmd() *cobra.Command {
	var title, body string
	var columns []string
	var labels labelEdits
	var anchors anchorEdits
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a ledger's title, description, columns, labels, or anchors",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			var edit notes.LedgerEdit
			if flags.Changed("title") {
				if err := validateTitle(title, titleHintDesc); err != nil {
					return err
				}
				edit.Title = &title
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				edit.Description = &text
			}
			if flags.Changed("column") {
				edit.Columns = &columns
			}
			edit.AddLabels, edit.RemoveLabels = labels.add, labels.rm
			edit.AddAnchors = notes.AnchorSpec{Commits: anchors.addCommits, Paths: anchors.addPaths, Dirs: anchors.addDirs, Branches: anchors.addBranches}
			edit.RemoveAnchors = notes.AnchorSpec{Commits: anchors.rmCommits, Paths: anchors.rmPaths, Dirs: anchors.rmDirs, Branches: anchors.rmBranches}
			if ledgerEditEmpty(edit) {
				return &UsageError{Err: errors.New("ledger edit requires at least one flag")}
			}
			c, id, err := openLedger(cmd, args[0])
			if err != nil {
				return err
			}
			l, err := c.EditLedger(cmd.Context(), id, edit)
			if err != nil {
				return ledgerErr(err)
			}
			return printLedger(cmd, l, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new description; - reads stdin")
	bindColumns(flags, &columns)
	labels.bind(flags)
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLedgerRmCmd() *cobra.Command {
	return ledgerSpec.rmCmd("Tombstone a ledger", (*notes.Client).ResolveLedger, (*notes.Client).RemoveLedger)
}

func newLedgerSearchCmd() *cobra.Command {
	var labels []string
	var author string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across ledger titles, labels, descriptions, and row content",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			limitAll := limit
			if limitAll == 0 {
				limitAll = -1
			}
			ledgers, err := c.SearchLedgers(cmd.Context(), args[0], notes.SearchFilter{
				Labels:  labels,
				Author:  author,
				Anchors: anchorFiltersToNotes(filters),
				Limit:   limitAll,
			})
			if err != nil {
				return err
			}
			return printLedgerList(cmd, ledgers, jsonOut)
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

func newLedgerCommentCmd() *cobra.Command {
	var body string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "comment ID [BODY]",
		Short: "Append a comment (positional BODY, --body, or - for stdin)",
		Args:  maxArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &UsageError{Err: errors.New("ledger comment requires a ledger ID")}
			}
			var pos string
			if len(args) > 1 {
				pos = args[1]
			}
			text, err := freeText(cmd, "body", body, pos, len(args) > 1, true)
			if err != nil {
				return err
			}
			c, id, err := openLedger(cmd, args[0])
			if err != nil {
				return err
			}
			l, err := c.CommentLedger(cmd.Context(), id, text)
			if err != nil {
				return ledgerErr(err)
			}
			return printLedger(cmd, l, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "comment body; - reads stdin")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLedgerHistoryCmd() *cobra.Command { return kindHistoryCmd(model.KindLedger, "ledger") }

func newLedgerSyncCmd() *cobra.Command {
	var file string
	var prune, jsonOut bool
	cmd := &cobra.Command{
		Use:   "sync ID",
		Short: "Write a whole row set from JSON, merging fields into the rows already there",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := readRowSpecs(cmd, file)
			if err != nil {
				return err
			}
			c, id, err := openLedger(cmd, args[0])
			if err != nil {
				return err
			}
			l, result, err := c.SyncRows(cmd.Context(), id, rows, prune)
			if err != nil {
				return ledgerErr(err)
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newLedgerSyncDTO(l, result))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t+%d ~%d -%d\t%d rows\n", l.ID.Short(), result.Added, result.Updated, result.Removed, len(l.Rows))
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&file, "file", "-", "JSON row array to write; - reads stdin")
	flags.BoolVar(&prune, "prune", false, "remove every row the set omits")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newLedgerRowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "row",
		Short: "Write and read the rows of a ledger one at a time",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(newRowSetCmd(), newRowRmCmd(), newRowListCmd())
	return cmd
}

func newRowSetCmd() *cobra.Command {
	var key string
	var fields []string
	var replace, jsonOut bool
	cmd := &cobra.Command{
		Use:   "set ID --key KEY --field NAME=VALUE",
		Short: "Write fields into one row, inserting it when the ledger has none",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if key == "" {
				return &UsageError{Err: errors.New("ledger row set requires --key")}
			}
			values, err := parseFields(fields)
			if err != nil {
				return err
			}
			if len(values) == 0 {
				return &UsageError{Err: errors.New("ledger row set requires at least one --field")}
			}
			c, id, err := openLedger(cmd, args[0])
			if err != nil {
				return err
			}
			l, err := c.SetRow(cmd.Context(), id, key, values, replace)
			if err != nil {
				return ledgerErr(err)
			}
			return printLedger(cmd, l, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&key, "key", "", "row key")
	bindFields(flags, &fields)
	flags.BoolVar(&replace, "replace", false, "drop the fields --field omits instead of leaving them standing")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newRowRmCmd() *cobra.Command {
	var key string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID --key KEY",
		Short: "Remove one row",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if key == "" {
				return &UsageError{Err: errors.New("ledger row rm requires --key")}
			}
			c, id, err := openLedger(cmd, args[0])
			if err != nil {
				return err
			}
			l, err := c.RemoveRow(cmd.Context(), id, key)
			if err != nil {
				return ledgerErr(err)
			}
			return printLedger(cmd, l, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&key, "key", "", "row key")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newRowListCmd() *cobra.Command {
	var where []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list ID",
		Short: "List a ledger's rows, narrowed by --where",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			_, l, err := ledgerSpec.load(cmd.Context(), s, args[0])
			if err != nil {
				return err
			}
			match, err := parseFields(where)
			if err != nil {
				return err
			}
			rows := notes.MatchRows(l, match)
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, ledgerRowDTOs(rows))
			}
			for _, row := range rows {
				if _, err := fmt.Fprintln(out, leanRowLine(l, row)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	flags := cmd.Flags()
	bindWhere(flags, &where)
	bindJSON(flags, &jsonOut)
	return cmd
}

// openLedger resolves prefix to a ledger id, installing the hooks first, so
// every mutating ledger verb shares one preamble.
func openLedger(cmd *cobra.Command, prefix string) (*notes.Client, model.EntityID, error) {
	ctx := cmd.Context()
	s, c, err := openStoreClient(cmd)
	if err != nil {
		return nil, "", err
	}
	if err := autoInstall(ctx, cmd, s.Git); err != nil {
		return nil, "", err
	}
	id, err := c.ResolveLedger(ctx, prefix)
	if err != nil {
		return nil, "", err
	}
	return c, id, nil
}

func bindColumns(f *pflag.FlagSet, columns *[]string) {
	f.StringArrayVar(columns, "column", nil, "column name, in display order (repeatable)")
}

func bindFields(f *pflag.FlagSet, fields *[]string) {
	f.StringArrayVar(fields, "field", nil, "NAME=VALUE field to write (repeatable)")
}

func bindWhere(f *pflag.FlagSet, where *[]string) {
	f.StringArrayVar(where, "where", nil, "keep only rows whose NAME=VALUE (repeatable, ANDed)")
}

// parseFields splits NAME=VALUE pairs into a field map. A pair with no "=" or
// an empty name is a UsageError; a value may contain "=" and may be empty.
func parseFields(pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name == "" {
			return nil, &UsageError{Err: fmt.Errorf("field %q is not NAME=VALUE", pair)}
		}
		out[name] = value
	}
	return out, nil
}

// readRowSpecs decodes the JSON row array at path, or from stdin when path is
// "-". A row with an empty key is refused before anything is written.
func readRowSpecs(cmd *cobra.Command, path string) ([]notes.RowSpec, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(cmd.InOrStdin())
	} else {
		//nolint:gosec // G304: path is the operator-supplied row file for this CLI; reading it is the intended behavior.
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}
	var rows []notes.RowSpec
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, &UsageError{Err: fmt.Errorf("parse rows: %w", err)}
	}
	for _, row := range rows {
		if row.Key == "" {
			return nil, &UsageError{Err: errors.New("every row needs a non-empty key")}
		}
	}
	return rows, nil
}

// ledgerErr maps a notes-layer *ConflictError to the CLI's, matching the other
// noun groups' conflict exit codes and stderr bytes.
func ledgerErr(err error) error {
	var conflict *notes.ConflictError
	if errors.As(err, &conflict) {
		return &ConflictError{Msg: strings.TrimPrefix(conflict.Error(), "cc-notes: ")}
	}
	return err
}

// ledgerEditEmpty reports whether a ledger edit mask sets nothing, the CLI's
// "at least one flag" guard raised as a UsageError.
func ledgerEditEmpty(e notes.LedgerEdit) bool {
	return e.Title == nil && e.Description == nil && e.Columns == nil &&
		len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0 &&
		anchorSpecEmpty(e.AddAnchors) && anchorSpecEmpty(e.RemoveAnchors)
}

// printLedgerList writes ledgers as a JSON array of their summary DTOs or one
// lean line per ledger.
func printLedgerList(cmd *cobra.Command, ledgers []model.Ledger, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]ledgerSummaryDTO, len(ledgers))
		for i, l := range ledgers {
			dtos[i] = newLedgerSummaryDTO(l)
		}
		return printJSON(out, dtos)
	}
	for _, l := range ledgers {
		if _, err := fmt.Fprintln(out, leanLedgerLine(l)); err != nil {
			return err
		}
	}
	return nil
}
