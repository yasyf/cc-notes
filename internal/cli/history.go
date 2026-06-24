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
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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
	entries, err := historyEntries(steps)
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

// histEntry is one commit in an entity's trail: the commit's metadata, a
// machine kind ("create"|"edit"|"checkpoint"), a human verb for the header,
// the covered-commit count for checkpoints, and the fields it changed.
type histEntry struct {
	commit  model.PackCommit
	kind    string
	verb    string
	covers  int
	changes []histChange
}

// histChange is one field's delta: a scalar From→To, or a set of Added and
// Removed elements. scalar discriminates the two.
type histChange struct {
	field   string
	scalar  bool
	from    string
	to      string
	added   []string
	removed []string
}

func historyEntries(steps []fold.Step) ([]histEntry, error) {
	var entries []histEntry
	for i, st := range steps {
		e := histEntry{commit: st.Commit}
		switch {
		case isCheckpointCommit(st.Commit):
			e.kind = "checkpoint"
			e.covers = checkpointCovers(st.Commit)
			e.verb = fmt.Sprintf("compacted (covers %d %s)", e.covers, plural(e.covers, "commit", "commits"))
		case i == 0:
			e.kind = "create"
			e.verb = "created " + entityKind(st.Snapshot)
			changes, err := diffSnapshots(zeroLike(st.Snapshot), st.Snapshot)
			if err != nil {
				return nil, err
			}
			// A create is "from nothing": render every initial scalar as a
			// plain set, so a numeric default reads "priority: 2", not "0 → 2".
			for j := range changes {
				if changes[j].scalar {
					changes[j].from = ""
				}
			}
			e.changes = changes
		default:
			e.kind = "edit"
			changes, err := diffSnapshots(steps[i-1].Snapshot, st.Snapshot)
			if err != nil {
				return nil, err
			}
			// A commit whose only effect was on bookkeeping (a lease heartbeat)
			// or that was idempotent changes no visible field — it is not an
			// edit, so it stays out of the trail.
			if len(changes) == 0 {
				continue
			}
			e.changes = changes
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// diffSnapshots reports the fields that changed between two snapshots of the
// same entity, by marshaling each to its canonical JSON map and comparing every
// field but the bookkeeping ones. Scalars report From→To; set-valued fields
// report Added and Removed elements.
func diffSnapshots(before, after model.Snapshot) ([]histChange, error) {
	bm, err := snapshotMap(before)
	if err != nil {
		return nil, err
	}
	am, err := snapshotMap(after)
	if err != nil {
		return nil, err
	}
	var changes []histChange
	for _, field := range unionKeys(bm, am) {
		if historyHiddenFields[field] {
			continue
		}
		if ch, ok := diffField(field, bm[field], am[field]); ok {
			changes = append(changes, ch)
		}
	}
	return changes, nil
}

func diffField(field string, before, after any) (histChange, bool) {
	ba, baIsArray := before.([]any)
	aa, aaIsArray := after.([]any)
	if baIsArray || aaIsArray {
		added, removed := diffElements(field, ba, aa)
		if len(added) == 0 && len(removed) == 0 {
			return histChange{}, false
		}
		return histChange{field: field, added: added, removed: removed}, true
	}
	from := formatScalar(field, before)
	to := formatScalar(field, after)
	if from == to {
		return histChange{}, false
	}
	return histChange{field: field, scalar: true, from: from, to: to}, true
}

func diffElements(field string, before, after []any) (added, removed []string) {
	bset := elementSet(field, before)
	aset := elementSet(field, after)
	for s := range aset {
		if !bset[s] {
			added = append(added, s)
		}
	}
	for s := range bset {
		if !aset[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func elementSet(field string, elems []any) map[string]bool {
	out := make(map[string]bool, len(elems))
	for _, e := range elems {
		out[formatElement(field, e)] = true
	}
	return out
}

// historyHiddenFields are snapshot fields excluded from the audit diff:
// bookkeeping that moves on every commit, or derived content witnesses — none
// of which is a user edit.
var historyHiddenFields = map[string]bool{
	"id":                true,
	"author":            true,
	"created_at":        true,
	"updated_at":        true,
	"head":              true,
	"heartbeat_at":      true,
	"heartbeat_lamport": true,
	"witness":           true,
	"verified_commit":   true,
}

// historyTimeFields are unix-seconds scalars rendered as RFC3339 UTC.
var historyTimeFields = map[string]bool{
	"verified_at": true,
	"started_at":  true,
	"closed_at":   true,
	"stale_at":    true,
	"start_date":  true,
	"end_date":    true,
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

func formatScalar(field string, v any) string {
	if v == nil {
		return ""
	}
	if historyTimeFields[field] {
		if n, ok := v.(float64); ok {
			if n == 0 {
				return ""
			}
			return rfc3339(int64(n))
		}
	}
	return scalarString(v)
}

// formatElement renders one set element to a stable, human string: a string
// element verbatim, a known object element (anchor, comment, log entry,
// criterion) summarized, any other object as compact JSON.
func formatElement(field string, v any) string {
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
	}
	b, _ := json.Marshal(m)
	return string(b)
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

func renderHistoryText(w io.Writer, entries []histEntry) error {
	for _, e := range entries {
		header := fmt.Sprintf("%s  %s  %s", shortSHA(e.commit.SHA), e.commit.Author, rfc3339(e.commit.AuthorTime))
		if e.verb != "" {
			header += "  " + e.verb
		}
		if _, err := fmt.Fprintln(w, header); err != nil {
			return err
		}
		for _, ch := range e.changes {
			for _, line := range renderChangeLines(ch) {
				if _, err := fmt.Fprintln(w, "    "+line); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func renderChangeLines(ch histChange) []string {
	if ch.scalar {
		switch {
		case ch.from == "":
			return []string{fmt.Sprintf("%s: %s", ch.field, ch.to)}
		case ch.to == "":
			return []string{fmt.Sprintf("%s: %s → (none)", ch.field, ch.from)}
		default:
			return []string{fmt.Sprintf("%s: %s → %s", ch.field, ch.from, ch.to)}
		}
	}
	if simpleSetFields[ch.field] {
		tokens := make([]string, 0, len(ch.added)+len(ch.removed))
		for _, a := range ch.added {
			tokens = append(tokens, "+"+a)
		}
		for _, r := range ch.removed {
			tokens = append(tokens, "-"+r)
		}
		return []string{ch.field + ": " + strings.Join(tokens, " ")}
	}
	lines := make([]string, 0, len(ch.added)+len(ch.removed))
	for _, a := range ch.added {
		lines = append(lines, fmt.Sprintf("%s: +%s", ch.field, a))
	}
	for _, r := range ch.removed {
		lines = append(lines, fmt.Sprintf("%s: -%s", ch.field, r))
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

func newHistoryEntryDTO(e histEntry) historyEntryDTO {
	changes := make([]historyChangeDTO, len(e.changes))
	for i, ch := range e.changes {
		if ch.scalar {
			changes[i] = historyChangeDTO{Field: ch.field, From: optString(ch.from), To: optString(ch.to)}
		} else {
			changes[i] = historyChangeDTO{Field: ch.field, Added: ch.added, Removed: ch.removed}
		}
	}
	return historyEntryDTO{
		SHA:     string(e.commit.SHA),
		Author:  string(e.commit.Author),
		Time:    rfc3339(e.commit.AuthorTime),
		Lamport: uint64(e.commit.Pack.Lamport),
		Kind:    e.kind,
		Covers:  e.covers,
		Changes: changes,
	}
}

func snapshotMap(snap model.Snapshot) (map[string]any, error) {
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isCheckpointCommit reports whether a commit carries only Checkpoint ops — a
// compaction marker, not a user edit.
func isCheckpointCommit(c model.PackCommit) bool {
	if len(c.Pack.Ops) == 0 {
		return false
	}
	for _, op := range c.Pack.Ops {
		if _, ok := op.(model.Checkpoint); !ok {
			return false
		}
	}
	return true
}

func checkpointCovers(c model.PackCommit) int {
	n := 0
	for _, op := range c.Pack.Ops {
		if cp, ok := op.(model.Checkpoint); ok {
			n += len(cp.CoversShas)
		}
	}
	return n
}

func zeroLike(snap model.Snapshot) model.Snapshot {
	switch snap.(type) {
	case model.Note:
		return model.Note{}
	case model.Doc:
		return model.Doc{}
	case model.Log:
		return model.Log{}
	case model.Task:
		return model.Task{}
	case model.Sprint:
		return model.Sprint{}
	case model.Project:
		return model.Project{}
	default:
		panic(fmt.Sprintf("history: unknown snapshot type %T", snap))
	}
}

func entityKind(snap model.Snapshot) string {
	switch snap.(type) {
	case model.Note:
		return "note"
	case model.Doc:
		return "doc"
	case model.Log:
		return "log"
	case model.Task:
		return "task"
	case model.Sprint:
		return "sprint"
	case model.Project:
		return "project"
	default:
		panic(fmt.Sprintf("history: unknown snapshot type %T", snap))
	}
}

func shortSHA(sha model.SHA) string { return model.EntityID(sha).Short() }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
