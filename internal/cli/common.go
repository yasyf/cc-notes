package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
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
	case len(report.Added) == 0 && len(report.Removed) == 0:
		return nil
	}
	stderr := cmd.ErrOrStderr()
	if len(report.Added) > 0 {
		if _, err := fmt.Fprintf(stderr, "cc-notes: installed refspecs in .git/config for %q: %s\n",
			defaultRemote, strings.Join(report.Added, "; ")); err != nil {
			return err
		}
	}
	if len(report.Removed) > 0 {
		if _, err := fmt.Fprintf(stderr, "cc-notes: removed pre-fix fetch refspecs a plain \"git fetch --prune\" would use to delete unsynced refs: %s\n",
			strings.Join(report.Removed, "; ")); err != nil {
			return err
		}
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

// attachOps ingests every --attach file into the local LFS store — fully
// offline; content reaches the remote only at sync — and returns one
// AddAttachment op per file. A duplicate base name within one invocation, a
// missing file, an empty file, or an invalid name is a UsageError. The one
// time ingestion installs the prune guard it announces the config line on
// stderr, mirroring autoInstall.
func attachOps(ctx context.Context, cmd *cobra.Command, s *store.Store, paths []string) ([]model.Op, error) {
	seen := make(map[string]bool, len(paths))
	ops := make([]model.Op, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		if seen[name] {
			return nil, &UsageError{Err: fmt.Errorf("--attach %s: duplicate attachment name %q in one invocation", path, name)}
		}
		seen[name] = true
		att, guarded, err := s.AttachFile(ctx, path)
		if errors.Is(err, model.ErrInvalidValue) || errors.Is(err, os.ErrNotExist) {
			return nil, &UsageError{Err: err}
		}
		if err != nil {
			return nil, err
		}
		if guarded {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: installed %s in .git/config (makes `git lfs prune` keep objects it cannot verify on the remote)\n",
				strings.Join(store.PruneGuardConfigs[:], " and ")); err != nil {
				return nil, err
			}
		}
		ops = append(ops, model.AddAttachment(att))
	}
	return ops, nil
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
			return &UsageError{Err: fmt.Errorf("attachment %q already exists; pass --replace to overwrite it", name)}
		}
	}
	return nil
}

// entityAttachments renders an entity's attachments with their local
// presence, always non-nil so JSON serializes an empty list rather than
// null. The LFS store is opened only when there is something to probe, so
// attachment-less output paths cost nothing.
func entityAttachments(ctx context.Context, s *store.Store, atts []model.Attachment) ([]attachmentDTO, error) {
	out := make([]attachmentDTO, 0, len(atts))
	if len(atts) == 0 {
		return out, nil
	}
	content, err := s.LFS(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range atts {
		out = append(out, attachmentDTO{Name: a.Name, OID: a.OID, Size: a.Size, Present: content.Has(a.OID)})
	}
	return out, nil
}

// readScript reads the validation script file at path and returns its
// contents verbatim. The contents become a criterion's check command, run only
// by task validate.
func readScript(path string) (string, error) {
	//nolint:gosec // G304: path is the operator-supplied validation-script file for this CLI; reading it is the intended behavior.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read script %s: %w", path, err)
	}
	return string(data), nil
}

// maxTitleBytes caps a title in bytes, mirroring maxAttachmentNameBytes.
const maxTitleBytes = 256

// Escape hints name, per command surface, the places that actually hold long
// content: note/doc add and edit both carry it in --body/--checkout/--attach,
// logs carry it in entries, task/sprint/project in --desc, and a checked-out
// file-mode buffer carries it in the body below the frontmatter (bufferHint
// serves both the title cap and errEmptyDocBody there).
const (
	titleHintBody     = "put the content in --body (- reads stdin), --checkout file mode, or --attach"
	titleHintBodyEdit = "put the content in --body (- reads stdin), --checkout file mode, or --attach"
	titleHintLog      = "put the content in log entries (--entry on add, or log append)"
	titleHintDesc     = "put the content in --desc"
	bufferHint        = "put the content in the body below the frontmatter"
	docBodyHintAdd    = "pass --body (- reads stdin), --checkout file mode, or --attach the content"
	docBodyHintEdit   = "pass --body (- reads stdin), --checkout file mode, or --attach the content"
)

