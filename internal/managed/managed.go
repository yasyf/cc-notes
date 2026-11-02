// Package managed owns the local authority used by managed cc-notes writers.
package managed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Format identifies the managed authority schema and projection contract.
const Format = "cc-notes-managed-v3"

// QueryGenerations identifies the required immutable metadata storage.
const QueryGenerations = "query-generations-v1"

// QueryObjects identifies immutable graph objects shared by metadata generations.
const QueryObjects = "objects"

// QueryObjectRefs counts immutable graph edges and retained generation roots.
const QueryObjectRefs = "object-refs"

// ErrReindex requires an explicit import before ordinary managed operations.
var ErrReindex = errors.New("managed authority unavailable; run cc-notes reindex")

// Path names the shared records authority database.
func Path(commonDir string) string {
	return filepath.Join(commonDir, "cc-notes-managed", "authority-v1.db")
}

// Lock retains the stable authority lock until its returned file is closed.
func Lock(ctx context.Context, commonDir string, exclusive bool) (*os.File, error) {
	path := Path(commonDir) + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, errors.Join(ErrReindex, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: The lock path is a fixed child of the bound records common directory.
	if err != nil {
		return nil, errors.Join(ErrReindex, err)
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, file.Close())
		}
		err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(err, file.Close())
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// Initialize creates the authority for a repository known to contain no records.
func Initialize(ctx context.Context, commonDir string) (err error) {
	lock, err := Lock(ctx, commonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if _, err := os.Stat(Path(commonDir)); err == nil {
		return fmt.Errorf("authority already exists: %s", Path(commonDir))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := bolt.Open(Path(commonDir), 0o600, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucket([]byte(Format))
		if err != nil {
			return err
		}
		if _, err := root.CreateBucket([]byte("refs")); err != nil {
			return err
		}
		generations, err := tx.CreateBucket([]byte(QueryGenerations))
		if err != nil {
			return err
		}
		if _, err = generations.CreateBucket([]byte(QueryObjects)); err != nil {
			return err
		}
		_, err = generations.CreateBucket([]byte(QueryObjectRefs))
		return err
	})
}
