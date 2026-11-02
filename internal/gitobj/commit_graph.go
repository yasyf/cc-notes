package gitobj

import (
	"bufio"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/go-git/go-git/v5/plumbing"
	commitgraph "github.com/go-git/go-git/v5/plumbing/format/commitgraph/v2"
)

// unknownGeneration ranks a commit the commit-graph does not hold above every
// indexed one, the way git treats GENERATION_NUMBER_INFINITY: it is newer
// than the graph, so nothing proves it cannot reach an ancestor.
const unknownGeneration = math.MaxUint64

// commitGraph serves parents and topological levels from the first object
// directory, own then alternates, that holds a commit-graph, as git loads it.
// Graph files are mapped read-only; git replaces them by rename and never
// rewrites one in place, so a mapping stays valid after a rewrite. The
// mappings are unmapped when the graph is replaced, or, for a Repo dropped
// without that (a retained store reopening), once the graph is unreachable.
type commitGraph struct {
	index    commitgraph.Index
	stamp    []pathStamp
	mappings [][]byte
	cleanup  runtime.Cleanup
}

type mappedGraph struct{ data []byte }

func (m mappedGraph) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (mappedGraph) Close() error { return nil }

// refreshCommitGraph reopens the commit-graph when any candidate file moved
// since it was opened, and drops it in a shallow repository, whose grafts the
// graph does not record. Callers hold mu.
func (r *Repo) refreshCommitGraph() error {
	if len(r.shallow) > 0 {
		return r.closeCommitGraph()
	}
	paths := commitGraphCandidates(filepath.Join(r.refDirectory, "objects"))
	stamp := make([]pathStamp, len(paths))
	for i, path := range paths {
		stamp[i] = statStamp(path)
	}
	if r.graph != nil && slices.Equal(r.graph.stamp, stamp) {
		return nil
	}
	if err := r.closeCommitGraph(); err != nil {
		return err
	}
	index, mappings, err := openCommitGraphIndex(paths)
	if err != nil || index == nil {
		r.graph = &commitGraph{stamp: stamp}
		return nil
	}
	graph := &commitGraph{index: index, stamp: stamp, mappings: mappings}
	graph.cleanup = runtime.AddCleanup(graph, func(mappings [][]byte) { _ = unmapGraphs(mappings) }, mappings)
	r.graph = graph
	return nil
}

// closeCommitGraph drops the open graph and the frontiers expanded under it,
// whose generations a replaced graph, or none, would contradict. Callers hold
// mu.
func (r *Repo) closeCommitGraph() error {
	if r.graph == nil {
		return nil
	}
	graph := r.graph
	r.graph = nil
	clear(r.reaches)
	r.reachOrder = r.reachOrder[:0]
	if len(graph.mappings) == 0 {
		return nil
	}
	graph.cleanup.Stop()
	return unmapGraphs(graph.mappings)
}

func unmapGraphs(mappings [][]byte) error {
	var err error
	for _, data := range mappings {
		err = errors.Join(err, syscall.Munmap(data))
	}
	return err
}

// graphParents returns hash's parents from the commit-graph, or ok=false when
// no graph is open or it does not hold hash. Callers hold mu.
func (r *Repo) graphParents(hash plumbing.Hash) (parents []plumbing.Hash, ok bool) {
	data, ok := r.graphCommit(hash)
	if !ok {
		return nil, false
	}
	return data.ParentHashes, true
}

// generation returns hash's topological level, or unknownGeneration when the
// commit-graph does not hold it. Callers hold mu.
func (r *Repo) generation(hash plumbing.Hash) uint64 {
	data, ok := r.graphCommit(hash)
	if !ok || data.Generation == 0 {
		return unknownGeneration
	}
	return data.Generation
}

func (r *Repo) graphCommit(hash plumbing.Hash) (*commitgraph.CommitData, bool) {
	if r.graph == nil || r.graph.index == nil {
		return nil, false
	}
	i, err := r.graph.index.GetIndexByHash(hash)
	if err != nil {
		return nil, false
	}
	data, err := r.graph.index.GetCommitDataByIndex(i)
	if err != nil {
		return nil, false
	}
	return data, true
}

// commitGraphCandidates lists, per object directory in git's lookup order,
// the single-file graph and the split chain's manifest.
func commitGraphCandidates(objects string) []string {
	dirs := append([]string{objects}, alternateObjectDirs(objects)...)
	paths := make([]string, 0, 2*len(dirs))
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(dir, "info", "commit-graph"), filepath.Join(dir, "info", "commit-graphs", "commit-graph-chain"))
	}
	return paths
}

func alternateObjectDirs(objects string) []string {
	f, err := os.Open(filepath.Join(objects, "info", "alternates")) //nolint:gosec // G304: the alternates file of the repository this handle opened.
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var dirs []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(objects, line)
		}
		dirs = append(dirs, filepath.Clean(line))
	}
	return dirs
}

// openCommitGraphIndex opens the first candidate graph present: a single file,
// or a chain whose layers stack base first, with the mappings it holds. It
// returns a nil index when none exists.
func openCommitGraphIndex(paths []string) (commitgraph.Index, [][]byte, error) {
	for i := 0; i < len(paths); i += 2 {
		var mappings [][]byte
		index, err := mapGraph(paths[i], nil, &mappings)
		if errors.Is(err, os.ErrNotExist) {
			index, err = openGraphChain(paths[i+1], &mappings)
		}
		if err == nil {
			return index, mappings, nil
		}
		if unmapErr := unmapGraphs(mappings); unmapErr != nil {
			return nil, nil, errors.Join(err, unmapErr)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
	}
	return nil, nil, nil
}

func openGraphChain(chainPath string, mappings *[][]byte) (commitgraph.Index, error) {
	f, err := os.Open(chainPath) //nolint:gosec // G304: the commit-graph chain of the repository this handle opened.
	if err != nil {
		return nil, err
	}
	layers, err := commitgraph.OpenChainFile(f)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	var index commitgraph.Index
	for _, layer := range layers {
		if index, err = mapGraph(filepath.Join(filepath.Dir(chainPath), "graph-"+layer+".graph"), index, mappings); err != nil {
			return nil, err
		}
	}
	return index, nil
}

func mapGraph(path string, parent commitgraph.Index, mappings *[][]byte) (commitgraph.Index, error) {
	f, err := os.Open(path) //nolint:gosec // G304: a commit-graph layer of the repository this handle opened.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 || info.Size() > math.MaxInt32 {
		return nil, errors.New("commit-graph size out of range")
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	*mappings = append(*mappings, data)
	return commitgraph.OpenFileIndexWithParent(mappedGraph{data: data}, parent)
}

func statStamp(path string) pathStamp {
	info, err := os.Stat(path)
	if err != nil {
		return pathStamp{}
	}
	ctime, inode := StatIdentity(info)
	return pathStamp{exists: true, size: info.Size(), modNano: info.ModTime().UnixNano(), ctimeNano: ctime, inode: inode}
}
