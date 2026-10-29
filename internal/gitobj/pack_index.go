package gitobj

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/yasyf/cc-notes/internal/ccnhome"
)

const indexBusyWait = 25 * time.Millisecond

type packIdentity struct {
	hash  plumbing.Hash
	stamp []byte
}

type packIndex struct {
	dir   *dotgit.DotGit
	path  string
	ready bool
}

const packIndexSchema = `CREATE TABLE IF NOT EXISTS packs (hash BLOB PRIMARY KEY, stamp BLOB NOT NULL, count INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS objects (pack BLOB NOT NULL REFERENCES packs(hash) ON DELETE CASCADE, hash BLOB NOT NULL, offset INTEGER NOT NULL, crc INTEGER NOT NULL, PRIMARY KEY(pack, hash)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS objects_by_hash ON objects(hash, pack);
CREATE INDEX IF NOT EXISTS objects_by_offset ON objects(pack, offset);
CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value BLOB NOT NULL) WITHOUT ROWID;`

func newPackIndex(dir *dotgit.DotGit, commonDir string) (*packIndex, error) {
	repo, err := ccnhome.ForRepo(commonDir)
	if err != nil {
		return nil, err
	}
	dir = dotgit.NewWithOptions(dir.Fs(), dotgit.Options{AlternatesFS: osfs.New(string(filepath.Separator))})
	return &packIndex{dir: dir, path: filepath.Join(repo.Dir, "objects-v2.sqlite")}, nil
}

func (p *packIndex) identities(ctx context.Context) ([]packIdentity, error) {
	packs, err := p.dir.ObjectPacks()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(packs, func(a, b plumbing.Hash) int { return bytes.Compare(a[:], b[:]) })
	packs = slices.Compact(packs)
	identities := make([]packIdentity, 0, len(packs))
	for _, hash := range packs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stamp, err := p.identity(hash)
		if err != nil {
			return nil, err
		}
		identities = append(identities, packIdentity{hash: hash, stamp: stamp})
	}
	return identities, nil
}

func (p *packIndex) identity(hash plumbing.Hash) ([]byte, error) {
	stamp := make([]byte, 0, 64)
	for _, ext := range []string{"idx", "pack"} {
		info, err := p.dir.Fs().Stat(filepath.Join(packDir, "pack-"+hash.String()+"."+ext))
		if errors.Is(err, os.ErrNotExist) {
			return nil, dotgit.ErrPackfileNotFound
		}
		if err != nil {
			return nil, err
		}
		device, inode := FileID(info)
		stamp, err = binary.Append(stamp, binary.BigEndian, struct {
			Device, Inode uint64
			Size          int64
		}{device, inode, info.Size()})
		if err != nil {
			return nil, err
		}
		if ext == "idx" {
			ctime, _ := StatIdentity(info)
			stamp, err = binary.Append(stamp, binary.BigEndian, struct {
				Modified, Changed int64
			}{info.ModTime().UnixNano(), ctime})
			if err != nil {
				return nil, err
			}
		}
	}
	return stamp, nil
}

