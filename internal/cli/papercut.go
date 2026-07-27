package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// papercutTitle is display only; notes.PapercutTag is the journal's identity. A
// papercut is one appended entry in the repo-wide journal, folded and stored as
// an ordinary Log.
const papercutTitle = "papercuts"

func newPapercutCmd() *cobra.Command {
	var modelID, body string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "papercut [TEXT]",
		Short: "File a friction complaint to the repo-wide papercut journal",
		Long: `Record a one-paragraph complaint about friction hit during work — a dead-end
tool call, a broken link, a misleading doc — instead of silently pushing through.
Each complaint appends one entry to the repo-wide papercut journal: a log titled
"papercuts", tagged "papercut", auto-created on first use.

The complaint text comes from the TEXT positional, --body, or - for stdin (exactly
one). --model (or CC_NOTES_MODEL, with the flag winning) records the model
identity on the entry.

Because "papercut list" reads the journal back, filing a complaint whose text is
literally "list" needs an escape: "cc-notes papercut -- list", or pipe it via
stdin ("... | cc-notes papercut -").`,
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			posGiven := len(args) > 0
			var pos string
			if posGiven {
				pos = args[0]
			}
			text, err := freeText(cmd, "body", body, pos, posGiven, true)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return &UsageError{Err: errors.New("papercut text is empty — describe the friction you hit in one paragraph")}
			}
			entryModel := resolvePapercutModel(cmd, modelID)
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			journal, err := findOrCreatePapercutLog(ctx, cmd, c)
			if err != nil {
				return err
			}
			log, err := c.AppendLog(ctx, journal.ID, notes.LogAppend{Text: text, Model: entryModel})
			if err != nil {
				return err
			}
			return printLog(cmd, c, log, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&modelID, "model", "", "model identity to record on the entry (default: CC_NOTES_MODEL)")
	bindBody(flags, &body, "the complaint; - reads stdin")
	bindJSON(flags, &jsonOut)
	cmd.AddCommand(newPapercutListCmd(), newPapercutShowCmd())
	return cmd
}

