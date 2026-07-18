package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// newHistoryCmd builds the top-level "cc-notes history ID": the edit trail of
// any entity, resolving the id across every kind. Each entry is one commit in
// linearization order with the fields it changed; it is read-only and never
// touches the remote.
func newHistoryCmd() *cobra.Command {
	resolve := func(ctx context.Context, c *notes.Client, prefix string) (model.Kind, model.EntityID, error) {
		return c.ResolveEntity(ctx, prefix)
	}
	return historyCmd("history ID", "Show the edit history of any note, doc, log, task, sprint, project, runbook, or investigation", resolve)
}

func newNoteHistoryCmd() *cobra.Command    { return kindHistoryCmd(model.KindNote, "note") }
func newDocHistoryCmd() *cobra.Command     { return kindHistoryCmd(model.KindDoc, "doc") }
func newLogHistoryCmd() *cobra.Command     { return kindHistoryCmd(model.KindLog, "log") }
func newTaskHistoryCmd() *cobra.Command    { return kindHistoryCmd(model.KindTask, "task") }
func newSprintHistoryCmd() *cobra.Command  { return kindHistoryCmd(model.KindSprint, "sprint") }
func newProjectHistoryCmd() *cobra.Command { return kindHistoryCmd(model.KindProject, "project") }

// kindHistoryCmd builds a noun-scoped "history ID" subcommand that resolves the
// id within a single kind, so a wrong-kind id fails cleanly rather than
// resolving to a sibling entity that happens to share the prefix.
func kindHistoryCmd(kind model.Kind, noun string) *cobra.Command {
	resolve := func(ctx context.Context, c *notes.Client, prefix string) (model.Kind, model.EntityID, error) {
		id, err := resolveInKind(ctx, c, kind, prefix)
		return kind, id, err
	}
	return historyCmd("history ID", "Show this "+noun+"'s edit history", resolve)
}

// resolveInKind expands an id prefix within a single kind, so a prefix that
// names an entity of another kind fails with ErrNotFound rather than resolving
// to it.
func resolveInKind(ctx context.Context, c *notes.Client, kind model.Kind, prefix string) (model.EntityID, error) {
	switch kind {
	case model.KindNote:
		return c.ResolveNote(ctx, prefix)
	case model.KindDoc:
		return c.ResolveDoc(ctx, prefix)
	case model.KindLog:
		return c.ResolveLog(ctx, prefix)
	case model.KindTask:
		return c.ResolveTask(ctx, prefix)
	case model.KindSprint:
		return c.ResolveSprint(ctx, prefix)
	case model.KindProject:
		return c.ResolveProject(ctx, prefix)
	case model.KindRunbook:
		return c.ResolveRunbook(ctx, prefix)
	case model.KindInvestigation:
		return c.ResolveInvestigation(ctx, prefix)
	default:
		panic(fmt.Sprintf("history: unknown kind %q", kind))
	}
}

// historyOpts carries the history command's output flags.
type historyOpts struct {
	jsonOut bool
	reverse bool
	limit   int
}

func historyCmd(use, short string, resolve func(context.Context, *notes.Client, string) (model.Kind, model.EntityID, error)) *cobra.Command {
	var opts historyOpts
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := openClient()
			if err != nil {
				return err
			}
			kind, id, err := resolve(ctx, c, args[0])
			if err != nil {
				return err
			}
			entries, err := c.History(ctx, id)
			if err != nil {
				return err
			}
			return printHistory(cmd, kind, entries, opts)
		},
	}
	flags := cmd.Flags()
	bindJSON(flags, &opts.jsonOut)
	flags.BoolVar(&opts.reverse, "reverse", false, "oldest first (chronological); default is newest first")
	bindLimit(flags, &opts.limit, 0)
	return cmd
}

