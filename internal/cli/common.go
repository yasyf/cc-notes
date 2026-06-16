package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
)

// defaultRemote is the remote every mutating command best-effort wires
// before writing.
const defaultRemote = "origin"

// openStore opens the store for the repository containing the working
// directory.
func openStore() (*store.Store, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	return store.Open(dir)
}

// resolveBranch returns value verbatim when set — a name git's
// check-ref-format refuses is a UsageError before anything is written —
// otherwise the branch HEAD points at. A detached HEAD is an error telling
// the caller to pass the named flag (e.g. "branch" or "into").
func resolveBranch(ctx context.Context, s *store.Store, flag, value string) (model.Branch, error) {
	if value != "" {
		if err := s.Git.CheckRefFormat(ctx, value); err != nil {
			return "", &UsageError{Err: err}
		}
		return model.Branch(value), nil
	}
	branch, err := s.Git.HeadBranch(ctx)
	if errors.Is(err, gitcmd.ErrDetachedHead) {
		return "", fmt.Errorf("detached HEAD; pass --%s", flag)
	}
	return branch, err
}

// autoInstall best-effort wires the default remote's refspecs before a
// write: a repository without the remote is left alone, any other failure
// is loud. Config lines it actually added are announced once on stderr —
// including the push.default override when the HEAD push refspec is new —
// so the silent first mutating command never changes git push behavior
// invisibly.
func autoInstall(ctx context.Context, cmd *cobra.Command, g gitcmd.Git) error {
	report, err := ccsync.Install(ctx, g, defaultRemote)
	switch {
	case errors.Is(err, ccsync.ErrRemoteNotFound):
		return nil
	case err != nil:
		return err
	case len(report.Added) == 0:
		return nil
	}
	stderr := cmd.ErrOrStderr()
	if _, err := fmt.Fprintf(stderr, "cc-notes: installed refspecs in .git/config for %q: %s\n",
		defaultRemote, strings.Join(report.Added, "; ")); err != nil {
		return err
	}
	if report.HeadPushAdded {
		if _, err := fmt.Fprintf(stderr, "cc-notes: note: \"git push\" now pushes the current branch to its same-named remote branch (remote.%s.push overrides push.default)\n",
			defaultRemote); err != nil {
			return err
		}
	}
	return nil
}

// bodyArg returns value, or the command's stdin (trailing newlines trimmed)
// when value is "-".
func bodyArg(cmd *cobra.Command, value string) (string, error) {
	if value != "-" {
		return value, nil
	}
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// exactArgs is cobra.ExactArgs returning a UsageError, so arity mistakes
// exit 2.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == n {
			return nil
		}
		return &UsageError{Err: fmt.Errorf("%s accepts %d arg(s), received %d", cmd.CommandPath(), n, len(args))}
	}
}

// noUnknownSubcommand rejects positional arguments on a command group with
// a UsageError, so unknown subcommands exit 2.
func noUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return &UsageError{Err: fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())}
}

// runHelp makes a command group runnable so cobra validates its args —
// rejecting unknown subcommands via noUnknownSubcommand — instead of
// short-circuiting to help; a bare group invocation still prints help.
func runHelp(cmd *cobra.Command, _ []string) error { return cmd.Help() }

func parseStatus(value string) (model.Status, error) {
	switch s := model.Status(value); s {
	case model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled:
		return s, nil
	default:
		return "", fmt.Errorf("invalid status %q (open|in_progress|done|cancelled)", value)
	}
}

func parseTaskType(value string) (model.TaskType, error) {
	switch t := model.TaskType(value); t {
	case model.TypeTask, model.TypeBug, model.TypeEpic, model.TypeQuestion:
		return t, nil
	default:
		return "", fmt.Errorf("invalid type %q (task|bug|epic|question)", value)
	}
}

func validatePriority(p int) (model.Priority, error) {
	if p < 0 || p > 3 {
		return 0, fmt.Errorf("invalid priority %d (0-3)", p)
	}
	return model.Priority(p), nil
}

