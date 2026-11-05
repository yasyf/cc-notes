package store

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
)

type queryVersion struct {
	born  uint64
	died  uint64
	value []byte
}

func (v queryVersion) visibleAt(generation uint64) bool {
	return v.born <= generation && (v.died == 0 || generation < v.died)
}

func decodeQueryVersions(data []byte) ([]queryVersion, error) {
	var versions []queryVersion
	for len(data) > 0 {
		if len(data) < 16 {
			return nil, fmt.Errorf("truncated query version: %w", managed.ErrReindex)
		}
		size, n := binary.Uvarint(data[16:])
		if n <= 0 {
			return nil, fmt.Errorf("truncated query version size: %w", managed.ErrReindex)
		}
		value := data[16+n:]
		if size > uint64(len(value)) {
			return nil, fmt.Errorf("truncated query version value: %w", managed.ErrReindex)
		}
		value = value[:size]
		versions = append(versions, queryVersion{born: binary.BigEndian.Uint64(data), died: binary.BigEndian.Uint64(data[8:]), value: value})
		data = data[16+n+len(value):]
	}
	return versions, nil
}

func encodeQueryVersions(versions []queryVersion) []byte {
	var data []byte
	for _, version := range versions {
		data = binary.BigEndian.AppendUint64(data, version.born)
		data = binary.BigEndian.AppendUint64(data, version.died)
		data = binary.AppendUvarint(data, uint64(len(version.value)))
		data = append(data, version.value...)
	}
	return data
}

func (r *retainedQuery) get(key string) ([]byte, error) {
	versions, err := decodeQueryVersions(r.versions.Get([]byte(key)))
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if version.visibleAt(r.generation.Number) {
			return version.value, nil
		}
	}
	return nil, nil
}

func (r *retainedQuery) prefix(prefix string, visit func(value []byte) error) error {
	return r.scan([]byte(prefix), func(_, value []byte) error { return visit(value) })
}

func (r *retainedQuery) each(visit func(key, value []byte) error) error {
	return r.scan(nil, visit)
}

func (r *retainedQuery) scan(prefix []byte, visit func(key, value []byte) error) error {
	cursor := r.versions.Cursor()
	for key, data := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, data = cursor.Next() {
		versions, err := decodeQueryVersions(data)
		if err != nil {
			return err
		}
		for _, version := range versions {
			if version.visibleAt(r.generation.Number) {
				if err := visit(key, version.value); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// advanceQueryGeneration applies mutations as the binding's next generation,
// then prunes the versions neither it nor the active generation can see. A
// generation whose mutations change nothing keeps its number.
func advanceQueryGeneration(ctx context.Context, binding *bolt.Bucket, mutations []sourcegraph.Mutation, added, removed uint64) error {
	versions, err := binding.CreateBucketIfNotExists(queryVersionsBucket)
	if err != nil {
		return err
	}
	deaths, err := binding.CreateBucketIfNotExists(queryDeathsBucket)
	if err != nil {
		return err
	}
	current, err := readQueryGeneration(binding, queryCurrentRoot)
	if err != nil {
		return err
	}
	next, changed := current.Number+1, false
	slices.SortFunc(mutations, func(a, b sourcegraph.Mutation) int { return cmp.Compare(a.Key, b.Key) })
	for _, mutation := range mutations {
		if err := ctx.Err(); err != nil {
			return err
		}
		applied, err := applyQueryVersion(versions, deaths, next, mutation)
		if err != nil {
			return err
		}
		changed = changed || applied
	}
	if changed {
		current.Number = next
	}
	current.Rows = current.Rows + added - removed
	if err := putQueryGeneration(binding, queryCurrentRoot, current); err != nil {
		return err
	}
	floor := current.Number
	if binding.Get(queryActiveRoot) != nil {
		active, err := readQueryGeneration(binding, queryActiveRoot)
		if err != nil {
			return err
		}
		floor = min(floor, active.Number)
	}
	return pruneQueryVersions(ctx, versions, deaths, floor)
}

func putQueryGeneration(binding *bolt.Bucket, key []byte, generation queryGeneration) error {
	data, err := json.Marshal(generation)
	if err != nil {
		return err
	}
	return binding.Put(key, data)
}

func applyQueryVersion(versions, deaths *bolt.Bucket, generation uint64, mutation sourcegraph.Mutation) (bool, error) {
	key := []byte(mutation.Key)
	list, err := decodeQueryVersions(versions.Get(key))
	if err != nil {
		return false, err
	}
	live := len(list) > 0 && list[len(list)-1].died == 0
	if mutation.Delete && !live {
		return false, nil
	}
	if !mutation.Delete && live && bytes.Equal(list[len(list)-1].value, mutation.Value) {
		return false, nil
	}
	if live {
		list[len(list)-1].died = generation
		if err := deaths.Put(queryDeathKey(generation, key), nil); err != nil {
			return false, err
		}
	}
	if !mutation.Delete {
		list = append(list, queryVersion{born: generation, value: mutation.Value})
	}
	return true, versions.Put(key, encodeQueryVersions(list))
}

func queryDeathKey(generation uint64, key []byte) []byte {
	return append(binary.BigEndian.AppendUint64(nil, generation), key...)
}

func pruneQueryVersions(ctx context.Context, versions, deaths *bolt.Bucket, floor uint64) error {
	cursor := deaths.Cursor()
	for death, _ := cursor.First(); death != nil && binary.BigEndian.Uint64(death) <= floor; death, _ = cursor.First() {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := bytes.Clone(death[8:])
		list, err := decodeQueryVersions(versions.Get(key))
		if err != nil {
			return err
		}
		list = slices.DeleteFunc(list, func(version queryVersion) bool { return version.died != 0 && version.died <= floor })
		if len(list) == 0 {
			err = versions.Delete(key)
		} else {
			err = versions.Put(key, encodeQueryVersions(list))
		}
		if err != nil {
			return err
		}
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	return nil
}
