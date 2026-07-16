package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// newShowCmd builds the top-level "cc-notes show ID": show any entity, resolving
// the id across every kind and dispatching to that kind's renderer. Like
// history, compact, and blame, it is global because an id-addressed read whose
// kind is inferable from the resolved ref needs no noun — the noun-scoped
// "<kind> show" commands remain for a kind-checked lookup.
func newShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show any note, doc, log, task, sprint, project, or runbook by id",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			kind, entityID, err := c.ResolveEntity(ctx, args[0])
			if err != nil {
				return err
			}
			id := string(entityID)
			switch kind {
			case model.KindNote:
				return showNote(cmd, s, id, jsonOut)
			case model.KindDoc:
				return showDoc(cmd, s, id, jsonOut)
			case model.KindLog:
				return showLog(cmd, s, id, jsonOut)
			case model.KindTask:
				return showTask(cmd, s, id, jsonOut)
			case model.KindSprint:
				return showSprint(cmd, s, id, jsonOut)
			case model.KindProject:
				return showProject(cmd, s, id, jsonOut)
			case model.KindRunbook:
				return showRunbook(cmd, s, id, jsonOut)
			default:
				panic(fmt.Sprintf("ResolveEntity returned unknown kind %q", kind))
			}
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func showNote(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, note, err := noteSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	head, err := resolveHead(ctx, s)
	if err != nil {
		return err
	}
	staleAfter, err := noteStaleAfter(ctx, s.Git)
	if err != nil {
		return err
	}
	verdict, err := noteVerdict(ctx, s, head, note, time.Now(), staleAfter, false)
	if err != nil {
		return err
	}
	supersedes, err := reverseSupersedes(ctx, s, note.ID)
	if err != nil {
		return err
	}
	atts, err := entityAttachments(ctx, s, note.Attachments)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newNoteDTO(note, verdict, atts))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderNoteShow(note, verdict, supersedes, atts))
	return err
}

func showDoc(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, doc, err := docSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	head, err := resolveHead(ctx, s)
	if err != nil {
		return err
	}
	staleAfter, err := noteStaleAfter(ctx, s.Git)
	if err != nil {
		return err
	}
	verdict, err := docVerdict(ctx, s, head, doc, time.Now(), staleAfter, false)
	if err != nil {
		return err
	}
	supersedes, err := reverseSupersedesDocs(ctx, s, doc.ID)
	if err != nil {
		return err
	}
	atts, err := entityAttachments(ctx, s, doc.Attachments)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newDocDTO(doc, verdict, atts))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderDocShow(doc, verdict, supersedes, atts))
	return err
}

func showLog(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, log, err := logSpec.load(ctx, s, prefix)
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
}

func showTask(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, task, err := taskSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	live, err := allTasks(ctx, s)
	if err != nil {
		return err
	}
	blocks := blocksFor(live, task.ID)
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newTaskDTO(task, blocks))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderTaskShow(task, blocks))
	return err
}

func showSprint(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, sprint, err := sprintSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return err
	}
	members := tasksInSprint(tasks, sprint.ID)
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newSprintDTO(sprint, members))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderSprintShow(sprint, members))
	return err
}

func showRunbook(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, rb, err := runbookSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newRunbookDTO(rb))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderRunbookShow(rb))
	return err
}

func showProject(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, project, err := projectSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	sprints, err := s.ListSprints(ctx)
	if err != nil {
		return err
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return err
	}
	projectSprints := sprintsInProject(sprints, project.ID)
	projectTasks := tasksInProject(tasks, sprints, project.ID)
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newProjectDTO(project, projectSprints, projectTasks))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderProjectShow(project, projectSprints, projectTasks))
	return err
}
