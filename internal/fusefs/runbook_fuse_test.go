//go:build fuse

package fusefs

import (
	"slices"
	"strings"
	"testing"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// createRunbook builds a runbook with two steps and one finished run — the
// shape RenderRunbook exercises end to end.
func createRunbook(t *testing.T, s *store.Store, title string) model.Runbook {
	t.Helper()
	step1, step2 := model.NewNonce(), model.NewNonce()
	snap, err := s.Create(t.Context(), []model.Op{
		model.CreateRunbook{Nonce: model.NewNonce(), Title: title, Description: "Ship it."},
		model.AddStep{ID: step1, Text: "Build", Command: "make build", Position: "a"},
		model.AddStep{ID: step2, Text: "Deploy", Position: "b"},
	})
	if err != nil {
		t.Fatalf("create runbook: %v", err)
	}
	rb := snap.(model.Runbook)
	runID := model.NewNonce()
	next, err := s.Append(t.Context(), refs.For(model.KindRunbook, rb.ID), []model.Op{
		model.StartRun{ID: runID},
		model.SetRunStepStatus{RunID: runID, StepID: step1, Status: model.StepDone},
		model.FinishRun{ID: runID, Status: model.RunSucceeded},
	})
	if err != nil {
		t.Fatalf("append run: %v", err)
	}
	return next.(model.Runbook)
}

func TestRunbookMountReadOnly(t *testing.T) {
	f, s := newTestFS(t)
	rb := createRunbook(t, s, "Deploy")
	name := RunbookFilename(rb)
	flat := "/runbooks/" + name

	if root := readNames(t, f, "/"); !slices.Contains(root, "runbooks") {
		t.Errorf("Readdir(/) = %v, want to contain \"runbooks\"", root)
	}
	if names := readNames(t, f, "/runbooks"); !slices.Contains(names, name) {
		t.Fatalf("Readdir(/runbooks) = %v, want to contain %q", names, name)
	}

	errc, fh := f.Open(flat, fuse.O_RDONLY)
	if errc != 0 {
		t.Fatalf("Open(O_RDONLY) = %d", errc)
	}
	buf := make([]byte, 8192)
	n := f.Read(flat, buf, 0, fh)
	if n < 0 {
		t.Fatalf("Read = %d", n)
	}
	loaded, err := s.Load(t.Context(), refs.For(model.KindRunbook, rb.ID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := string(buf[:n]), string(RenderRunbook(loaded.(model.Runbook))); got != want {
		t.Errorf("read:\n got %q\nwant %q", got, want)
	}

	var st fuse.Stat_t
	if errc := f.Getattr(flat, &st, fh); errc != 0 {
		t.Fatalf("Getattr(open) = %d", errc)
	}
	if want := uint32(fuse.S_IFREG | 0o444); st.Mode != want {
		t.Errorf("open Getattr mode = %#o, want %#o", st.Mode, want)
	}
	f.Release(flat, fh)

	st = fuse.Stat_t{}
	if errc := f.Getattr(flat, &st, invalidFh); errc != 0 {
		t.Fatalf("Getattr(unopened) = %d", errc)
	}
	if want := uint32(fuse.S_IFREG | 0o444); st.Mode != want {
		t.Errorf("unopened Getattr mode = %#o, want %#o", st.Mode, want)
	}

	for _, fl := range []int{fuse.O_WRONLY, fuse.O_RDWR, fuse.O_RDWR | fuse.O_TRUNC, fuse.O_APPEND} {
		if errc, _ := f.Open(flat, fl); errc != -fuse.EACCES {
			t.Errorf("Open(%#x) = %d, want -EACCES", fl, errc)
		}
	}

	if errc := f.Truncate(flat, 0, invalidFh); errc != -fuse.EACCES {
		t.Errorf("Truncate = %d, want -EACCES", errc)
	}
	if errc, _ := f.Create("/runbooks/fresh.md", fuse.O_WRONLY, 0o644); errc != -fuse.EPERM {
		t.Errorf("Create under /runbooks = %d, want -EPERM", errc)
	}
	if errc := f.Rename("/notes/scratch.md", flat); errc != -fuse.EPERM {
		t.Errorf("Rename onto /runbooks = %d, want -EPERM", errc)
	}
}

func TestRunbookReaddirIncludesArchived(t *testing.T) {
	f, s := newTestFS(t)
	rb := createRunbook(t, s, "Legacy")
	appendOps(t, s, refs.For(model.KindRunbook, rb.ID), model.SetRunbookStatus{Status: model.RunbookArchived})
	if names := readNames(t, f, "/runbooks"); !slices.Contains(names, RunbookFilename(rb)) {
		t.Errorf("Readdir(/runbooks) = %v, want to include archived runbook %q", names, RunbookFilename(rb))
	}
}

// TestRunbookExternalAppendVisible pins the notesSeed cache-defeat path: an
// external same-second CLI commit changes the Getattr mtime nanosecond (so
// FUSE-T's NFS client cannot keep serving stale pages) and the next read
// reflects the new step.
func TestRunbookExternalAppendVisible(t *testing.T) {
	f, s := newTestFS(t)
	rb := createRunbook(t, s, "Deploy")
	ref := refs.For(model.KindRunbook, rb.ID)
	flat := "/runbooks/" + RunbookFilename(rb)

	var st fuse.Stat_t
	if errc := getattrDefeated(f, flat, &st, invalidFh); errc != 0 {
		t.Fatalf("Getattr = %d", errc)
	}
	before := st.Mtim.Nsec

	appendOps(t, s, ref, model.AddStep{ID: model.NewNonce(), Text: "Verify", Position: "c"})

	st = fuse.Stat_t{}
	if errc := getattrDefeated(f, flat, &st, invalidFh); errc != 0 {
		t.Fatalf("Getattr after append = %d", errc)
	}
	if st.Mtim.Nsec == before {
		t.Errorf("mtime nanosecond unchanged after external append; a stale render would be served")
	}

	errc, fh := f.Open(flat, fuse.O_RDONLY)
	if errc != 0 {
		t.Fatalf("Open = %d", errc)
	}
	defer f.Release(flat, fh)
	buf := make([]byte, 8192)
	n := f.Read(flat, buf, 0, fh)
	if n < 0 {
		t.Fatalf("Read = %d", n)
	}
	if !strings.Contains(string(buf[:n]), "Verify") {
		t.Errorf("read after append missing the new step:\n%s", buf[:n])
	}
}