func printHistory(cmd *cobra.Command, kind model.Kind, entries []notes.HistoryEntry, opts historyOpts) error {
	if opts.limit > 0 && opts.limit < len(entries) {
		entries = entries[len(entries)-opts.limit:]
	}
	if !opts.reverse {
		slices.Reverse(entries)
	}
	out := cmd.OutOrStdout()
	if opts.jsonOut {
		dtos := make([]historyEntryDTO, len(entries))
		for i, e := range entries {
			changes := make([]historyChangeDTO, len(e.Changes))
			for j, ch := range e.Changes {
				changes[j] = historyChangeDTO{Field: ch.Field, From: ch.From, To: ch.To, Added: ch.Added, Removed: ch.Removed}
			}
			dtos[i] = historyEntryDTO{
				SHA:     string(e.SHA),
				Author:  string(e.Author),
				Session: e.Session,
				Time:    render.RFC3339(e.Time),
				Lamport: uint64(e.Lamport),
				Kind:    e.Kind,
				Covers:  e.Covers,
				Changes: changes,
			}
		}
		return printJSON(out, dtos)
	}
	return renderHistoryText(out, kind, entries)
}

// historyVerb renders the human header verb for one history entry: a compaction
// marker for checkpoints, "created <kind>" for the create, and nothing for a
// plain edit.
func historyVerb(e notes.HistoryEntry, kind model.Kind) string {
	switch e.Kind {
	case "checkpoint":
		return fmt.Sprintf("compacted (covers %d %s)", e.Covers, plural(e.Covers, "commit", "commits"))
	case "create":
		return "created " + string(kind)
	default:
		return ""
	}
}

// simpleSetFields are string-valued sets rendered on one line; every other
// set-valued field holds objects rendered one element per line.
var simpleSetFields = map[string]bool{
	"tags":          true,
	"labels":        true,
	"blocked_by":    true,
	"commits":       true,
	"superseded_by": true,
}

func renderHistoryText(w io.Writer, kind model.Kind, entries []notes.HistoryEntry) error {
	for _, e := range entries {
		header := fmt.Sprintf("%s  %s  %s", shortSHA(e.SHA), e.Author, render.RFC3339(e.Time))
		if verb := historyVerb(e, kind); verb != "" {
			header += "  " + verb
		}
		if e.Session != "" {
			header += "  session:" + shortSession(e.Session)
		}
		if _, err := fmt.Fprintln(w, header); err != nil {
			return err
		}
		for _, ch := range e.Changes {
			for _, line := range renderChangeLines(ch) {
				if _, err := fmt.Fprintln(w, "    "+line); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func renderChangeLines(ch notes.FieldChange) []string {
	if ch.Added == nil && ch.Removed == nil {
		from := derefString(ch.From)
		to := derefString(ch.To)
		switch {
		case from == "":
			return []string{fmt.Sprintf("%s: %s", ch.Field, to)}
		case to == "":
			return []string{fmt.Sprintf("%s: %s → (none)", ch.Field, from)}
		default:
			return []string{fmt.Sprintf("%s: %s → %s", ch.Field, from, to)}
		}
	}
	if simpleSetFields[ch.Field] {
		tokens := make([]string, 0, len(ch.Added)+len(ch.Removed))
		for _, a := range ch.Added {
			tokens = append(tokens, "+"+a)
		}
		for _, r := range ch.Removed {
			tokens = append(tokens, "-"+r)
		}
		return []string{ch.Field + ": " + strings.Join(tokens, " ")}
	}
	lines := make([]string, 0, len(ch.Added)+len(ch.Removed))
	for _, a := range ch.Added {
		lines = append(lines, fmt.Sprintf("%s: +%s", ch.Field, a))
	}
	for _, r := range ch.Removed {
		lines = append(lines, fmt.Sprintf("%s: -%s", ch.Field, r))
	}
	return lines
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// historyChangeDTO is one field delta in JSON: a scalar carries from/to (null
// when the field was unset on that side); a set carries added/removed.
type historyChangeDTO struct {
	Field   string   `json:"field"`
	From    *string  `json:"from,omitempty"`
	To      *string  `json:"to,omitempty"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// historyEntryDTO fixes the JSON field order for one history entry: the commit
// sha, author, Claude session, RFC3339 UTC time, lamport, the entry kind
// (create|edit|checkpoint), the covered-commit count for checkpoints, and the
// field changes.
type historyEntryDTO struct {
	SHA     string             `json:"sha"`
	Author  string             `json:"author"`
	Session string             `json:"session,omitempty"`
	Time    string             `json:"time"`
	Lamport uint64             `json:"lamport"`
	Kind    string             `json:"kind"`
	Covers  int                `json:"covers,omitempty"`
	Changes []historyChangeDTO `json:"changes"`
}

func shortSHA(sha model.SHA) string { return model.EntityID(sha).Short() }

func shortSession(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
