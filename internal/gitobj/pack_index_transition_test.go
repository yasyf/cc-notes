package gitobj

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitialPackIndexJournalTransitionHonorsCancellation(t *testing.T) {
	_, commonDir, _ := indexedCommitFixture(t)
	index, _ := testPackIndex(t, commonDir)
	if err := os.MkdirAll(filepath.Dir(index.path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", index.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE initial_reader(value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error(err)
		}
	})
	var count int
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM initial_reader").Scan(&count); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := index.prepare(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initial journal transition beside a live reader = %v, want caller deadline", err)
	}
	if index.ready {
		t.Fatal("blocked initial journal transition published a generation")
	}
}