func newPapercutListCmd() *cobra.Command {
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the most recent papercut complaints, newest first",
		Long: `List the journal newest-first, capped at --limit and with each complaint clipped
to a preview. The clip marker names the "papercut show LOG_ID INDEX" call that
reads that one complaint back in full.`,
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			logs, err := c.Logs(cmd.Context(), notes.LogFilter{})
			if err != nil {
				return err
			}
			rows := papercutRecent(papercutRows(logs), limit)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), papercutEntryDTOs(rows, false))
			}
			return printPapercutRows(cmd, rows, false)
		},
	}
	flags := cmd.Flags()
	bindLimit(flags, &limit, 20)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newPapercutShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show LOG_ID INDEX",
		Short: "Show one papercut complaint with its full untruncated text",
		Long: `Read back the complaint a "papercut list" row previews, addressed by that row's
log_id and index. The index is the entry's position within its own journal, so
the address holds whatever --limit or ordering the listing used.`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			index, err := strconv.Atoi(args[1])
			if err != nil {
				return &UsageError{Err: fmt.Errorf("papercut index %q is not a number — pass the index a papercut list row carries", args[1])}
			}
			ctx := cmd.Context()
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			id, err := c.ResolveLog(ctx, args[0])
			if err != nil {
				return err
			}
			journal, err := c.Log(ctx, id)
			if err != nil {
				return err
			}
			row, err := papercutEntry(journal, index)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newPapercutEntryDTO(row, true))
			}
			return printPapercutRows(cmd, []papercutRow{row}, true)
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
// when absent. The canonical pick is the papercut-tagged log with the lowest
// (created_at, id) — the create-dedupe survivor order, the oldest twin — so
// future appends deterministically converge onto it even when a cross-clone twin
// journal exists. CreateLog never bundles the first entry into the create pack:
// dedupeCovered excludes append_entry, so bundling would disable the same-clone
// convergence backstop.
func findOrCreatePapercutLog(ctx context.Context, cmd *cobra.Command, c *notes.Client) (model.Log, error) {
	logs, err := c.Logs(ctx, notes.LogFilter{Labels: []string{notes.PapercutTag}})
	if err != nil {
		return model.Log{}, err
	}
	if len(logs) > 0 {
		canonical := logs[0]
		for _, l := range logs[1:] {
			if l.CreatedAt < canonical.CreatedAt || (l.CreatedAt == canonical.CreatedAt && l.ID < canonical.ID) {
				canonical = l
			}
		}
		return canonical, nil
	}
	log, reused, err := c.CreateLog(ctx, notes.LogSpec{Title: papercutTitle, Tags: []string{notes.PapercutTag}})
	if err != nil {
		return model.Log{}, err
	}
	if reused {
		warnDuplicate(cmd, "log", log.ID)
	}
	return log, nil
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
		if !slices.Contains(l.Tags, notes.PapercutTag) {
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

// papercutRecent takes the limit newest rows off the tail of the chronology (0 =
// all) and flips them newest-first, so a capped listing surfaces the freshest
// friction instead of burying it under the oldest.
func papercutRecent(rows []papercutRow, limit int) []papercutRow {
	if limit > 0 && limit < len(rows) {
		rows = rows[len(rows)-limit:]
	}
	slices.Reverse(rows)
	return rows
}

// papercutEntry addresses one complaint by its index within journal. A log
// outside the journal set and an index no entry occupies are both caller
// mistakes the listing's log_id and index pair prevents.
func papercutEntry(journal model.Log, index int) (papercutRow, error) {
	if !slices.Contains(journal.Tags, notes.PapercutTag) {
		return papercutRow{}, &UsageError{Err: fmt.Errorf("log %s is not a papercut journal — it carries no %q tag", journal.ID.Short(), notes.PapercutTag)}
	}
	if index < 0 || index >= len(journal.Entries) {
		return papercutRow{}, &UsageError{Err: fmt.Errorf("papercut index %d is out of range — journal %s holds %d complaint(s), indexed from 0", index, journal.ID.Short(), len(journal.Entries))}
	}
	return papercutRow{log: journal, entry: journal.Entries[index], index: index}, nil
}

// papercutText renders the complaint as a listing carries it — clipped, the
// in-band marker naming the exact "papercut show" call that recovers it — or
// verbatim when full.
func papercutText(r papercutRow, full bool) string {
	if full {
		return r.entry.Text
	}
	return clipHistoryString(r.entry.Text, fmt.Sprintf("papercut show %s %d", r.log.ID.Short(), r.index))
}

// papercutEntryDTO is one papercut complaint in the list DTO: the journal id and
// within-journal index that address it, the recorded model identity (null when
// unset), the author and RFC3339 UTC timestamp from the carrying commit, and the
// complaint text.
type papercutEntryDTO struct {
	LogID  string  `json:"log_id"`
	Index  int     `json:"index"`
	Model  *string `json:"model"`
	Author string  `json:"author"`
	TS     string  `json:"ts"`
	Text   string  `json:"text"`
}

// newPapercutEntryDTO renders one row, clipping its text unless full.
func newPapercutEntryDTO(r papercutRow, full bool) papercutEntryDTO {
	return papercutEntryDTO{
		LogID:  string(r.log.ID),
		Index:  r.index,
		Model:  render.OptString(r.entry.Model),
		Author: string(r.entry.Author),
		TS:     render.RFC3339(r.entry.TS),
		Text:   papercutText(r, full),
	}
}

// papercutEntryDTOs renders unioned rows into their DTO form, always non-nil so
// an empty journal set marshals as [] rather than null.
func papercutEntryDTOs(rows []papercutRow, full bool) []papercutEntryDTO {
	out := make([]papercutEntryDTO, len(rows))
	for i, r := range rows {
		out[i] = newPapercutEntryDTO(r, full)
	}
	return out
}

// printPapercutRows writes each complaint as a "-- <model> — <author> <ts>"
// block, dropping the "<model> — " segment when no model was recorded, in the
// block idiom renderLogShow entries and task comments share, with a blank line
// between blocks and the text clipped unless full. Empty input prints nothing.
func printPapercutRows(cmd *cobra.Command, rows []papercutRow, full bool) error {
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		text := papercutText(r, full)
		if r.entry.Model != "" {
			fmt.Fprintf(&b, "-- %s — %s %s\n%s\n", r.entry.Model, r.entry.Author, render.RFC3339(r.entry.TS), text)
		} else {
			fmt.Fprintf(&b, "-- %s %s\n%s\n", r.entry.Author, render.RFC3339(r.entry.TS), text)
		}
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}
