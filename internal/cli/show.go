package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// showHistoryCap bounds the append-only history a show embeds: the most recent
// entries, findings, comments, or runs survive, with the elided count beside
// them. The rest stays reachable through that kind's list verb.
const showHistoryCap = 20

// newShowCmd builds the top-level "cc-notes show ID": show any entity, resolving
// the id across every kind and dispatching to that kind's renderer. Like
// history, compact, and blame, it is global because an id-addressed read whose
// kind is inferable from the resolved ref needs no noun — the noun-scoped
// "<kind> show" commands remain for a kind-checked lookup.
func newShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show any note, doc, log, task, sprint, project, runbook, investigation, or plan by id",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
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
				return showNote(cmd, s, c, id, jsonOut)
			case model.KindDoc:
				return showDoc(cmd, s, c, id, jsonOut)
			case model.KindLog:
				return showLog(cmd, s, c, id, jsonOut)
			case model.KindTask:
				return showTask(cmd, s, c, id, jsonOut)
			case model.KindSprint:
				return showSprint(cmd, s, c, id, jsonOut)
			case model.KindProject:
				return showProject(cmd, s, c, id, jsonOut)
			case model.KindRunbook:
				return showRunbook(cmd, s, c, id, jsonOut)
			case model.KindInvestigation:
				return showInvestigation(cmd, s, c, id, jsonOut)
			case model.KindPlan:
				return showPlan(cmd, s, c, id, jsonOut)
			default:
				panic(fmt.Sprintf("ResolveEntity returned unknown kind %q", kind))
			}
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func showNote(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
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
	supersedes, err := c.NoteSuperseders(ctx, note.ID)
	if err != nil {
		return err
	}
	liveHead, err := c.NoteSupersedeHeads(ctx, note.ID)
	if err != nil {
		return err
	}
	atts, err := entityAttachments(ctx, c, note.Attachments)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newNoteDTO(note, verdict, supersedes, liveHead, atts))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderNoteShow(note, verdict, supersedes, atts))
	return err
}

func showDoc(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
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
	supersedes, err := c.DocSuperseders(ctx, doc.ID)
	if err != nil {
		return err
	}
	liveHead, err := c.DocSupersedeHeads(ctx, doc.ID)
	if err != nil {
		return err
	}
	atts, err := entityAttachments(ctx, c, doc.Attachments)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newDocDTO(doc, verdict, supersedes, liveHead, atts))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderDocShow(doc, verdict, supersedes, atts))
	return err
}

func showLog(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, log, err := logSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	atts, err := entityAttachments(ctx, c, log.Attachments)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newLogShowDTO(log, atts))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderLogShow(log, atts))
	return err
}

func showInvestigation(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	ref, inv, err := investigationSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	atts, err := entityAttachments(ctx, c, inv.Attachments)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newInvestigationShowDTO(inv, atts))
	}
	steps, err := s.History(ctx, ref)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderInvestigationShow(inv, atts, steps))
	return err
}

func showTask(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, task, err := taskSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	blocks, err := c.TasksBlocking(ctx, task.ID)
	if err != nil {
		return err
	}
	children, err := c.TaskChildren(ctx, task.ID)
	if err != nil {
		return err
	}
	runs, err := c.TaskRuns(ctx, task.ID)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newTaskShowDTO(task, blocks, children, runs))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderTaskShow(task, blocks))
	return err
}

func showSprint(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, sprint, err := sprintSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	members, err := c.SprintTasks(ctx, sprint.ID)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newSprintDTO(sprint, members))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderSprintShow(sprint, members))
	return err
}

func showRunbook(cmd *cobra.Command, s *store.Store, _ *notes.Client, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, rb, err := runbookSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newRunbookShowDTO(rb))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderRunbookShow(rb))
	return err
}

func showProject(cmd *cobra.Command, s *store.Store, c *notes.Client, prefix string, jsonOut bool) error {
	ctx := cmd.Context()
	_, project, err := projectSpec.load(ctx, s, prefix)
	if err != nil {
		return err
	}
	projectSprints, err := c.ProjectSprints(ctx, project.ID)
	if err != nil {
		return err
	}
	projectTasks, err := c.ProjectTasks(ctx, project.ID)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), newProjectDTO(project, projectSprints, projectTasks))
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderProjectShow(project, projectSprints, projectTasks))
	return err
}

// logShowDTO is the log DTO with its entries capped at the most recent
// showHistoryCap, beside the count of older entries elided.
type logShowDTO struct {
	logDTO
	EntriesOmitted int `json:"entries_omitted,omitempty"`
}

func newLogShowDTO(l model.Log, atts []attachmentDTO) logShowDTO {
	dto := newLogDTO(l, atts)
	entries, omitted := tailWithCount(dto.Entries, showHistoryCap)
	dto.Entries = entries
	return logShowDTO{logDTO: dto, EntriesOmitted: omitted}
}

// investigationShowDTO is the investigation DTO with its findings and timeline
// each capped at the most recent showHistoryCap, beside the elided counts.
type investigationShowDTO struct {
	investigationDTO
	FindingsOmitted int `json:"findings_omitted,omitempty"`
	EntriesOmitted  int `json:"entries_omitted,omitempty"`
}

func newInvestigationShowDTO(inv model.Investigation, atts []attachmentDTO) investigationShowDTO {
	dto := newInvestigationDTO(inv, atts)
	findings, findingsOmitted := tailWithCount(dto.Findings, showHistoryCap)
	dto.Findings = findings
	entries, entriesOmitted := tailWithCount(dto.Entries, showHistoryCap)
	dto.Entries = entries
	return investigationShowDTO{investigationDTO: dto, FindingsOmitted: findingsOmitted, EntriesOmitted: entriesOmitted}
}

// taskShowDTO is the task DTO with its comments capped at the most recent
// showHistoryCap, beside the count of older comments elided.
type taskShowDTO struct {
	taskDTO
	CommentsOmitted int `json:"comments_omitted,omitempty"`
}

func newTaskShowDTO(t model.Task, blocks, children []model.EntityID, runs []notes.TaskRun) taskShowDTO {
	dto := newTaskDTO(t, blocks, children, runs)
	comments, omitted := tailWithCount(dto.Comments, showHistoryCap)
	dto.Comments = comments
	return taskShowDTO{taskDTO: dto, CommentsOmitted: omitted}
}

// runbookShowDTO is the runbook DTO with its runs capped at the most recent
// showHistoryCap, beside the count of older runs elided.
type runbookShowDTO struct {
	runbookDTO
	RunsOmitted int `json:"runs_omitted,omitempty"`
}

func newRunbookShowDTO(rb model.Runbook) runbookShowDTO {
	dto := newRunbookDTO(rb)
	runs, omitted := tailWithCount(dto.Runs, showHistoryCap)
	dto.Runs = runs
	return runbookShowDTO{runbookDTO: dto, RunsOmitted: omitted}
}
