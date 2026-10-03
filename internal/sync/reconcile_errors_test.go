package sync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// TestReconcileNamesEveryRefAMovedBindingHid pins that reconcile joins every
// worker's failure: two refs published into a moved binding are both named.
func TestReconcileNamesEveryRefAMovedBindingHid(t *testing.T) {
	ctx := t.Context()
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "base")
	thin := gittest.ShallowClone(t, source, 1)
	if _, err := store.Bind(ctx, thin, source); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	s, err := store.Open(thin)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	scope := map[string]model.SHA{}
	for _, title := range []string{"one", "two"} {
		snap, err := s.Create(ctx, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: title}})
		if err != nil {
			t.Fatalf("Create %s: %v", title, err)
		}
		ref := refs.For(model.KindNote, snap.EntityID())
		scope[ref] = model.SHA(gittest.Git(t, source, "rev-parse", "--verify", ref))
		gittest.Git(t, source, "update-ref", "-d", ref)
	}
	other := filepath.Join(gittest.InitRepo(t), ".git")
	info, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	device, inode := gitobj.FileID(info)
	gittest.Git(t, thin, "config", "cc-notes.storage", store.Binding{CommonDir: other, Device: device, Inode: inode}.String())

	e := &engine{store: s, remote: "origin"}
	err = e.reconcile(ctx, scope)
	if !errors.Is(err, store.ErrBindingChanged) {
		t.Fatalf("reconcile = %v, want ErrBindingChanged", err)
	}
	for ref, tip := range scope {
		if !strings.Contains(err.Error(), "published "+ref) {
			t.Fatalf("reconcile error does not name %s:\n%v", ref, err)
		}
		if got := model.SHA(gittest.Git(t, source, "rev-parse", "--verify", ref)); got != tip {
			t.Fatalf("%s = %s after reconcile, want the published tip %s", ref, got, tip)
		}
	}
}
