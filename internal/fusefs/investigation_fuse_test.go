//go:build fuse

package fusefs

import (
	"slices"
	"testing"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func createInvestigation(t *testing.T, s *store.Store, title string) model.Investigation {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateInvestigation{
		Nonce: model.NewNonce(), Title: title, Premise: "Workers hang after cancellation.",
	}})
	if err != nil {
		t.Fatalf("create investigation: %v", err)
	}
	inv := snap.(model.Investigation)
	next, err := s.Append(t.Context(), refs.For(model.KindInvestigation, inv.ID), []model.Op{
		model.AppendEntry{Text: "Captured a blocked worker stack."},
		model.AddAttachment{Name: "goroutines.txt", OID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 4096},
	})
	if err != nil {
		t.Fatalf("append investigation: %v", err)
	}
	return next.(model.Investigation)
}

func TestInvestigationMountReadOnly(t *testing.T) {
	f, s := newTestFS(t)
	inv := createInvestigation(t, s, "CI worker deadlock")
	name := Filename(inv)
	flat := "/investigations/" + name

	if root := readNames(t, f, "/"); !slices.Contains(root, "investigations") {
		t.Errorf("Readdir(/) = %v, want to contain investigations", root)
	}
	if names := readNames(t, f, "/investigations"); !slices.Contains(names, name) {
		t.Fatalf("Readdir(/investigations) = %v, want to contain %q", names, name)
	}
	if names := readNames(t, f, "/attachments"); !slices.Contains(names, inv.ID.Short()) {
		t.Errorf("Readdir(/attachments) = %v, want to contain %q", names, inv.ID.Short())
	}

	errno, fh := f.Open(flat, fuse.O_RDONLY)
	if errno != 0 {
		t.Fatalf("Open(O_RDONLY) = %d", errno)
	}
	buf := make([]byte, 8192)
	n := f.Read(flat, buf, 0, fh)
	if n < 0 {
		t.Fatalf("Read = %d", n)
	}
	if got, want := string(buf[:n]), string(RenderInvestigation(inv)); got != want {
		t.Errorf("read:\n got %q\nwant %q", got, want)
	}
	var st fuse.Stat_t
	if errno := f.Getattr(flat, &st, fh); errno != 0 {
		t.Fatalf("Getattr(open) = %d", errno)
	}
	if want := uint32(fuse.S_IFREG | 0o444); st.Mode != want {
		t.Errorf("open Getattr mode = %#o, want %#o", st.Mode, want)
	}
	f.Release(flat, fh)

	for _, flags := range []int{fuse.O_WRONLY, fuse.O_RDWR, fuse.O_RDWR | fuse.O_TRUNC, fuse.O_APPEND} {
		if errno, _ := f.Open(flat, flags); errno != -fuse.EACCES {
			t.Errorf("Open(%#x) = %d, want -EACCES", flags, errno)
		}
	}
	if errno := f.Truncate(flat, 0, invalidFh); errno != -fuse.EACCES {
		t.Errorf("Truncate = %d, want -EACCES", errno)
	}
	if errno, _ := f.Create("/investigations/fresh.md", fuse.O_WRONLY, 0o644); errno != -fuse.EPERM {
		t.Errorf("Create under /investigations = %d, want -EPERM", errno)
	}
	if errno := f.Rename("/notes/scratch.md", flat); errno != -fuse.EPERM {
		t.Errorf("Rename onto /investigations = %d, want -EPERM", errno)
	}
}
