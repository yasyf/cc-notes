package sync_test

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

type boundSyncFixture struct {
	source, recordsRemote string
	thin, thinRemote      string
	thinStore             *store.Store
}

func newBoundSyncFixture(t *testing.T) *boundSyncFixture {
	t.Helper()
	f := &boundSyncFixture{}
	f.source = gittest.InitRepo(t)
	gittest.Git(t, f.source, "commit", "-q", "--allow-empty", "-m", "base")
	f.recordsRemote = gittest.InitBare(t)
	gittest.Git(t, f.source, "remote", "add", "origin", f.recordsRemote)
	f.thin = gittest.ShallowClone(t, f.source, 1)
	f.thinRemote = gittest.InitBare(t)
	gittest.Git(t, f.thin, "remote", "set-url", "origin", f.thinRemote)
	gittest.Git(t, f.thin, "remote", "add", "upstream", f.thinRemote)
	if _, err := store.Bind(t.Context(), f.thin, f.source); err != nil {
		t.Fatalf("Bind(thin, source): %v", err)
	}
	s, err := store.Open(f.thin)
	if err != nil {
		t.Fatalf("Open(thin): %v", err)
	}
	f.thinStore = s
	return f
}

func (f *boundSyncFixture) assertThinUntouched(t *testing.T) {
	t.Helper()
	for _, dir := range []string{f.thin, f.thinRemote} {
		if out := gittest.Git(t, dir, "for-each-ref", "--format=%(refname)", "refs/cc-notes/", "refs/cc-notes-sync/"); out != "" {
			t.Fatalf("%s holds records state:\n%s", dir, out)
		}
	}
}

func noteIDs(notes []model.Note) []model.EntityID {
	ids := make([]model.EntityID, len(notes))
	for i, n := range notes {
		ids[i] = n.ID
	}
	slices.Sort(ids)
	return ids
}

func TestSyncBoundUsesRecordsRemote(t *testing.T) {
	ctx := t.Context()
	f := newBoundSyncFixture(t)
	server, lfsURL := newFakeLFS(t)
	authValue := "basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:records-only"))
	server.requireAuth = authValue
	authHeaderKey := "http." + strings.TrimSuffix(lfsURL, "/lfs") + "/.extraheader"
	gittest.Git(t, f.source, "config", "lfs.url", lfsURL)
	gittest.Git(t, f.source, "config", authHeaderKey, "AUTHORIZATION: "+authValue)

	note := createNote(t, f.thinStore, "thin-authored")
	ref := refs.For(model.KindNote, note.ID)
	att := attachFile(t, f.thinStore, ref, "thin.bin", []byte("attached through the thin store"))

	report := sync(t, f.thinStore)
	if report.Pushed != 1 || report.Uploaded != 1 || report.Rounds != 1 {
		t.Fatalf("first sync report = %+v, want 1 pushed, 1 uploaded, 1 round", report)
	}
	wantRefs := map[string]string{ref: gittest.Git(t, f.source, "rev-parse", "--verify", ref)}
	if got := ccRefs(t, f.recordsRemote); !reflect.DeepEqual(got, wantRefs) {
		t.Fatalf("records remote refs = %v, want %v", got, wantRefs)
	}
	if !server.isVerified(att.OID) {
		t.Fatalf("LFS endpoint discovered from the records config never received %s", att.OID)
	}
	f.assertThinUntouched(t)

	peer := clone(t, f.recordsRemote, "Peer", "peer@example.com")
	gittest.Git(t, peer.Git.Dir, "config", "lfs.url", lfsURL)
	gittest.Git(t, peer.Git.Dir, "config", authHeaderKey, "AUTHORIZATION: "+authValue)
	peerNote := createNote(t, peer, "peer-authored")
	if peerReport := sync(t, peer); peerReport.Downloaded != 1 {
		t.Fatalf("peer sync report = %+v, want the thin-uploaded attachment downloaded", peerReport)
	}
	report = sync(t, f.thinStore)
	if report.Created != 1 || report.Pushed != 0 {
		t.Fatalf("second sync report = %+v, want 1 created, 0 pushed", report)
	}
	wantTracking := []string{"refs/cc-notes-sync/origin/notes/" + string(note.ID), "refs/cc-notes-sync/origin/notes/" + string(peerNote.ID)}
	slices.Sort(wantTracking)
	if got := gittest.Git(t, f.source, "for-each-ref", "--format=%(refname)", "refs/cc-notes-sync/origin/"); got != wantTracking[0]+"\n"+wantTracking[1] {
		t.Fatalf("source tracking refs:\n%s\nwant:\n%s", got, wantTracking[0]+"\n"+wantTracking[1])
	}
	wantIDs := noteIDs([]model.Note{note, peerNote})
	src, err := store.Open(f.source)
	if err != nil {
		t.Fatalf("Open(source): %v", err)
	}
	for name, s := range map[string]*store.Store{"source": src, "thin": f.thinStore} {
		notes, err := s.ListNotes(ctx, false, false)
		if err != nil {
			t.Fatalf("%s ListNotes: %v", name, err)
		}
		if got := noteIDs(notes); !reflect.DeepEqual(got, wantIDs) {
			t.Fatalf("%s ListNotes ids = %v, want %v", name, got, wantIDs)
		}
	}
	f.assertThinUntouched(t)

	if _, err := ccsync.Sync(ctx, f.thinStore, "upstream", false); !errors.Is(err, ccsync.ErrRemoteNotFound) {
		t.Fatalf("Sync over a remote only the thin configures = %v, want ErrRemoteNotFound", err)
	}
	f.assertThinUntouched(t)
}

