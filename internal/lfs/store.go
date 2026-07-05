package lfs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Store is the local LFS content store rooted at <git-common-dir>/lfs — the
// same directory across linked worktrees, and the same layout the git-lfs
// CLI reads and writes. It only ever holds complete, verified objects:
// writes stream through sha256 into a temp file on the same volume and
// rename into place atomically, so a crash never leaves a partial object and
// concurrent writers of the same oid are idempotent. No fsync, matching
// git-lfs: a content-addressed object lost to power failure heals on the
// next sync's presence check.
type Store struct{ Dir string }

// Path returns oid's object path, objects/<oid[0:2]>/<oid[2:4]>/<oid> under
// the store root.
func (s Store) Path(oid string) string {
	return filepath.Join(s.Dir, "objects", oid[0:2], oid[2:4], oid)
}

// Has reports whether oid's content is present in the store.
func (s Store) Has(oid string) bool {
	_, err := os.Stat(s.Path(oid))
	return err == nil
}

// PutFile hashes path's content into the store and returns its oid and size.
func (s Store) PutFile(path string) (oid string, size int64, err error) {
	//nolint:gosec // G304: path is the file the caller asked PutFile to hash and ingest.
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	return s.put(f, "", -1)
}

// PutVerified streams r into the store, failing with ErrCorrupt — and
// installing nothing — unless the content hashes to wantOID and is wantSize
// bytes.
func (s Store) PutVerified(r io.Reader, wantOID string, wantSize int64) error {
	_, _, err := s.put(r, wantOID, wantSize)
	return err
}

// Open opens oid's content for reading; a missing object wraps
// ErrObjectMissing.
func (s Store) Open(oid string) (*os.File, error) {
	f, err := os.Open(s.Path(oid))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", oid, ErrObjectMissing)
	}
	return f, err
}

func (s Store) put(r io.Reader, wantOID string, wantSize int64) (string, int64, error) {
	tmpDir := filepath.Join(s.Dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return "", 0, err
	}
	tmp := filepath.Join(tmpDir, "obj-"+rand.Text())
	//nolint:gosec // G304: tmp is an internal staging path under the LFS cache dir, never attacker-supplied.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.Remove(tmp) }() // no-op after successful rename
	h := sha256.New()
	size, err := io.Copy(f, io.TeeReader(r, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	oid := hex.EncodeToString(h.Sum(nil))
	if wantOID != "" && (oid != wantOID || size != wantSize) {
		return "", 0, fmt.Errorf("%w: got %s/%d want %s/%d", ErrCorrupt, oid, size, wantOID, wantSize)
	}
	dest := s.Path(oid)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", 0, err
	}
	return oid, size, nil
}
