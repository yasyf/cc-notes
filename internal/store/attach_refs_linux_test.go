package store

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
	"golang.org/x/sys/unix"
)

func TestReferencedAttachmentsPackedRefPasses(t *testing.T) {
	s := initStore(t)
	note, err := s.Create(t.Context(), noteOps("attachment ref passes"))
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	attach(t, s, refs.For(model.KindNote, note.EntityID()), "a.bin", oidA, 5)
	gittest.Git(t, s.Git.Dir, "pack-refs", "--all")
	referenced(t, s)

	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatalf("create inotify: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Close(fd); err != nil {
			t.Errorf("close inotify: %v", err)
		}
	})
	if _, err := unix.InotifyAddWatch(fd, filepath.Join(s.recordsCommonDir, "packed-refs"), unix.IN_OPEN|unix.IN_CLOSE_NOWRITE); err != nil {
		t.Fatalf("watch packed-refs: %v", err)
	}
	referenced(t, s)
	var data [4096]byte
	opens := 0
	for {
		n, err := unix.Read(fd, data[:])
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatalf("read inotify: %v", err)
		}
		for at := 0; at < n; {
			mask := binary.NativeEndian.Uint32(data[at+4 : at+8])
			if mask&unix.IN_Q_OVERFLOW != 0 {
				t.Fatal("inotify queue overflow")
			}
			if mask&unix.IN_OPEN != 0 {
				opens++
			}
			at += unix.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(data[at+12:at+16]))
		}
	}
	t.Logf("packed-refs opens per attachment scan = %d", opens)
	if opens != 1 {
		t.Fatalf("packed-refs opens per attachment scan = %d, want 1", opens)
	}
}
