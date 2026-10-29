package gitobj

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

type objectStorage struct {
	*filesystem.Storage
	index   *packIndex
	objects cache.Object
	scope   *objectReadScope
}

func (s *objectStorage) Reindex() {
	s.index.ready = false
}

func (s *objectStorage) EncodedObject(typ plumbing.ObjectType, hash plumbing.Hash) (_ plumbing.EncodedObject, resultErr error) {
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, plumbing.ErrObjectNotFound) {
			s.index.ready = false
		}
	}()
	if err := s.scope.ctx.Err(); err != nil {
		return nil, err
	}
	first, second := s.looseObject, s.packedObject
	if s.index.ready {
		first, second = second, first
	}
	obj, err := first(hash)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		obj, err = second(hash)
	}
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		if !s.scope.alternatesLoaded {
			s.scope.alternatesLoaded = true
			alternates, alternateErr := s.index.dir.Alternates()
			if alternateErr == nil {
				for _, alternate := range alternates {
					index, indexErr := newPackIndex(dotgit.New(gitDirFS{alternate.Fs()}), alternate.Fs().Root())
					if indexErr != nil {
						continue
					}
					s.scope.alternates = append(s.scope.alternates, &objectStorage{index: index, objects: s.objects, scope: &objectReadScope{ctx: s.scope.ctx}})
				}
			}
		}
		for _, storage := range s.scope.alternates {
			alternateObject, alternateErr := storage.alternateObject(typ, hash)
			if alternateErr == nil {
				return alternateObject, nil
			}
		}
	}
	if contextErr := s.scope.ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, err
	}
	if typ != plumbing.AnyObject && obj.Type() != typ {
		return nil, plumbing.ErrObjectNotFound
	}
	return obj, nil
}

func (s *objectStorage) alternateObject(typ plumbing.ObjectType, hash plumbing.Hash) (plumbing.EncodedObject, error) {
	obj, err := s.EncodedObject(typ, hash)
	if !staleIndex(err) || s.scope.tx == nil {
		return obj, err
	}
	identities, generationErr := s.index.identities(s.scope.ctx)
	if generationErr != nil {
		return nil, generationErr
	}
	var generation []byte
	if generationErr := s.scope.tx.QueryRowContext(s.scope.ctx, "SELECT value FROM metadata WHERE key = 'generation'").Scan(&generation); generationErr != nil {
		return nil, generationErr
	}
	if bytes.Equal(generation, packGeneration(identities)) {
		return obj, err
	}
	s.Reindex()
	return s.EncodedObject(typ, hash)
}

func (s *objectStorage) looseObject(hash plumbing.Hash) (obj plumbing.EncodedObject, resultErr error) {
	file, err := s.index.dir.Object(hash)
	if errors.Is(err, os.ErrNotExist) {
		return nil, plumbing.ErrObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	if cached, ok := s.objects.Get(hash); ok {
		return cached, nil
	}
	reader, err := objfile.NewReader(contextFile{File: file, ctx: s.scope.ctx})
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	typ, size, err := reader.Header()
	if err != nil {
		return nil, err
	}
	memory := &plumbing.MemoryObject{}
	memory.SetType(typ)
	memory.SetSize(size)
	writer, err := memory.Writer()
	if err != nil {
		return nil, err
	}
	_, copyErr := io.Copy(writer, reader)
	if err := errors.Join(copyErr, writer.Close()); err != nil {
		return nil, err
	}
	s.objects.Put(memory)
	return memory, nil
}

func (s *objectStorage) packedObject(hash plumbing.Hash) (plumbing.EncodedObject, error) {
	if !s.index.ready && s.scope.tx != nil {
		if err := s.scope.closeIndex(); err != nil {
			return nil, err
		}
	}
	if err := s.index.prepare(s.scope.ctx); err != nil {
		return nil, err
	}
	if s.scope.tx == nil {
		db, err := s.index.open(s.scope.ctx, true)
		if err != nil {
			return nil, err
		}
		s.scope.db = db
		tx, err := db.BeginTx(s.scope.ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		s.scope.tx = tx
		var generation []byte
		if err := tx.QueryRowContext(s.scope.ctx, "SELECT value FROM metadata WHERE key = 'generation'").Scan(&generation); err != nil {
			return nil, err
		}
		if len(generation) != 32 {
			return nil, errors.New("object index generation is incomplete")
		}
	}
	pack, index, err := locateIndexedPack(s.scope.ctx, s.scope.tx, hash)
	if err != nil {
		return nil, err
	}
	reader, err := s.scope.pack(s, pack, index)
	if err != nil {
		return nil, err
	}
	return reader.Get(hash)
}

func (r *Repo) beginRead(ctx context.Context) {
	r.storage.scope = &objectReadScope{ctx: ctx}
}

func (r *Repo) endRead(resultErr *error) {
	closeErr := r.storage.scope.close()
	*resultErr = errors.Join(*resultErr, closeErr)
	if closeErr != nil {
		r.storage.index.ready = false
	}
	r.storage.scope = nil
}