// validateTitle rejects an empty or over-long title as a UsageError, run before
// openStore/autoInstall so a rejected create or rename mutates nothing. hint names
// the flags on the calling command that hold the long content a title should not.
func validateTitle(title, hint string) error {
	switch {
	case title == "":
		return &UsageError{Err: errors.New("title is empty — a title is a short handle for the entity; give it a few descriptive words")}
	case len(title) > maxTitleBytes:
		return &UsageError{Err: fmt.Errorf("title is %d bytes (max %d) — a title is a short handle; %s", len(title), maxTitleBytes, hint)}
	default:
		return nil
	}
}

// errEmptyDocBody is the shared UsageError for a doc created or edited to carry
// no body; hint names where the content goes on the rejecting surface.
func errEmptyDocBody(hint string) error {
	return &UsageError{Err: fmt.Errorf("doc body is empty — a doc is its body; %s", hint)}
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

// maxArgs is cobra.MaximumNArgs returning a UsageError, so arity mistakes
// exit 2 (cobra.MaximumNArgs would regress them to exit 1).
func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= n {
			return nil
		}
		return &UsageError{Err: fmt.Errorf("%s accepts at most %d arg(s), received %d", cmd.CommandPath(), n, len(args))}
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

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

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

func parseSprintStatus(value string) (model.SprintStatus, error) {
	switch s := model.SprintStatus(value); s {
	case model.SprintPlanned, model.SprintActive, model.SprintCompleted, model.SprintCancelled:
		return s, nil
	default:
		return "", fmt.Errorf("invalid sprint status %q (planned|active|completed|cancelled)", value)
	}
}

func parseProjectStatus(value string) (model.ProjectStatus, error) {
	switch s := model.ProjectStatus(value); s {
	case model.ProjectActive, model.ProjectCompleted, model.ProjectArchived, model.ProjectCancelled:
		return s, nil
	default:
		return "", fmt.Errorf("invalid project status %q (active|completed|archived|cancelled)", value)
	}
}

func validatePriority(p int) (model.Priority, error) {
	if p < 0 || p > 3 {
		return 0, fmt.Errorf("invalid priority %d (0-3)", p)
	}
	return model.Priority(p), nil
}

// parseDate parses a YYYY-MM-DD calendar date as UTC midnight into unix
// seconds. An empty value is the caller's signal to clear the date and is
// handled before calling this.
func parseDate(value string) (int64, error) {
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return 0, fmt.Errorf("invalid date %q (want YYYY-MM-DD): %w", value, err)
	}
	return t.UTC().Unix(), nil
}

// resolveCriterion expands a criterion id prefix — matched case-insensitively —
// against a task's criteria. No match fails with ErrNotFound; several matches
// fail with an error listing each candidate's short id and text; one match
// returns the criterion.
func resolveCriterion(task model.Task, prefix string) (model.Criterion, error) {
	lowered := strings.ToLower(prefix)
	var matches []model.Criterion
	for _, c := range task.Criteria {
		if strings.HasPrefix(strings.ToLower(c.ID), lowered) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return model.Criterion{}, fmt.Errorf("%w: no criterion matches %q", store.ErrNotFound, prefix)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for i, c := range matches {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s %s", c.ID[:7], c.Text)
		}
		return model.Criterion{}, fmt.Errorf("%w: criterion prefix %q matches %d: %s", store.ErrAmbiguous, prefix, len(matches), b.String())
	}
}

