package store

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type queryMetadataSource struct {
	store   *Store
	kind    model.Kind
	binding string
	tips    map[model.EntityID]model.SHA
}

func captureQueryMetadata(ctx context.Context, s *Store, kind model.Kind) (queryMetadataSource, error) {
	if err := ctx.Err(); err != nil {
		return queryMetadataSource{}, err
	}
	if err := s.CheckRecords(); err != nil {
		return queryMetadataSource{}, err
	}
	var pinned *Store
	if s.pins != nil {
		pinned = s.Pinned(maps.Clone(s.pins))
	} else {
		var err error
		pinned, err = s.PinnedKinds(ctx, kind)
		if err != nil {
			return queryMetadataSource{}, err
		}
	}
	identity, err := executableIdentity()
	if err != nil {
		return queryMetadataSource{}, fmt.Errorf("identify executable for the %s query metadata: %w", kind, err)
	}
	source := queryMetadataSource{
		store:   pinned,
		kind:    kind,
		binding: pinned.listCacheName(kind, fmt.Appendf(nil, "metadata %d", queryMetadataVersion), foldCacheGeneration, identity),
		tips:    make(map[model.EntityID]model.SHA),
	}
	root := refs.Root(kind)
	for _, entry := range directChildren(root, pinned.pins) {
		if err := ctx.Err(); err != nil {
			return queryMetadataSource{}, err
		}
		source.tips[model.EntityID(strings.TrimPrefix(entry.ref, root))] = entry.tip
	}
	return source, nil
}

func (q queryMetadataSource) load(ctx context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
	if err := ctx.Err(); err != nil {
		return queryMetadata{}, err
	}
	if q.tips[id] != tip || tip == "" {
		return queryMetadata{}, fmt.Errorf("query metadata tip differs from captured %s %s", q.kind, id)
	}
	snapshot, err := q.store.LoadAt(ctx, tip)
	if err != nil {
		return queryMetadata{}, err
	}
	if err := ctx.Err(); err != nil {
		return queryMetadata{}, err
	}
	row := projectQueryMetadata(q.binding, tip, snapshot)
	if row.ID != id || row.Kind != q.kind {
		return queryMetadata{}, fmt.Errorf("query metadata snapshot differs from captured %s %s", q.kind, id)
	}
	return row, nil
}
