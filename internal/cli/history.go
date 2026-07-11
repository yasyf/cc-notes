package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

// newHistoryCmd builds the top-level "cc-notes history ID": the edit trail of
// any entity, resolving the id across every kind. Each entry is one commit in
// linearization order with the fields it changed; it is read-only and never
// touches the remote.
func newHistoryCmd() *cobra.Command {
	return historyCmd("history ID", "Show the edit history of any note, doc, log, task, sprint, project, or runbook", resolveAnyEntity)
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

// resolveAnyEntity expands a kind-agnostic id prefix into a ref by resolving it
// against every kind. Ids are globally unique, so at most one kind matches a
// full id; a prefix that matches entities in more than one kind is ambiguous
// and fails with an *AmbiguousError listing each match. A prefix that is
// ambiguous within a single kind surfaces that kind's *AmbiguousError directly.
func resolveAnyEntity(ctx context.Context, s *store.Store, prefix string) (string, error) {
	kinds := model.Kinds()
	matched := make([]string, 0, len(kinds))
	for _, kind := range kinds {
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
		header := fmt.Sprintf("%s  %s  %s", shortSHA(e.Commit.SHA), e.Commit.Author, render.RFC3339(e.Commit.AuthorTime))
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
		from := formatTrailScalar(ch.Field, ch.From)
		to := formatTrailScalar(ch.Field, ch.To)
		switch {
		case from == "":
			return []string{fmt.Sprintf("%s: %s", ch.Field, to)}
		case to == "":
			return []string{fmt.Sprintf("%s: %s → (none)", ch.Field, from)}
		default:
			return []string{fmt.Sprintf("%s: %s → %s", ch.Field, from, to)}
		}
	}
	added := formatTrailSet(ch.Field, ch.Added)
	removed := formatTrailSet(ch.Field, ch.Removed)
	if simpleSetFields[ch.Field] {
		tokens := make([]string, 0, len(added)+len(removed))
		for _, a := range added {
			tokens = append(tokens, "+"+a)
		}
		for _, r := range removed {
			tokens = append(tokens, "-"+r)
		}
		return []string{ch.Field + ": " + strings.Join(tokens, " ")}
	}
	lines := make([]string, 0, len(added)+len(removed))
	for _, a := range added {
		lines = append(lines, fmt.Sprintf("%s: +%s", ch.Field, a))
	}
	for _, r := range removed {
		lines = append(lines, fmt.Sprintf("%s: -%s", ch.Field, r))
	}
	return lines
}

// timeFields are unix-seconds scalars rendered as RFC3339 UTC in the trail.
var timeFields = map[string]bool{
	"verified_at": true,
	"started_at":  true,
	"closed_at":   true,
	"stale_at":    true,
	"start_date":  true,
	"end_date":    true,
}

// formatTrailScalar renders a scalar trail value to its history string: "" for a
// nil (unset) field, RFC3339 UTC for a time field, else the plain value.
func formatTrailScalar(field string, v any) string {
	if v == nil {
		return ""
	}
	if timeFields[field] {
		if n, ok := v.(float64); ok {
			if n == 0 {
				return ""
			}
			return render.RFC3339(int64(n))
		}
	}
	return scalarString(v)
}

// formatTrailElement renders one set element to a stable, human string: a string
// element verbatim, a known object element (anchor, comment, log entry,
// criterion) summarized, any other object as compact JSON.
func formatTrailElement(field string, v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return scalarString(v)
	}
	switch field {
	case "anchors":
		return fmt.Sprintf("%s:%s", scalarString(m["kind"]), scalarString(m["value"]))
	case "comments":
		return fmt.Sprintf("comment by %s: %q", scalarString(m["author"]), scalarString(m["body"]))
	case "entries":
		return fmt.Sprintf("entry by %s: %q", scalarString(m["author"]), scalarString(m["text"]))
	case "criteria":
		return fmt.Sprintf("%q [%s]", scalarString(m["text"]), scalarString(m["status"]))
	case "steps":
		return fmt.Sprintf("%q", scalarString(m["text"]))
	case "runs":
		return fmt.Sprintf("run by %s [%s]", scalarString(m["runner"]), scalarString(m["status"]))
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// formatTrailSet renders a set field's elements to sorted history strings,
// preserving the trail's former formatted-string ordering; it returns nil for an
// empty set so the JSON DTO omits it.
func formatTrailSet(field string, elems []any) []string {
	if len(elems) == 0 {
		return nil
	}
	out := make([]string, len(elems))
	for i, e := range elems {
		out[i] = formatTrailElement(field, e)
	}
	sort.Strings(out)
	return out
}

func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
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
			changes[i] = historyChangeDTO{Field: ch.Field, From: render.OptString(formatTrailScalar(ch.Field, ch.From)), To: render.OptString(formatTrailScalar(ch.Field, ch.To))}
		} else {
			changes[i] = historyChangeDTO{Field: ch.Field, Added: formatTrailSet(ch.Field, ch.Added), Removed: formatTrailSet(ch.Field, ch.Removed)}
		}
	}
	return historyEntryDTO{
		SHA:     string(e.Commit.SHA),
		Author:  string(e.Commit.Author),
		Time:    render.RFC3339(e.Commit.AuthorTime),
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
