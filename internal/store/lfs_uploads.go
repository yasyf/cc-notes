package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const uploadedAttachmentsSubdir = "cc-notes/lfs-uploaded"

// UploadedAttachments returns the attachment oids the LFS server at endpoint
// has confirmed holding, as RecordUploadedAttachments logged them. LFS
// objects are content-addressed and a server keeps what it accepted, so a
// confirmed oid never needs another batch round trip to that endpoint.
func (s *Store) UploadedAttachments(endpoint string) (map[string]bool, error) {
	f, err := os.Open(s.uploadedAttachmentsPath(endpoint))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read uploaded attachments: %w", err)
	}
	defer func() { _ = f.Close() }()
	uploaded := map[string]bool{}
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		if oid := lines.Text(); isOID(oid) {
			uploaded[oid] = true
		}
	}
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("read uploaded attachments: %w", err)
	}
	return uploaded, nil
}

// RecordUploadedAttachments appends oids to endpoint's confirmed log in one
// append-mode write, so concurrent syncs interleave whole lines.
func (s *Store) RecordUploadedAttachments(endpoint string, oids []string) error {
	if len(oids) == 0 {
		return nil
	}
	path := s.uploadedAttachmentsPath(endpoint)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record uploaded attachments: %w", err)
	}
	//nolint:gosec // G304: the path is this store's own upload log, named by a hash of the endpoint.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("record uploaded attachments: %w", err)
	}
	_, err = f.WriteString(strings.Join(oids, "\n") + "\n")
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("record uploaded attachments: %w", err)
	}
	return nil
}

func (s *Store) uploadedAttachmentsPath(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return filepath.Join(s.recordsCommonDir, uploadedAttachmentsSubdir, hex.EncodeToString(sum[:8]))
}

func isOID(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
