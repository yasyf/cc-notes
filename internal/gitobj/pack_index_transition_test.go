package gitobj

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestNativePackedReadsIgnoreLegacyIndexWriterAfterRepack(t *testing.T) {
	dir, commonDir, first := indexedCommitFixture(t)
	home, err := ccnhome.ForRepo(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home.Dir, "objects-v2.sqlite")
	db, err := sql.Open("sqlite", path+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE retained(value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	}()
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO retained(value) VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-adq")
	gittest.Git(t, dir, "multi-pack-index", "write")
	repo := openPackedRepo(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	verdict, err := repo.Ancestry(ctx, model.SHA(first.String()), second)
	if err != nil || verdict != Ancestor {
		t.Fatalf("read beside obsolete SQLite writer = %v, %v", verdict, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy object index changed: %v", err)
	}
	t.Logf("repacked read beside held legacy writer elapsed=%v; legacy index bytes unchanged=%d", time.Since(started), len(before))
}
