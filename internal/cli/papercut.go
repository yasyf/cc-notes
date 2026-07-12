package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// papercut identity: the tag is identity (a retitle never forks the journal),
// the title is display. A papercut is one appended entry in the repo-wide
// journal, folded and stored as an ordinary Log.
const (
	papercutTag   = "papercut"
	papercutTitle = "papercuts"
)

func newPapercutCmd() *cobra.Command {
	var modelID string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "papercut TEXT",
		Short: "File a friction complaint to the repo-wide papercut journal",
		Long: `Record a one-paragraph complaint about friction hit during work — a dead-end
tool call, a broken link, a misleading doc — instead of silently pushing through.
Each complaint appends one entry to the repo-wide papercut journal: a log titled
"papercuts", tagged "papercut", auto-created on first use.

TEXT is the complaint; - reads it from stdin. --model (or CC_NOTES_MODEL, with
the flag winning) records the model identity on the entry.

Because "papercut list" reads the journal back, filing a complaint whose text is
literally "list" needs an escape: "cc-notes papercut -- list", or pipe it via
stdin ("... | cc-notes papercut -").`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := bodyArg(cmd, args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return &UsageError{Err: errors.New("papercut text is empty — describe the friction you hit in one paragraph")}
			}
			entryModel := resolvePapercutModel(cmd, modelID)
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			journal, err := findOrCreatePapercutLog(ctx, cmd, s)
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, refs.For(model.KindLog, journal.ID), []model.Op{model.AppendEntry{Text: text, Model: entryModel}})
			if err != nil {
				return err
			}
			return printLog(cmd, s, snapshot.(model.Log), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&modelID, "model", "", "model identity to record on the entry (default: CC_NOTES_MODEL)")
	bindJSON(flags, &jsonOut)
	cmd.AddCommand(newPapercutListCmd())
	return cmd
}

func newPapercutListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every papercut complaint in timestamp order",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			logs, err := s.ListLogs(cmd.Context(), false)
			if err != nil {
				return err
			}
			rows := papercutRows(logs)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), papercutEntryDTOs(rows))
			}
			return printPapercutRows(cmd, rows)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// resolvePapercutModel resolves the entry's model identity: the --model flag
// verbatim when set, else the trimmed CC_NOTES_MODEL environment variable.
func resolvePapercutModel(cmd *cobra.Command, flag string) string {
	if cmd.Flags().Changed("model") {
		return flag
	}
	return strings.TrimSpace(os.Getenv("CC_NOTES_MODEL"))
}

// findOrCreatePapercutLog returns the canonical papercut journal, creating it
// when absent. The canonical pick is the first papercut-tagged log in ListLogs
// order (CreatedAt then id ascending) — the create-dedupe survivor order — so
// future appends deterministically converge onto the oldest twin. The
// AppendEntry is never bundled into the create pack: dedupeCovered excludes
// append_entry, so bundling would disable the same-clone convergence backstop.
func findOrCreatePapercutLog(ctx context.Context, cmd *cobra.Command, s *store.Store) (model.Log, error) {
	logs, err := s.ListLogs(ctx, false)
	if err != nil {
		return model.Log{}, err
	}
	for _, l := range logs {
		if slices.Contains(l.Tags, papercutTag) {
			return l, nil
		}
	}
	create := model.CreateLog{Nonce: model.NewNonce(), Title: papercutTitle, Tags: []string{papercutTag}}
	snapshot, err := createEntity(ctx, cmd, s, []model.Op{create})
	if err != nil {
		return model.Log{}, err
	}
	return snapshot.(model.Log), nil
}

// papercutRow pairs one folded log entry with its journal and its index within
// that journal, the tuple the unioned list orders by.
type papercutRow struct {
	log   model.Log
	entry model.LogEntry
	index int
}

// papercutRows unions the entries of every live papercut-tagged log into one
// slice ordered by entry timestamp, breaking ties by the journal's creation time
// then id, then the entry's index within its journal — so twin journals merge
// into a single deterministic chronology.
func papercutRows(logs []model.Log) []papercutRow {
	var rows []papercutRow
	for _, l := range logs {
		if !slices.Contains(l.Tags, papercutTag) {
			continue
		}
		for i, e := range l.Entries {
			rows = append(rows, papercutRow{log: l, entry: e, index: i})
		}
	}
	slices.SortFunc(rows, func(a, b papercutRow) int {
		if c := cmp.Compare(a.entry.TS, b.entry.TS); c != 0 {
			return c
		}
		if c := cmp.Compare(a.log.CreatedAt, b.log.CreatedAt); c != 0 {
			return c
		}
		if c := cmp.Compare(a.log.ID, b.log.ID); c != 0 {
			return c
		}
		return cmp.Compare(a.index, b.index)
	})
	return rows
}

// papercutEntryDTO is one papercut complaint in the list DTO: its journal id,
// the recorded model identity (null when unset), the author and RFC3339 UTC
// timestamp from the carrying commit, and the complaint text.
type papercutEntryDTO struct {
	LogID  string  `json:"log_id"`
	Model  *string `json:"model"`
	Author string  `json:"author"`
	TS     string  `json:"ts"`
	Text   string  `json:"text"`
}

// papercutEntryDTOs renders unioned rows into their DTO form, always non-nil so
// an empty journal set marshals as [] rather than null.
func papercutEntryDTOs(rows []papercutRow) []papercutEntryDTO {
	out := make([]papercutEntryDTO, len(rows))
	for i, r := range rows {
		out[i] = papercutEntryDTO{
			LogID:  string(r.log.ID),
			Model:  render.OptString(r.entry.Model),
			Author: string(r.entry.Author),
			TS:     render.RFC3339(r.entry.TS),
			Text:   r.entry.Text,
		}
	}
	return out
}

// printPapercutRows writes each complaint as a "-- <model> — <author> <ts>"
// block, dropping the "<model> — " segment when no model was recorded, in the
// block idiom renderLogShow entries and task comments share, with a blank line
// between blocks. Empty input prints nothing.
func printPapercutRows(cmd *cobra.Command, rows []papercutRow) error {
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		if r.entry.Model != "" {
			fmt.Fprintf(&b, "-- %s — %s %s\n%s\n", r.entry.Model, r.entry.Author, render.RFC3339(r.entry.TS), r.entry.Text)
		} else {
			fmt.Fprintf(&b, "-- %s %s\n%s\n", r.entry.Author, render.RFC3339(r.entry.TS), r.entry.Text)
		}
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}