// loadNote resolves a note id prefix and folds its chain.
func loadNote(ctx context.Context, s *store.Store, prefix string) (string, model.Note, error) {
	ref, err := s.Resolve(ctx, refs.KindNote, prefix)
	if err != nil {
		return "", model.Note{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Note{}, err
	}
	return ref, snapshot.(model.Note), nil
}

// loadDoc resolves a doc id prefix and folds its chain.
func loadDoc(ctx context.Context, s *store.Store, prefix string) (string, model.Doc, error) {
	ref, err := s.Resolve(ctx, refs.KindDoc, prefix)
	if err != nil {
		return "", model.Doc{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Doc{}, err
	}
	return ref, snapshot.(model.Doc), nil
}

// loadLog resolves a log id prefix and folds its chain.
func loadLog(ctx context.Context, s *store.Store, prefix string) (string, model.Log, error) {
	ref, err := s.Resolve(ctx, refs.KindLog, prefix)
	if err != nil {
		return "", model.Log{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Log{}, err
	}
	return ref, snapshot.(model.Log), nil
}

// loadTask resolves a task id prefix globally and folds its chain.
func loadTask(ctx context.Context, s *store.Store, prefix string) (string, model.Task, error) {
	ref, err := s.Resolve(ctx, refs.KindTask, prefix)
	if err != nil {
		return "", model.Task{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Task{}, err
	}
	return ref, snapshot.(model.Task), nil
}

// loadSprint resolves a sprint id prefix and folds its chain.
func loadSprint(ctx context.Context, s *store.Store, prefix string) (string, model.Sprint, error) {
	ref, err := s.Resolve(ctx, refs.KindSprint, prefix)
	if err != nil {
		return "", model.Sprint{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Sprint{}, err
	}
	return ref, snapshot.(model.Sprint), nil
}

// loadProject resolves a project id prefix and folds its chain.
func loadProject(ctx context.Context, s *store.Store, prefix string) (string, model.Project, error) {
	ref, err := s.Resolve(ctx, refs.KindProject, prefix)
	if err != nil {
		return "", model.Project{}, err
	}
	snapshot, err := s.Load(ctx, ref)
	if err != nil {
		return "", model.Project{}, err
	}
	return ref, snapshot.(model.Project), nil
}

// allTasks folds every task in the repository keyed by entity id. It backs
// the global blocker lookups and the derived blocks index.
func allTasks(ctx context.Context, s *store.Store) (map[model.EntityID]model.Task, error) {
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[model.EntityID]model.Task, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return m, nil
}

// resolveBlocker expands a task id prefix against every task in the
// repository. It returns the task map so callers can reuse it for cycle
// checks.
func resolveBlocker(ctx context.Context, s *store.Store, prefix string) (model.EntityID, map[model.EntityID]model.Task, error) {
	live, err := allTasks(ctx, s)
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

// sortDocs orders docs by updated_at descending, then id ascending.
func sortDocs(docs []model.Doc) {
	slices.SortFunc(docs, func(a, b model.Doc) int {
		if c := cmp.Compare(b.UpdatedAt, a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// sortLogs orders logs by updated_at descending, then id ascending.
func sortLogs(logs []model.Log) {
	slices.SortFunc(logs, func(a, b model.Log) int {
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

// warnDuplicate reports on stderr that Create's best-effort duplicate guard
// reused an existing entity of kind (identified by its short id) instead of
// writing a twin. The caller still emits the reused entity on stdout.
func warnDuplicate(cmd *cobra.Command, kind string, id model.EntityID) {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: exact duplicate of %s %s; reusing the existing %s (nothing created)\n", kind, id.Short(), kind)
}

// printNote writes n as its JSON DTO or its lean line. A mutation echo carries
// no drift verdict.
func printNote(cmd *cobra.Command, s *store.Store, n model.Note, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanNoteLine(n))
		return err
	}
	atts, err := entityAttachments(cmd.Context(), s, n.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newNoteDTO(n, "", atts))
}

// printDoc writes d as its JSON DTO carrying the drift verdict, or its lean
// line. A mutation echo passes an empty drift.
func printDoc(cmd *cobra.Command, s *store.Store, d model.Doc, drift string, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanDocLine(d))
		return err
	}
	atts, err := entityAttachments(cmd.Context(), s, d.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newDocDTO(d, drift, atts))
}

// printLog writes l as its JSON DTO or its lean line.
func printLog(cmd *cobra.Command, s *store.Store, l model.Log, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanLogLine(l))
		return err
	}
	atts, err := entityAttachments(cmd.Context(), s, l.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newLogDTO(l, atts))
}

// printTask writes t as its JSON DTO — with the derived blocks index — or
// its lean line.
func printTask(cmd *cobra.Command, s *store.Store, t model.Task, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanTaskLine(t))
		return err
	}
	live, err := allTasks(cmd.Context(), s)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newTaskDTO(t, blocksFor(live, t.ID)))
}

// printSprint writes sprint as its JSON DTO — carrying the reverse-index ids of
// its tasks — or its lean line.
func printSprint(cmd *cobra.Command, s *store.Store, sprint model.Sprint, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanSprintLine(sprint))
		return err
	}
	tasks, err := s.ListTasks(cmd.Context())
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newSprintDTO(sprint, tasksInSprint(tasks, sprint.ID)))
}

// printProject writes project as its JSON DTO — carrying the reverse-index ids
// of its sprints and tasks — or its lean line.
func printProject(cmd *cobra.Command, s *store.Store, project model.Project, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanProjectLine(project))
		return err
	}
	ctx := cmd.Context()
	sprints, err := s.ListSprints(ctx)
	if err != nil {
		return err
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newProjectDTO(project, sprintsInProject(sprints, project.ID), tasksInProject(tasks, sprints, project.ID)))
}
