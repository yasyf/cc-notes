package gitobj

import (
	"context"
	"errors"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

type objectStorage struct {
	*filesystem.Storage
	backend gitcmd.Git
	objects cache.Object
	pool    batchPool
	scope   *objectReadScope
}

func (s *objectStorage) Reindex() { s.scope.refresh = true }

func (s *objectStorage) EncodedObject(typ plumbing.ObjectType, hash plumbing.Hash) (plumbing.EncodedObject, error) {
	if err := s.scope.ctx.Err(); err != nil {
		return nil, err
	}
	obj, ok := s.objects.Get(hash)
	if !ok {
		var err error
		if obj, err = s.nativeRead(hash); err != nil {
			return nil, errors.Join(err, s.scope.ctx.Err())
		}
	}
	if typ != plumbing.AnyObject && obj.Type() != typ {
		return nil, plumbing.ErrObjectNotFound
	}
	return obj, nil
}

// nativeRead resolves hash through the pooled git reader, which consults loose
// objects, packs, and alternates in one lookup.
func (s *objectStorage) nativeRead(hash plumbing.Hash) (plumbing.EncodedObject, error) {
	if s.scope.refresh {
		if err := s.scope.closeBatches(); err != nil {
			return nil, err
		}
		s.scope.refresh = false
	}
	batch, err := s.scope.batch(s.backend)
	if err != nil {
		return nil, err
	}
	info, err := batch.Info(hash)
	if err != nil {
		return nil, err
	}
	if info.Size > 1<<16 {
		return &nativeObject{info: info, scope: s.scope, backend: s.backend}, nil
	}
	reader, err := batch.Contents(info)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	memory := &plumbing.MemoryObject{}
	memory.SetType(info.Type)
	memory.SetSize(info.Size)
	writer, err := memory.Writer()
	if err != nil {
		return nil, err
	}
	_, readErr := io.Copy(writer, reader)
	if err := errors.Join(readErr, writer.Close()); err != nil {
		return nil, err
	}
	s.objects.Put(memory)
	return memory, nil
}

type nativeObject struct {
	info    gitcmd.ObjectInfo
	scope   *objectReadScope
	backend gitcmd.Git
}

func (o *nativeObject) Hash() plumbing.Hash         { return o.info.Hash }
func (o *nativeObject) Type() plumbing.ObjectType   { return o.info.Type }
func (o *nativeObject) Size() int64                 { return o.info.Size }
func (o *nativeObject) SetType(plumbing.ObjectType) {}
func (o *nativeObject) SetSize(int64)               {}
func (o *nativeObject) Writer() (io.WriteCloser, error) {
	return nil, errors.New("native object is read-only")
}

func (o *nativeObject) Reader() (io.ReadCloser, error) {
	batch, err := o.scope.batch(o.backend)
	if err != nil {
		return nil, err
	}
	return batch.Contents(o.info)
}

func (r *Repo) beginRead(ctx context.Context) {
	r.storage.scope = &objectReadScope{ctx: ctx, pool: &r.storage.pool}
}

func (r *Repo) endRead(resultErr *error) {
	*resultErr = errors.Join(*resultErr, r.storage.scope.release())
	r.storage.scope.closed = true
	r.storage.scope = nil
}
