package gitobj

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"slices"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

type scopedPack struct {
	hash   plumbing.Hash
	reader *packfile.Packfile
}

type objectReadScope struct {
	ctx              context.Context
	db               *sql.DB
	tx               *sql.Tx
	packs            []scopedPack
	alternates       []*objectStorage
	alternatesLoaded bool
}

func (s *objectReadScope) pack(storage *objectStorage, hash plumbing.Hash, index indexedPack) (*packfile.Packfile, error) {
	for i, pack := range s.packs {
		if pack.hash == hash {
			copy(s.packs[1:i+1], s.packs[:i])
			s.packs[0] = pack
			return pack.reader, nil
		}
	}
	if len(s.packs) == openPacks {
		last := len(s.packs) - 1
		if err := s.packs[last].reader.Close(); err != nil {
			return nil, err
		}
		s.packs = s.packs[:last]
	}
	file, err := storage.index.dir.ObjectPack(hash)
	if err != nil {
		return nil, err
	}
	reader := packfile.NewPackfileWithCache(index, contextFS{Filesystem: storage.index.dir.Fs(), ctx: s.ctx}, contextFile{File: file, ctx: s.ctx}, storage.objects, 0)
	s.packs = slices.Insert(s.packs, 0, scopedPack{hash: hash, reader: reader})
	return reader, nil
}

func (s *objectReadScope) closeIndex() error {
	var result error
	for _, pack := range s.packs {
		result = errors.Join(result, pack.reader.Close())
	}
	s.packs = nil
	if s.tx != nil {
		if err := s.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			result = errors.Join(result, err)
		}
		s.tx = nil
	}
	if s.db != nil {
		result = errors.Join(result, s.db.Close())
		s.db = nil
	}
	return result
}

func (s *objectReadScope) close() error {
	result := s.closeIndex()
	for _, alternate := range s.alternates {
		result = errors.Join(result, alternate.scope.close())
	}
	return result
}

type contextFile struct {
	billy.File
	ctx context.Context
}

func (f contextFile) Stat() (os.FileInfo, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	return f.File.(fileStater).Stat()
}

type contextFS struct {
	billy.Filesystem
	ctx context.Context
}

func (fs contextFS) Open(path string) (billy.File, error) {
	if err := fs.ctx.Err(); err != nil {
		return nil, err
	}
	file, err := fs.Filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	return contextFile{File: file, ctx: fs.ctx}, nil
}

func (f contextFile) Read(data []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(data)
}

func (f contextFile) ReadAt(data []byte, offset int64) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.ReadAt(data, offset)
}
