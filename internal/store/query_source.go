package store

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type queryMetadataSource struct {
	store      *Store
	kind       model.Kind
	binding    string
	tips       map[model.EntityID]model.SHA
	pinned     bool
	generation string
}

func queryMetadataIdentity(ctx context.Context, s *Store, kind model.Kind) (queryMetadataSource, error) {
	if err := ctx.Err(); err != nil {
		return queryMetadataSource{}, err
	}
	if err := s.CheckRecords(); err != nil {
		return queryMetadataSource{}, err
	}
	identity, err := executableIdentity()
	if err != nil {
		return queryMetadataSource{}, fmt.Errorf("identify executable for the %s query metadata: %w", kind, err)
	}
	return queryMetadataSource{
		store:   s,
		pinned:  s.pins != nil,
		kind:    kind,
		binding: s.listCacheName(kind, fmt.Appendf(nil, "metadata %d", queryMetadataVersion), foldCacheGeneration, identity),
	}, nil
}

func captureQueryMetadata(ctx context.Context, s *Store, kind model.Kind) (queryMetadataSource, error) {
	source, err := queryMetadataIdentity(ctx, s, kind)
	if err != nil {
		return queryMetadataSource{}, err
	}
	return source.capture(ctx)
}

func (q queryMetadataSource) currentGeneration(ctx context.Context) (string, error) {
	taken := time.Now()
	generation, err := q.store.Repo.RefGeneration(ctx, refs.Root(q.kind))
	if err != nil {
		return "", err
	}
	if !generation.SettledAt(taken) {
		return "", nil
	}
	return generation.Key(), nil
}

func (q queryMetadataSource) capture(ctx context.Context) (queryMetadataSource, error) {
	var pinned *Store
	if q.pinned {
		pinned = q.store.Pinned(maps.Clone(q.store.pins))
	} else {
		var err error
		q.generation, err = q.currentGeneration(ctx)
		if err != nil {
			return queryMetadataSource{}, err
		}
		pinned, err = q.store.PinnedKinds(ctx, q.kind)
		if err != nil {
			return queryMetadataSource{}, err
		}
		generation, err := q.currentGeneration(ctx)
		if err != nil {
			return queryMetadataSource{}, err
		}
		if generation != q.generation {
			q.generation = ""
		}
	}
	q.store = pinned
	q.tips = make(map[model.EntityID]model.SHA)
	root := refs.Root(q.kind)
	for _, entry := range directChildren(root, pinned.pins) {
		if err := ctx.Err(); err != nil {
			return queryMetadataSource{}, err
		}
		q.tips[model.EntityID(strings.TrimPrefix(entry.ref, root))] = entry.tip
	}
	return q, nil
}

func (q queryMetadataSource) load(ctx context.Context, id model.EntityID, tip model.SHA) (queryMetadata, *cachePublication, error) {
	if err := ctx.Err(); err != nil {
		return queryMetadata{}, nil, err
	}
	if q.tips[id] != tip || tip == "" {
		return queryMetadata{}, nil, fmt.Errorf("query metadata tip differs from captured %s %s", q.kind, id)
	}
	snapshot, chain, err := q.store.loadAt(ctx, tip)
	if err != nil {
		return queryMetadata{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return queryMetadata{}, nil, err
	}
	row := projectQueryMetadata(q.binding, tip, snapshot)
	if row.ID != id || row.Kind != q.kind {
		return queryMetadata{}, nil, fmt.Errorf("query metadata snapshot differs from captured %s %s", q.kind, id)
	}
	if len(chain) == 0 {
		return row, nil, nil
	}
	return row, foldChainPublication(tip, snapshot, chain), nil
}
