package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/stale"
)

// The files a snapshot directory holds. The data parts are JSONL — one
// complete record per line, in the order the fold produced — so a refresh
// shows up in review as the lines it added, removed, and rewrote.
const (
	manifestFile  = "manifest.json"
	validatedFile = "validated.json"
	corpusPart    = "corpus.jsonl"
	nodesPart     = "nodes.jsonl"
	edgesPart     = "edges.jsonl"
	eventsPart    = "events.jsonl"
	stalenessPart = "staleness.jsonl"
)

// Errors returned when a snapshot on disk cannot be trusted.
var (
	ErrUnsupportedVersion = errors.New("unsupported snapshot version")
	ErrRepoMismatch       = errors.New("snapshot was captured from another repository")
	ErrPartMissing        = errors.New("snapshot manifest names no such part")
	ErrPartDigest         = errors.New("snapshot part does not match its manifest digest")
)

// Dir is the snapshot directory of repo under root: one directory per
// repository, named after it.
func Dir(root, repo string) string {
	return filepath.Join(root, filepath.Base(filepath.Clean(repo)))
}

// Write dumps the corpus under root and returns the manifest it wrote, whose
// part digests every later load verifies. It clears any validation stamp the
// directory held: a fresh capture is unvalidated by definition, which is what
// stops a refreshed corpus from inheriting the previous corpus's gold labels.
func Write(root string, c Corpus) (Manifest, error) {
	dir := Dir(root, c.Manifest.Repo)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Manifest{}, fmt.Errorf("create %s: %w", dir, err)
	}
	parts, bodies, err := encode(c)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Remove(filepath.Join(dir, validatedFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, fmt.Errorf("clear %s: %w", validatedFile, err)
	}
	for i, p := range parts {
		if err := os.WriteFile(filepath.Join(dir, p.Name), bodies[i], 0o600); err != nil {
			return Manifest{}, fmt.Errorf("write %s: %w", p.Name, err)
		}
	}
	m := c.Manifest
	m.Parts = parts
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("encode %s: %w", manifestFile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFile), append(body, '\n'), 0o600); err != nil {
		return Manifest{}, fmt.Errorf("write %s: %w", manifestFile, err)
	}
	return m, nil
}

// Load reads the snapshot of repo under root, verifying every data part
// against its manifest digest. It does not check gold labels; Open does.
func Load(root, repo string) (Corpus, error) {
	dir := Dir(root, repo)
	m, err := readManifest(dir)
	if err != nil {
		return Corpus{}, err
	}
	if m.Repo != filepath.Clean(repo) {
		return Corpus{}, fmt.Errorf("%w: %s holds %s, want %s", ErrRepoMismatch, dir, m.Repo, repo)
	}
	entities, err := readPart[eval.Entity](dir, m, corpusPart)
	if err != nil {
		return Corpus{}, err
	}
	nodes, err := readPart[kg.Node](dir, m, nodesPart)
	if err != nil {
		return Corpus{}, err
	}
	edges, err := readPart[kg.Edge](dir, m, edgesPart)
	if err != nil {
		return Corpus{}, err
	}
	events, err := readPart[kg.Event](dir, m, eventsPart)
	if err != nil {
		return Corpus{}, err
	}
	assessments, err := readPart[stale.Assessment](dir, m, stalenessPart)
	if err != nil {
		return Corpus{}, err
	}
	return Corpus{
		Manifest: m,
		Entities: entities,
		Graph: &kg.Graph{
			Source:  m.GraphSource,
			BuiltAt: m.GraphBuiltAt,
			Nodes:   nodes,
			Edges:   edges,
			Events:  events,
		},
		Assessments: assessments,
	}, nil
}

// Open loads the snapshot of repo under root and refuses one whose gold labels
// have not been re-validated against the question file at questions since the
// snapshot was written.
func Open(root, repo, questions string) (Corpus, error) {
	c, err := Load(root, repo)
	if err != nil {
		return Corpus{}, err
	}
	if err := checkStamp(Dir(root, repo), questions); err != nil {
		return Corpus{}, err
	}
	return c, nil
}

func readManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, manifestFile)
	body, err := os.ReadFile(path) //nolint:gosec // G304: the snapshot directory is the operator-supplied path this harness reads.
	if err != nil {
		return Manifest{}, fmt.Errorf("read snapshot manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if m.Version != Version {
		return Manifest{}, fmt.Errorf("%w: %s is %d (want %d)", ErrUnsupportedVersion, path, m.Version, Version)
	}
	return m, nil
}

// encode renders every data part and its digest, in manifest order.
func encode(c Corpus) ([]Part, [][]byte, error) {
	corpus, corpusBody, err := encodePart(corpusPart, c.Entities)
	if err != nil {
		return nil, nil, err
	}
	nodes, nodesBody, err := encodePart(nodesPart, c.Graph.Nodes)
	if err != nil {
		return nil, nil, err
	}
	edges, edgesBody, err := encodePart(edgesPart, c.Graph.Edges)
	if err != nil {
		return nil, nil, err
	}
	events, eventsBody, err := encodePart(eventsPart, c.Graph.Events)
	if err != nil {
		return nil, nil, err
	}
	staleness, stalenessBody, err := encodePart(stalenessPart, c.Assessments)
	if err != nil {
		return nil, nil, err
	}
	return []Part{corpus, nodes, edges, events, staleness},
		[][]byte{corpusBody, nodesBody, edgesBody, eventsBody, stalenessBody}, nil
}

// encodePart renders one record slice as JSONL. HTML escaping is off: the
// bodies are prose, and < in every angle bracket makes a diff unreadable.
func encodePart[T any](name string, rows []T) (Part, []byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return Part{}, nil, fmt.Errorf("encode %s: %w", name, err)
		}
	}
	return Part{Name: name, Lines: len(rows), SHA256: digest(b.Bytes())}, b.Bytes(), nil
}

func readPart[T any](dir string, m Manifest, name string) ([]T, error) {
	i := slices.IndexFunc(m.Parts, func(p Part) bool { return p.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("%w: %s", ErrPartMissing, name)
	}
	path := filepath.Join(dir, name)
	body, err := os.ReadFile(path) //nolint:gosec // G304: the snapshot directory is the operator-supplied path this harness reads.
	if err != nil {
		return nil, fmt.Errorf("read snapshot part: %w", err)
	}
	if got := digest(body); got != m.Parts[i].SHA256 {
		return nil, fmt.Errorf("%w: %s is %s, manifest says %s", ErrPartDigest, path, got, m.Parts[i].SHA256)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var out []T
	for {
		var row T
		if err := dec.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		out = append(out, row)
	}
	return out, nil
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func digestFile(path string) (string, error) {
	body, err := os.ReadFile(path) //nolint:gosec // G304: the question set and manifest are the operator-supplied paths this harness reads.
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return digest(body), nil
}
