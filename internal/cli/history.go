package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

// newHistoryCmd builds the top-level "cc-notes history ID": the edit trail of
// any entity, resolving the id across every kind. Each entry is one commit in
// linearization order with the fields it changed; it is read-only and never
// touches the remote.
func newHistoryCmd() *cobra.Command {
	return historyCmd("history ID", "Show the edit history of any note, doc, log, task, sprint, or project", resolveAnyEntity)
}

func newNoteHistoryCmd() *cobra.Command    { return kindHistoryCmd(refs.KindNote, "note") }
func newDocHistoryCmd() *cobra.Command     { return kindHistoryCmd(refs.KindDoc, "doc") }
func newLogHistoryCmd() *cobra.Command     { return kindHistoryCmd(refs.KindLog, "log") }
func newTaskHistoryCmd() *cobra.Command    { return kindHistoryCmd(refs.KindTask, "task") }
func newSprintHistoryCmd() *cobra.Command  { return kindHistoryCmd(refs.KindSprint, "sprint") }
func newProjectHistoryCmd() *cobra.Command { return kindHistoryCmd(refs.KindProject, "project") }

// kindHistoryCmd builds a noun-scoped "history ID" subcommand that resolves the
// id within a single kind, so a wrong-kind id fails cleanly rather than
// resolving to a sibling entity that happens to share the prefix.
func kindHistoryCmd(kind refs.Kind, noun string) *cobra.Command {
	resolve := func(ctx context.Context, s *store.Store, prefix string) (string, error) {
		return s.Resolve(ctx, kind, prefix)
	}
	return historyCmd("history ID", "Show this "+noun+"'s edit history", resolve)
}

// historyOpts carries the history command's output flags.
type historyOpts struct {
	jsonOut bool
	reverse bool
	limit   int
}

func historyCmd(use, short string, resolve func(context.Context, *store.Store, string) (string, error)) *cobra.Command {
	var opts historyOpts
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			ref, err := resolve(ctx, s, args[0])
			if err != nil {
				return err
			}
			steps, err := s.History(ctx, ref)
			if err != nil {
				return err
			}
			return printHistory(cmd, steps, opts)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&opts.jsonOut, "json", false, "emit JSON")
	flags.BoolVar(&opts.reverse, "reverse", false, "oldest first (chronological); default is newest first")
	flags.IntVar(&opts.limit, "limit", 0, "show at most N most recent entries (0 = all)")
	return cmd
}

// entityKinds is every prefix-resolvable entity kind, in the order the
// top-level history command probes them.
var entityKinds = []refs.Kind{refs.KindNote, refs.KindDoc, refs.KindLog, refs.KindTask, refs.KindSprint, refs.KindProject}

// resolveAnyEntity expands a kind-agnostic id prefix into a ref by resolving it
// against every kind. Ids are globally unique, so at most one kind matches a
// full id; a prefix that matches entities in more than one kind is ambiguous
// and fails with an *AmbiguousError listing each match. A prefix that is
// ambiguous within a single kind surfaces that kind's *AmbiguousError directly.
func resolveAnyEntity(ctx context.Context, s *store.Store, prefix string) (string, error) {
	matched := make([]string, 0, len(entityKinds))
	for _, kind := range entityKinds {
		ref, err := s.Resolve(ctx, kind, prefix)
		switch {
		case err == nil:
			matched = append(matched, ref)
		case errors.Is(err, store.ErrNotFound):
			continue
		default:
			return "", err
		}
	}
	switch len(matched) {
	case 0:
		return "", fmt.Errorf("%w: no entity matches %q", store.ErrNotFound, prefix)
	case 1:
		return matched[0], nil
	default:
		return "", ambiguousAcrossKinds(ctx, s, prefix, matched)
	}
}

func printHistory(cmd *cobra.Command, steps []fold.Step, opts historyOpts) error {
	entries, err := trail.Entries(steps)
	if err != nil {
		return err
	}
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
			dtos[i] = newHistoryEntryDTO(e)
		}
		return printJSON(out, dtos)
	}
	return renderHistoryText(out, entries)
}

// historyVerb renders the human header verb for one trail entry: a compaction
// marker for checkpoints, "created <kind>" for the create, and nothing for a
// plain edit.
func historyVerb(e trail.Entry) string {
	switch e.Kind {
	case "checkpoint":
		return fmt.Sprintf("compacted (covers %d %s)", e.Covers, plural(e.Covers, "commit", "commits"))
	case "create":
		return "created " + trail.EntityKind(e.Snapshot)
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

func renderHistoryText(w io.Writer, entries []trail.Entry) error {
	for _, e := range entries {
		header := fmt.Sprintf("%s  %s  %s", shortSHA(e.Commit.SHA), e.Commit.Author, rfc3339(e.Commit.AuthorTime))
		if verb := historyVerb(e); verb != "" {
			header += "  " + verb
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

func renderChangeLines(ch trail.Change) []string {
	if ch.Scalar {
		switch {
		case ch.From == "":
			return []string{fmt.Sprintf("%s: %s", ch.Field, ch.To)}
		case ch.To == "":
			return []string{fmt.Sprintf("%s: %s → (none)", ch.Field, ch.From)}
		default:
			return []string{fmt.Sprintf("%s: %s → %s", ch.Field, ch.From, ch.To)}
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
// sha, author, RFC3339 UTC time, lamport, the entry kind
// (create|edit|checkpoint), the covered-commit count for checkpoints, and the
// field changes.
type historyEntryDTO struct {
	SHA     string             `json:"sha"`
	Author  string             `json:"author"`
	Time    string             `json:"time"`
	Lamport uint64             `json:"lamport"`
	Kind    string             `json:"kind"`
	Covers  int                `json:"covers,omitempty"`
	Changes []historyChangeDTO `json:"changes"`
}

func newHistoryEntryDTO(e trail.Entry) historyEntryDTO {
	changes := make([]historyChangeDTO, len(e.Changes))
	for i, ch := range e.Changes {
		if ch.Scalar {
			changes[i] = historyChangeDTO{Field: ch.Field, From: optString(ch.From), To: optString(ch.To)}
		} else {
			changes[i] = historyChangeDTO{Field: ch.Field, Added: ch.Added, Removed: ch.Removed}
		}
	}
	return historyEntryDTO{
		SHA:     string(e.Commit.SHA),
		Author:  string(e.Commit.Author),
		Time:    rfc3339(e.Commit.AuthorTime),
		Lamport: uint64(e.Commit.Pack.Lamport),
		Kind:    e.Kind,
		Covers:  e.Covers,
		Changes: changes,
	}
}

func shortSHA(sha model.SHA) string { return model.EntityID(sha).Short() }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