func (p *packIndex) open(ctx context.Context, readOnly bool) (*sql.DB, error) {
	mode, lock := "rwc", "immediate"
	if readOnly {
		if _, err := os.Stat(p.path); err != nil {
			return nil, err
		}
		mode, lock = "ro", "deferred"
	}
	location := url.URL{Scheme: "file", Path: p.path}
	query := location.Query()
	query.Set("mode", mode)
	query.Set("_txlock", lock)
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", indexBusyWait.Milliseconds()))
	query.Add("_pragma", "foreign_keys(ON)")
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func packGeneration(identities []packIdentity) []byte {
	hash := sha256.New()
	for _, identity := range identities {
		hash.Write(identity.hash[:])
		hash.Write(identity.stamp)
	}
	return hash.Sum(nil)
}

func (p *packIndex) prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.ready {
		return nil
	}
	identities, err := p.identities(ctx)
	if err != nil {
		return err
	}
	db, err := p.open(ctx, true)
	if err == nil {
		var generation []byte
		var schema string
		err = db.QueryRowContext(ctx, "SELECT name FROM sqlite_schema WHERE name = 'metadata' AND type = 'table'").Scan(&schema)
		if err == nil {
			err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'generation'").Scan(&generation)
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		err = errors.Join(err, db.Close())
		if err != nil {
			return err
		}
		if bytes.Equal(generation, packGeneration(identities)) {
			p.ready = true
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o750); err != nil {
		return err
	}
	db, err = p.open(ctx, false)
	if err != nil {
		return err
	}
	err = enterIndexWAL(ctx, db)
	if err == nil {
		err = p.reconcile(ctx, db)
	}
	err = errors.Join(err, db.Close())
	if err == nil {
		p.ready = true
	}
	return err
}

func enterIndexWAL(ctx context.Context, db *sql.DB) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL")
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
			return err
		}
		timer := time.NewTimer(indexBusyWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *packIndex) reconcile(ctx context.Context, db *sql.DB) (resultErr error) {
	tx, err := beginIndexWrite(ctx, db)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	identities, err := p.identities(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, packIndexSchema); err != nil {
		return err
	}
	var generation []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'generation'").Scan(&generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if bytes.Equal(generation, packGeneration(identities)) {
		return nil
	}
	wanted := make(map[plumbing.Hash]bool, len(identities))
	for _, identity := range identities {
		wanted[identity.hash] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT hash FROM packs")
	if err != nil {
		return err
	}
	var removed [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return errors.Join(err, rows.Close())
		}
		if len(raw) != len(plumbing.Hash{}) {
			return errors.Join(errors.New("invalid indexed pack hash"), rows.Close())
		}
		var hash plumbing.Hash
		copy(hash[:], raw)
		if !wanted[hash] {
			removed = append(removed, bytes.Clone(raw))
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, hash := range removed {
		if _, err := tx.ExecContext(ctx, "DELETE FROM packs WHERE hash = ?", hash); err != nil {
			return err
		}
	}
	for _, identity := range identities {
		var stamp []byte
		err := tx.QueryRowContext(ctx, "SELECT stamp FROM packs WHERE hash = ?", identity.hash[:]).Scan(&stamp)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if bytes.Equal(stamp, identity.stamp) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM packs WHERE hash = ?", identity.hash[:]); err != nil {
			return err
		}
		if err := p.add(ctx, tx, identity); err != nil {
			return err
		}
	}
	for _, identity := range identities {
		if err := ctx.Err(); err != nil {
			return err
		}
		stamp, err := p.identity(identity.hash)
		if err != nil {
			return err
		}
		if !bytes.Equal(stamp, identity.stamp) {
			return errors.New("pack generation changed while indexing")
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata(key, value) VALUES('generation', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", packGeneration(identities)); err != nil {
		return err
	}
	return tx.Commit()
}

func beginIndexWrite(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tx, err := db.BeginTx(ctx, nil)
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
			return tx, err
		}
	}
}

func (p *packIndex) add(ctx context.Context, tx *sql.Tx, identity packIdentity) (resultErr error) {
	file, err := p.dir.ObjectPackIdx(identity.hash)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	input, err := newIndexInput(contextFile{File: file, ctx: ctx})
	if err != nil {
		return err
	}
	index := idxfile.NewMemoryIndex()
	if err := idxfile.NewDecoder(input).Decode(index); err != nil {
		return err
	}
	if !bytes.Equal(index.PackfileChecksum[:], identity.hash[:]) {
		return fmt.Errorf("%w: packfile mismatch: target is %q not %q", idxfile.ErrMalformedIdxFile, plumbing.Hash(index.PackfileChecksum).String(), identity.hash.String())
	}
	count, err := index.Count()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO packs(hash, stamp, count) VALUES(?, ?, ?)", identity.hash[:], identity.stamp, count); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, "INSERT INTO objects(pack, hash, offset, crc) VALUES(?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, insert.Close()) }()
	iter, err := index.Entries()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, iter.Close()) }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if _, err := insert.ExecContext(ctx, identity.hash[:], entry.Hash[:], entry.Offset, entry.CRC32); err != nil {
			return err
		}
	}
	stamp, err := p.identity(identity.hash)
	if err != nil {
		return err
	}
	if !bytes.Equal(stamp, identity.stamp) {
		return fmt.Errorf("pack index changed while reading %s", identity.hash)
	}
	return nil
}

type indexInput struct {
	io.Reader
	size int64
}

type indexInputInfo struct {
	os.FileInfo
	size int64
}

func (info indexInputInfo) Size() int64 { return info.size }

func (input indexInput) Stat() (os.FileInfo, error) {
	return indexInputInfo{size: input.size}, nil
}

func newIndexInput(file io.ReadSeeker) (indexInput, error) {
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return indexInput{}, err
	}
	size, seekErr := file.Seek(0, io.SeekEnd)
	_, restoreErr := file.Seek(position, io.SeekStart)
	if err := errors.Join(seekErr, restoreErr); err != nil {
		return indexInput{}, err
	}
	return indexInput{Reader: file, size: size}, nil
}
