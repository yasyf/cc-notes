package cli

import (
	"slices"

	"github.com/yasyf/cc-notes/internal/model"
)

// tasksInSprint returns the sorted ids of the tasks whose folded sprint is
// sprintID — the reverse of a task's LWW sprint membership.
func tasksInSprint(tasks []model.Task, sprintID model.EntityID) []model.EntityID {
	var ids []model.EntityID
	for _, t := range tasks {
		if t.Sprint == sprintID {
			ids = append(ids, t.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// sprintsInProject returns the sorted ids of the sprints whose folded project
// is projectID — the reverse of a sprint's project membership.
func sprintsInProject(sprints []model.Sprint, projectID model.EntityID) []model.EntityID {
	var ids []model.EntityID
	for _, s := range sprints {
		if s.Project == projectID {
			ids = append(ids, s.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// tasksInProject returns the sorted, deduplicated ids of the tasks belonging to
// projectID: the union of tasks pointed directly at the project and tasks whose
// sprint belongs to the project. A task counted both ways appears once.
func tasksInProject(tasks []model.Task, sprints []model.Sprint, projectID model.EntityID) []model.EntityID {
	projectSprints := make(map[model.EntityID]bool)
	for _, s := range sprints {
		if s.Project == projectID {
			projectSprints[s.ID] = true
		}
	}
	seen := make(map[model.EntityID]bool)
	var ids []model.EntityID
	for _, t := range tasks {
		if t.Project == projectID || (t.Sprint != "" && projectSprints[t.Sprint]) {
			if !seen[t.ID] {
				seen[t.ID] = true
				ids = append(ids, t.ID)
			}
		}
	}
	slices.Sort(ids)
	return ids
}