// loadNote resolves a note id prefix and folds its chain.
func loadNote(ctx context.Context, s *store.Store, prefix string) (string, model.Note, error) {
	ref, err := s.Resolve(ctx, refs.KindNote, "", prefix)
	if err != nil {
		return "", model.Note{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Note{}, err
	}
	return ref, snapshot.(model.Note), nil
}

// loadTask resolves a task id prefix within branch's namespace and folds
// its chain.
func loadTask(ctx context.Context, s *store.Store, branch model.Branch, prefix string) (string, model.Task, error) {
	ref, err := s.Resolve(ctx, refs.KindTask, branch, prefix)
	if err != nil {
		return "", model.Task{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Task{}, err
	}
	return ref, snapshot.(model.Task), nil
}

// liveTasks folds every task ref in the repository and returns the live
// ones — folded branch equals ref branch — keyed by entity id. It backs the
// global blocker lookups and the derived blocks index.
func liveTasks(ctx context.Context, s *store.Store) (map[model.EntityID]model.Task, error) {
	tips, err := s.Repo.ListPrefix(ctx, refs.TasksRoot)
	if err != nil {
		return nil, err
	}
	tasks := make(map[model.EntityID]model.Task, len(tips))
	for _, name := range slices.Sorted(maps.Keys(tips)) {
		parsed, err := refs.Parse(name)
		if err != nil {
			return nil, fmt.Errorf("task ref: %w", err)
		}
		chain, err := s.Repo.ReadChain(ctx, tips[name])
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		task, err := fold.Task(chain)
		if err != nil {
			return nil, fmt.Errorf("fold %s: %w", name, err)
		}
		if task.Branch == parsed.Branch {
			tasks[task.ID] = task
		}
	}
	return tasks, nil
}

// resolveBlocker expands a task id prefix against every live task in the
// repository, across all branch namespaces. It returns the live task map so
// callers can reuse it for cycle checks.
func resolveBlocker(ctx context.Context, s *store.Store, prefix string) (model.EntityID, map[model.EntityID]model.Task, error) {
	live, err := liveTasks(ctx, s)
	if err != nil {
		return "", nil, err
	}
	lowered := strings.ToLower(prefix)
	var matches []model.EntityID
	for id := range live {
		if strings.HasPrefix(string(id), lowered) {
			matches = append(matches, id)
		}
	}
	slices.Sort(matches)
	switch len(matches) {
	case 0:
		return "", nil, fmt.Errorf("%w: no task matches %q", store.ErrNotFound, prefix)
	case 1:
		return matches[0], live, nil
	default:
		candidates := make([]store.Candidate, len(matches))
		for i, id := range matches {
			candidates[i] = store.Candidate{ID: id, Title: live[id].Title}
		}
		return "", nil, &store.AmbiguousError{Kind: refs.KindTask, Prefix: prefix, Candidates: candidates}
	}
}

// blocksFor derives the reverse dependency index: the ids of live tasks
// whose blocked_by contains id, sorted.
func blocksFor(live map[model.EntityID]model.Task, id model.EntityID) []model.EntityID {
	var blocks []model.EntityID
	for _, t := range live {
		if slices.Contains(t.BlockedBy, id) {
			blocks = append(blocks, t.ID)
		}
	}
	slices.Sort(blocks)
	return blocks
}

// hasPath reports whether target is reachable from start (inclusive)
// through the blocked_by closure over live tasks.
func hasPath(live map[model.EntityID]model.Task, start, target model.EntityID) bool {
	seen := map[model.EntityID]bool{}
	stack := []model.EntityID{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		stack = append(stack, live[id].BlockedBy...)
	}
	return false
}

// sortNotes orders notes by updated_at descending, then id ascending.
func sortNotes(notes []model.Note) {
	slices.SortFunc(notes, func(a, b model.Note) int {
		if c := cmp.Compare(b.UpdatedAt, a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// sortTasks orders tasks by priority ascending, then created_at ascending,
// then id ascending.
func sortTasks(tasks []model.Task) {
	slices.SortFunc(tasks, func(a, b model.Task) int {
		if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
			return c
		}
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// hasAll reports whether have contains every element of want.
func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// printNote writes n as its JSON DTO or its lean line.
func printNote(cmd *cobra.Command, n model.Note, jsonOut bool) error {
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newNoteDTO(n))
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), leanNoteLine(n))
	return err
}

// printTask writes t as its JSON DTO — with the derived blocks index — or
// its lean line.
func printTask(cmd *cobra.Command, s *store.Store, t model.Task, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanTaskLine(t))
		return err
	}
	live, err := liveTasks(cmd.Context(), s)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newTaskDTO(t, blocksFor(live, t.ID)))
}
