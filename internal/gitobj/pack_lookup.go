package gitobj

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
)

type indexedPack struct {
	ctx   context.Context
	tx    *sql.Tx
	hash  plumbing.Hash
	count int64
}

var _ idxfile.Index = indexedPack{}

func (p indexedPack) row(hash plumbing.Hash) (int64, uint32, error) {
	var offset int64
	var crc uint32
	err := p.tx.QueryRowContext(p.ctx, "SELECT offset, crc FROM objects WHERE pack = ? AND hash = ?", p.hash[:], hash[:]).Scan(&offset, &crc)
	if errors.Is(err, sql.ErrNoRows) {
		err = plumbing.ErrObjectNotFound
	}
	return offset, crc, err
}

func (p indexedPack) Contains(hash plumbing.Hash) (bool, error) {
	_, _, err := p.row(hash)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (p indexedPack) FindOffset(hash plumbing.Hash) (int64, error) {
	offset, _, err := p.row(hash)
	return offset, err
}

func (p indexedPack) FindCRC32(hash plumbing.Hash) (uint32, error) {
	_, crc, err := p.row(hash)
	return crc, err
}

func (p indexedPack) FindHash(offset int64) (plumbing.Hash, error) {
	var raw []byte
	err := p.tx.QueryRowContext(p.ctx, "SELECT hash FROM objects WHERE pack = ? AND offset = ?", p.hash[:], offset).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return plumbing.ZeroHash, plumbing.ErrObjectNotFound
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if len(raw) != len(plumbing.Hash{}) {
		return plumbing.ZeroHash, errors.New("invalid indexed pack offset")
	}
	var hash plumbing.Hash
	copy(hash[:], raw)
	return hash, nil
}

func (p indexedPack) Count() (int64, error) { return p.count, nil }

func (p indexedPack) Entries() (idxfile.EntryIter, error) { return p.entries("hash") }

func (p indexedPack) EntriesByOffset() (idxfile.EntryIter, error) { return p.entries("offset") }

func (p indexedPack) entries(order string) (idxfile.EntryIter, error) {
	rows, err := p.tx.QueryContext(p.ctx, "SELECT hash, offset, crc FROM objects WHERE pack = ? ORDER BY "+order, p.hash[:])
	if err != nil {
		return nil, err
	}
	return &indexedPackIter{rows: rows}, nil
}

type indexedPackIter struct{ rows *sql.Rows }

func (i *indexedPackIter) Next() (*idxfile.Entry, error) {
	if !i.rows.Next() {
		if err := i.rows.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var raw []byte
	entry := &idxfile.Entry{}
	if err := i.rows.Scan(&raw, &entry.Offset, &entry.CRC32); err != nil {
		return nil, err
	}
	if len(raw) != len(entry.Hash) {
		return nil, errors.New("invalid indexed pack entry")
	}
	copy(entry.Hash[:], raw)
	return entry, nil
}

func (i *indexedPackIter) Close() error { return i.rows.Close() }

func locateIndexedPack(ctx context.Context, tx *sql.Tx, hash plumbing.Hash) (plumbing.Hash, indexedPack, error) {
	var raw []byte
	var count int64
	err := tx.QueryRowContext(ctx, "SELECT objects.pack, packs.count FROM objects JOIN packs ON packs.hash = objects.pack WHERE objects.hash = ? ORDER BY objects.pack LIMIT 1", hash[:]).Scan(&raw, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return plumbing.ZeroHash, indexedPack{}, plumbing.ErrObjectNotFound
	}
	if err != nil {
		return plumbing.ZeroHash, indexedPack{}, err
	}
	if len(raw) != len(plumbing.Hash{}) || count < 0 {
		return plumbing.ZeroHash, indexedPack{}, errors.New("invalid object index route")
	}
	var pack plumbing.Hash
	copy(pack[:], raw)
	return pack, indexedPack{ctx: ctx, tx: tx, hash: pack, count: count}, nil
}
