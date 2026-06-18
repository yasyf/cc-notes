package cli

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestHierarchyTasksInSprint(t *testing.T) {
	tasks := []model.Task{
		{ID: "t3", Sprint: "sp1"},
		{ID: "t1", Sprint: "sp1"},
		{ID: "t2", Sprint: "sp2"},
		{ID: "t4", Sprint: ""},
	}
	for _, tc := range []struct {
		name   string
		sprint model.EntityID
		want   []model.EntityID
	}{
		{"two members sorted", "sp1", []model.EntityID{"t1", "t3"}},
		{"single member", "sp2", []model.EntityID{"t2"}},
		{"no members", "sp9", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tasksInSprint(tasks, tc.sprint)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("tasksInSprint(%q) = %v, want %v", tc.sprint, got, tc.want)
			}
		})
	}
}

func TestHierarchySprintsInProject(t *testing.T) {
	sprints := []model.Sprint{
		{ID: "sp3", Project: "pr1"},
		{ID: "sp1", Project: "pr1"},
		{ID: "sp2", Project: "pr2"},
		{ID: "sp4", Project: ""},
	}
	for _, tc := range []struct {
		name    string
		project model.EntityID
		want    []model.EntityID
	}{
		{"two members sorted", "pr1", []model.EntityID{"sp1", "sp3"}},
		{"single member", "pr2", []model.EntityID{"sp2"}},
		{"no members", "pr9", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sprintsInProject(sprints, tc.project)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("sprintsInProject(%q) = %v, want %v", tc.project, got, tc.want)
			}
		})
	}
}

func TestHierarchyTasksInProject(t *testing.T) {
	sprints := []model.Sprint{
		{ID: "sp1", Project: "pr1"},
		{ID: "sp2", Project: "pr2"},
		{ID: "sp3", Project: ""},
	}
	for _, tc := range []struct {
		name    string
		tasks   []model.Task
		project model.EntityID
		want    []model.EntityID
	}{
		{
			name: "direct membership only",
			tasks: []model.Task{
				{ID: "t2", Project: "pr1"},
				{ID: "t1", Project: "pr1"},
				{ID: "t3", Project: "pr2"},
			},
			project: "pr1",
			want:    []model.EntityID{"t1", "t2"},
		},
		{
			name: "via-sprint membership only",
			tasks: []model.Task{
				{ID: "t1", Sprint: "sp1"},
				{ID: "t2", Sprint: "sp2"},
				{ID: "t3", Sprint: "sp3"},
			},
			project: "pr1",
			want:    []model.EntityID{"t1"},
		},
		{
			name: "union of direct and via-sprint",
			tasks: []model.Task{
				{ID: "t1", Project: "pr1"},
				{ID: "t2", Sprint: "sp1"},
				{ID: "t3", Project: "pr2", Sprint: "sp2"},
			},
			project: "pr1",
			want:    []model.EntityID{"t1", "t2"},
		},
		{
			name: "dedup task that is both direct and via-sprint",
			tasks: []model.Task{
				{ID: "t1", Project: "pr1", Sprint: "sp1"},
			},
			project: "pr1",
			want:    []model.EntityID{"t1"},
		},
		{
			name: "task in project via direct but sprint in other project",
			tasks: []model.Task{
				{ID: "t1", Project: "pr1", Sprint: "sp2"},
			},
			project: "pr1",
			want:    []model.EntityID{"t1"},
		},
		{
			name: "no members",
			tasks: []model.Task{
				{ID: "t1", Project: "pr2", Sprint: "sp2"},
				{ID: "t2", Sprint: "sp3"},
			},
			project: "pr1",
			want:    nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tasksInProject(tc.tasks, sprints, tc.project)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("tasksInProject(%q) = %v, want %v", tc.project, got, tc.want)
			}
		})
	}
}
