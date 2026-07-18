package viz

import (
	"net/http"

	"github.com/yasyf/cc-notes/model"
)

// stateResponse is the /api/entities payload: the full folded snapshot of every
// live entity, grouped by kind. Superseded notes and docs stay in (the snapshot
// flags them), matching the legend; tombstoned entities drop out.
type stateResponse struct {
	Notes          []model.Note          `json:"notes"`
	Docs           []model.Doc           `json:"docs"`
	Logs           []model.Log           `json:"logs"`
	Tasks          []model.Task          `json:"tasks"`
	Sprints        []model.Sprint        `json:"sprints"`
	Projects       []model.Project       `json:"projects"`
	Runbooks       []model.Runbook       `json:"runbooks"`
	Investigations []model.Investigation `json:"investigations"`
}

func (s *Server) handleEntities(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	notes, err := s.store.ListNotes(ctx, false, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	docs, err := s.store.ListDocs(ctx, false, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logs, err := s.store.ListLogs(ctx, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks, err := s.store.ListTasks(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sprints, err := s.store.ListSprints(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runbooks, err := s.store.ListRunbooks(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	investigations, err := s.store.ListInvestigations(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stateResponse{
		Notes:          notes,
		Docs:           docs,
		Logs:           logs,
		Tasks:          tasks,
		Sprints:        sprints,
		Projects:       projects,
		Runbooks:       runbooks,
		Investigations: investigations,
	})
}
