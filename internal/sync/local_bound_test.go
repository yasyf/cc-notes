package sync_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

// TestLocalPolicyBoundLandsInTheRecords pins the local policy on a bound
// clone: policy config, push include, include.path, the withholding push, and
// the published-entity refusal all run on the records; the thin gains nothing.
func TestLocalPolicyBoundLandsInTheRecords(t *testing.T) {
	ctx := t.Context()
	f := newBoundSyncFixture(t)
	gittest.Git(t, f.source, "config", "cc-notes.localLabel", "secret")
	if _, err := ccsync.Install(ctx, f.thinStore.RecordsGit, "origin"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	public := refs.For(model.KindNote, createNote(t, f.thinStore, "public").ID)
	secret := refs.For(model.KindNote, createNote(t, f.thinStore, "secret").ID)
	appendOps(t, f.thinStore, secret, model.AddTag{Tag: "secret"})

	_, sourceCommon := gittest.Dirs(t, f.source)
	_, thinCommon := gittest.Dirs(t, f.thin)
	include, err := os.ReadFile(filepath.Join(sourceCommon, store.LocalPushInclude))
	if err != nil {
		t.Fatalf("records push include: %v", err)
	}
	for _, line := range []string{"secluded = " + secret, "push = ^" + secret} {
		if !strings.Contains(string(include), line) {
			t.Fatalf("records push include lacks %q:\n%s", line, include)
		}
	}
	if _, err := os.Stat(filepath.Join(thinCommon, store.LocalPushInclude)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("thin push include: stat = %v, want not exist", err)
	}
	if got := configAll(t, f.thinStore, "include.path"); len(got) != 0 {
		t.Fatalf("thin include.path = %v, want none", got)
	}
	if got, err := f.thinStore.RecordsGit.ConfigGetAll(ctx, "include.path"); err != nil || !slices.Contains(got, store.LocalPushInclude) {
		t.Fatalf("records include.path = %v, %v; want %s", got, err, store.LocalPushInclude)
	}

	report := sync(t, f.thinStore)
	if report.Pushed != 1 || report.Withheld != 1 {
		t.Fatalf("sync report = %+v, want 1 pushed, 1 withheld", report)
	}
	if !remoteHas(t, f.recordsRemote, public) || remoteHas(t, f.recordsRemote, secret) {
		t.Fatalf("records remote has public %v, secret %v; want public only", remoteHas(t, f.recordsRemote, public), remoteHas(t, f.recordsRemote, secret))
	}
	f.assertThinUntouched(t)

	raw := model.AddAttachment{Name: "run.log", OID: "e3b1c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Size: 1}
	_, err = f.thinStore.Append(ctx, public, []model.Op{raw})
	if refusal := (*store.LocalAttachmentError)(nil); !errors.As(err, &refusal) {
		t.Fatalf("Append raw log to the published note: err = %v, want LocalAttachmentError", err)
	}
	f.assertThinUntouched(t)
}
