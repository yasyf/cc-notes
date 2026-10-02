package sync_test

import (
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

func remoteHas(t *testing.T, bare, ref string) bool {
	t.Helper()
	return gittest.Git(t, bare, "for-each-ref", ref) != ""
}

func TestLocalEntitiesStayOffTheRemote(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, a.Git.Dir, "commit", "-q", "--allow-empty", "-m", "init")
	if _, err := ccsync.Install(t.Context(), a.Git, "origin"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	public := refs.For(model.KindNote, createNote(t, a, "public").ID)
	labelled := refs.For(model.KindNote, createNote(t, a, "brief").ID)
	appendOps(t, a, labelled, model.AddTag{Tag: "lane-brief"})
	task := refs.For(model.KindTask, createTask(t, a, "private task", "main").ID)
	appendOps(t, a, task, model.AddLabel{Label: store.LocalLabel})

	gittest.Git(t, a.Git.Dir, "push", "-q", "origin")
	if !remoteHas(t, bare, public) || remoteHas(t, bare, labelled) || remoteHas(t, bare, task) {
		t.Fatalf("plain push published a local entity or withheld the public one")
	}

	report := sync(t, a)
	if report.Withheld != 2 {
		t.Errorf("Withheld = %d, want 2", report.Withheld)
	}
	if remoteHas(t, bare, labelled) || remoteHas(t, bare, task) {
		t.Fatalf("sync published a local entity")
	}

	appendOps(t, a, labelled, model.AddTag{Tag: store.SyncedLabel})
	sync(t, a)
	if !remoteHas(t, bare, labelled) {
		t.Errorf("synced label did not publish %s", labelled)
	}
	if remoteHas(t, bare, task) {
		t.Errorf("sync published local task %s", task)
	}
}

func TestPublishedEntityRefusesLocalAttachment(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, a.Git.Dir, "commit", "-q", "--allow-empty", "-m", "init")
	if _, err := ccsync.Install(t.Context(), a.Git, "origin"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	ref := refs.For(model.KindNote, createNote(t, a, "published").ID)
	sync(t, a)
	raw := model.AddAttachment{Name: "run.log", OID: "e3b1c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Size: 1}

	_, err := a.Append(t.Context(), ref, []model.Op{raw})
	if refusal := (*store.LocalAttachmentError)(nil); !errors.As(err, &refusal) {
		t.Fatalf("Append raw log to published note: err = %v, want LocalAttachmentError", err)
	}
	appendOps(t, a, ref, model.AddTag{Tag: store.SyncedLabel}, raw)
}

func TestPolicyReasons(t *testing.T) {
	policy := store.DefaultLocalPolicy
	cases := []struct {
		name string
		meta model.Meta
		want string
	}{
		{"plain", model.Meta{}, ""},
		{"local beats synced", model.Meta{Labels: []string{store.LocalLabel, store.SyncedLabel}}, "label local"},
		{"synced beats defaults", model.Meta{Labels: []string{store.SyncedLabel, "lane-brief"}}, ""},
		{"default label", model.Meta{Labels: []string{"raw-capture"}}, "label raw-capture"},
		{"raw glob", model.Meta{Attachments: []model.Attachment{{Name: "build.log", Size: 1}}}, "attachment build.log matches *.log"},
		{"too large", model.Meta{Attachments: []model.Attachment{{Name: "dump.bin", Size: 11 << 20}}}, "attachment dump.bin is 11534336 bytes, over 10485760"},
		{"small evidence", model.Meta{Attachments: []model.Attachment{{Name: "evidence.md", Size: 100}}}, ""},
	}
	for _, c := range cases {
		if got := policy.Reason(c.meta); got != c.want {
			t.Errorf("%s: Reason = %q, want %q", c.name, got, c.want)
		}
	}
}