func TestSyncBoundRefusesReplacedBackend(t *testing.T) {
	ctx := t.Context()
	f := newBoundSyncFixture(t)
	createNote(t, f.thinStore, "before the swap")
	if report := sync(t, f.thinStore); report.Pushed != 1 {
		t.Fatalf("sync before the swap = %+v, want 1 pushed", report)
	}
	sourceCommon, err := filepath.EvalSymlinks(filepath.Join(f.source, ".git"))
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if err := os.Rename(f.source, f.source+".replaced"); err != nil {
		t.Fatalf("move source aside: %v", err)
	}
	if err := os.Mkdir(f.source, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	gittest.Git(t, f.source, "init", "-q", "-b", "main")
	gittest.Git(t, f.source, "remote", "add", "origin", f.recordsRemote)

	_, err = ccsync.Sync(ctx, f.thinStore, "origin", false)
	var be *store.BindingError
	if !errors.Is(err, store.ErrBackendReplaced) || !errors.As(err, &be) {
		t.Fatalf("Sync after the backend was replaced = %v, want a *store.BindingError wrapping ErrBackendReplaced", err)
	}
	if be.Source != sourceCommon {
		t.Fatalf("BindingError.Source = %q, want %q", be.Source, sourceCommon)
	}
	if out := gittest.Git(t, f.source, "for-each-ref", "--format=%(refname)", "refs/cc-notes/", "refs/cc-notes-sync/"); out != "" {
		t.Fatalf("the replacement repository gained records state from a refused sync; the remote's ref was fetched before the backend was rechecked:\n%s", out)
	}
	f.assertThinUntouched(t)
}

// TestSyncBoundResolvesRelativeRemoteFromSource pins the transport working
// directory: a bound sync runs git from the source's working tree, so a
// relative remote URL or pushurl reaches the sibling origin.git that git run
// inside the source reaches, never a repository nested inside the source.
func TestSyncBoundResolvesRelativeRemoteFromSource(t *testing.T) {
	rows := []struct {
		name   string
		remote func(t *testing.T, source, sibling string)
	}{
		{name: "relative url", remote: func(t *testing.T, source, _ string) {
			gittest.Git(t, source, "remote", "add", "origin", "../origin.git")
		}},
		{name: "relative pushurl", remote: func(t *testing.T, source, sibling string) {
			gittest.Git(t, source, "remote", "add", "origin", sibling)
			gittest.Git(t, source, "remote", "set-url", "--push", "origin", "../origin.git")
		}},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			gittest.ScrubEnv(t)
			root := t.TempDir()
			source, sibling, nested := filepath.Join(root, "source"), filepath.Join(root, "origin.git"), filepath.Join(root, "source", "origin.git")
			gittest.Git(t, root, "init", "-q", "-b", "main", "source")
			gittest.Git(t, source, "config", "user.name", "Test User")
			gittest.Git(t, source, "config", "user.email", "test@example.com")
			gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "base")
			gittest.Git(t, root, "init", "-q", "--bare", "origin.git")
			gittest.Git(t, source, "init", "-q", "--bare", "origin.git")
			tc.remote(t, source, sibling)
			checkout := filepath.Join(root, "checkout")
			gittest.Git(t, root, "clone", "-q", "--no-local", "--depth=1", "--single-branch", "file://"+source, "checkout")
			gittest.Git(t, checkout, "config", "user.name", "Test User")
			gittest.Git(t, checkout, "config", "user.email", "test@example.com")
			if _, err := store.Bind(t.Context(), checkout, source); err != nil {
				t.Fatalf("Bind(checkout, source): %v", err)
			}
			s, err := store.Open(checkout)
			if err != nil {
				t.Fatalf("Open(checkout): %v", err)
			}
			note := createNote(t, s, "routed through the source")
			ref := refs.For(model.KindNote, note.ID)

			if report := sync(t, s); report.Pushed != 1 {
				t.Fatalf("sync report = %+v, want 1 pushed", report)
			}
			want := map[string]string{ref: gittest.Git(t, source, "rev-parse", "--verify", ref)}
			if got := ccRefs(t, sibling); !reflect.DeepEqual(got, want) {
				t.Fatalf("sibling origin.git refs = %v, want %v", got, want)
			}
			if got := ccRefs(t, nested); len(got) != 0 {
				t.Fatalf("the repository nested inside the source received %v: the relative URL resolved from the git directory", got)
			}
			if got := gittest.Git(t, source, "ls-remote", "--refs", "origin", ref); !strings.HasSuffix(got, "\t"+ref) {
				t.Fatalf("git ls-remote origin inside the source = %q, want %s where the bound sync pushed", got, ref)
			}
		})
	}
}
