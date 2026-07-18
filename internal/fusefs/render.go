// Package fusefs is the pure translation layer between entities and their
// filesystem representations: notes render as markdown with YAML
// frontmatter, tasks as the CLI's --json document pretty-printed. Render,
// parse, diff, and the path model are plain Go with no cgofuse or git
// dependency, so the package compiles in the default CGO_ENABLED=0 build;
// the mount machinery layers on top behind the fuse build tag.
package fusefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
)

// delimiter fences the YAML frontmatter of a rendered note.
const delimiter = "---\n"

var (
	// ErrParse reports file content that does not decode into an entity
	// document: garbage YAML or JSON, an unknown key, a missing frontmatter
	// delimiter, or an invalid enum value.
	ErrParse = errors.New("unparseable document")
	// ErrImmutableField reports an edit to a field the filesystem cannot
	// change; the wrapped detail names the field.
	ErrImmutableField = errors.New("immutable field")
)

// anchorKinds maps each note document key to its anchor kind, in render
// order.
var anchorKinds = []struct {
	key  string
	kind model.AnchorKind
}{
	{"commits", model.AnchorCommit},
	{"paths", model.AnchorPath},
	{"dirs", model.AnchorDir},
	{"branches", model.AnchorBranch},
}

// fmKey is one frontmatter key in a kind's ordered key list: node builds its
// yaml value from the snapshot, and keep (nil means always) decides whether the
// key renders at all. The slice order is the byte contract.
type fmKey[S any] struct {
	key  string
	node func(S) *yaml.Node
	keep func(S) bool
}

// renderFrontmatter encodes snap's frontmatter into a fresh buffer — the leading
// delimiter, the ordered keys whose keep passes, then the closing delimiter —
// which the caller appends the body to. SetIndent(2) and the node styles match
// the hand-rolled per-kind renderers byte for byte.
func renderFrontmatter[S any](snap S, keys []fmKey[S]) *bytes.Buffer {
	fm := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		if k.keep == nil || k.keep(snap) {
			fm.Content = append(fm.Content, scalarNode(k.key), k.node(snap))
		}
	}
	return encodeFrontmatter(fm)
}

// encodeFrontmatter writes fm between delimiters into a fresh buffer at indent
// 2 — the frontmatter block the renderers and the new-file templates share.
func encodeFrontmatter(fm *yaml.Node) *bytes.Buffer {
	var buf bytes.Buffer
	buf.WriteString(delimiter)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		panic(fmt.Sprintf("fusefs: encode frontmatter: %v", err))
	}
	if err := enc.Close(); err != nil {
		panic(fmt.Sprintf("fusefs: close frontmatter encoder: %v", err))
	}
	buf.WriteString(delimiter)
	return &buf
}

// anchorKeys builds the ordered anchor frontmatter keys for a kind, each
// rendered only when it carries values, in anchorKinds order.
func anchorKeys[S any](anchors func(S) []model.Anchor) []fmKey[S] {
	keys := make([]fmKey[S], len(anchorKinds))
	for i, ak := range anchorKinds {
		keys[i] = fmKey[S]{
			key:  ak.key,
			node: func(s S) *yaml.Node { return flowNode(render.AnchorValues(anchors(s), ak.kind)) },
			keep: func(s S) bool { return len(render.AnchorValues(anchors(s), ak.kind)) > 0 },
		}
	}
	return keys
}

// Field is one optional document field. Set reports that the key was
// present, Null that its value was an explicit JSON or YAML null. An unset
// field diffs as untouched — only keys the editor actually wrote can
// produce ops, which is what lets concurrent edits to different fields
// merge instead of conflicting.
type Field[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// UnmarshalJSON records presence, then decodes non-null values rejecting
// unknown nested keys.
func (f *Field[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(data, []byte("null")) {
		f.Null = true
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(&f.Value)
}

// UnmarshalYAML records presence, then decodes non-null values.
func (f *Field[T]) UnmarshalYAML(value *yaml.Node) error {
	f.Set = true
	if value.Tag == "!!null" {
		f.Null = true
		return nil
	}
	return value.Decode(&f.Value)
}

// ParsedWitness is one entry in a note document's witness sequence: the anchor
// kind and value plus the git oid recorded for it at verify time.
type ParsedWitness struct {
	Kind  string `yaml:"kind"`
	Value string `yaml:"value"`
	OID   string `yaml:"oid"`
}

// ParsedComment is one comment in a task document. TS stays the rendered
// RFC3339 string so an echoed comment compares exactly.
type ParsedComment struct {
	Author string `json:"author"`
	TS     string `json:"ts"`
	Body   string `json:"body"`
}

// ParsedCriterion is one acceptance criterion in a task document. It mirrors the
// CLI's criterionDTO and adds the stored evidence note, which marshals omitempty
// so a note-less criterion keeps its CLI-compatible bytes.
type ParsedCriterion struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Script string `json:"script"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// ParsedTask is the decoded form of a task file, mirroring the CLI's --json
// DTO key for key. Every field is optional so a minimal new file parses;
// DiffTask treats unset fields as untouched.
type ParsedTask struct {
	ID           Field[string]            `json:"id"`
	Branch       Field[string]            `json:"branch"`
	Title        Field[string]            `json:"title"`
	Description  Field[string]            `json:"description"`
	Type         Field[string]            `json:"type"`
	Status       Field[string]            `json:"status"`
	Priority     Field[int]               `json:"priority"`
	Assignee     Field[string]            `json:"assignee"`
	Labels       Field[[]string]          `json:"labels"`
	BlockedBy    Field[[]string]          `json:"blocked_by"`
	Blocks       Field[[]string]          `json:"blocks"`
	Parent       Field[string]            `json:"parent"`
	Comments     Field[[]ParsedComment]   `json:"comments"`
	Commits      Field[[]string]          `json:"commits"`
	Lease        ParsedLease              `json:"lease"`
	CreatedAt    Field[string]            `json:"created_at"`
	UpdatedAt    Field[string]            `json:"updated_at"`
	StartedAt    Field[string]            `json:"started_at"`
	ClosedAt     Field[string]            `json:"closed_at"`
	Sprint       Field[string]            `json:"sprint"`
	Project      Field[string]            `json:"project"`
	Criteria     Field[[]ParsedCriterion] `json:"criteria"`
	ClosedForced Field[bool]              `json:"closed_forced"`
}

// ParsedLease is the nested lease object of a parsed task document. Both fields
// are CLI-owned; DiffTask pins them immutable.
type ParsedLease struct {
	Holder    Field[string] `json:"holder"`
	Heartbeat Field[string] `json:"heartbeat"`
}

// taskDoc mirrors internal/cli's taskDTO field for field: the rendered task
// file must stay byte-compatible with `task show --json` pretty-printed
// (TestRenderTaskMatchesCLIJSON pins it), so any change there lands here
// too.
type taskDoc struct {
	ID           string            `json:"id"`
	Branch       string            `json:"branch"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Type         string            `json:"type"`
	Status       string            `json:"status"`
	Priority     int               `json:"priority"`
	Assignee     *string           `json:"assignee"`
	Labels       []string          `json:"labels"`
	BlockedBy    []string          `json:"blocked_by"`
	Blocks       []string          `json:"blocks"`
	Parent       *string           `json:"parent"`
	Comments     []ParsedComment   `json:"comments"`
	Commits      []string          `json:"commits"`
	Lease        leaseDoc          `json:"lease"`
	CreatedAt    string            `json:"created_at"`
	UpdatedAt    string            `json:"updated_at"`
	StartedAt    *string           `json:"started_at"`
	ClosedAt     *string           `json:"closed_at"`
	Sprint       *string           `json:"sprint"`
	Project      *string           `json:"project"`
	Criteria     []ParsedCriterion `json:"criteria"`
	ClosedForced bool              `json:"closed_forced"`
}

// leaseDoc mirrors internal/cli's leaseDTO field for field.
type leaseDoc struct {
	Holder    *string `json:"holder"`
	Heartbeat *string `json:"heartbeat"`
}

// noteKeys is the ordered note frontmatter contract: id, title, and tags
// always, the non-empty anchor kinds, then author/created/updated and the
// verification and stale keys, each rendered only when set. Anchor values split
// by kind with empty kinds omitted, tags as a flow sequence, RFC3339 UTC
// timestamps; witness renders as a block sequence in stored anchor order, never
// re-sorted.
var noteKeys = slices.Concat(
	[]fmKey[model.Note]{
		{key: "id", node: func(n model.Note) *yaml.Node { return scalarNode(string(n.ID)) }},
		{key: "title", node: func(n model.Note) *yaml.Node { return scalarNode(n.Title) }},
		{key: "tags", node: func(n model.Note) *yaml.Node { return flowNode(n.Tags) }},
	},
	anchorKeys(func(n model.Note) []model.Anchor { return n.Anchors }),
	[]fmKey[model.Note]{
		{key: "author", node: func(n model.Note) *yaml.Node { return scalarNode(string(n.Author)) }},
		{key: "created", node: func(n model.Note) *yaml.Node { return scalarNode(render.RFC3339(n.CreatedAt)) }},
		{key: "updated", node: func(n model.Note) *yaml.Node { return scalarNode(render.RFC3339(n.UpdatedAt)) }},
		{key: "verified_at", keep: func(n model.Note) bool { return n.VerifiedAt != 0 }, node: func(n model.Note) *yaml.Node { return scalarNode(render.RFC3339(n.VerifiedAt)) }},
		{key: "verified_by", keep: func(n model.Note) bool { return n.VerifiedBy != "" }, node: func(n model.Note) *yaml.Node { return scalarNode(string(n.VerifiedBy)) }},
		{key: "verified_commit", keep: func(n model.Note) bool { return n.VerifiedCommit != "" }, node: func(n model.Note) *yaml.Node { return scalarNode(string(n.VerifiedCommit)) }},
		{key: "witness", keep: func(n model.Note) bool { return len(n.Witness) > 0 }, node: func(n model.Note) *yaml.Node { return witnessNode(n.Witness) }},
		{key: "superseded_by", keep: func(n model.Note) bool { return len(n.SupersededBy) > 0 }, node: func(n model.Note) *yaml.Node { return flowNode(render.IDStrings(n.SupersededBy)) }},
		{key: "stale_at", keep: func(n model.Note) bool { return n.StaleAt != 0 }, node: func(n model.Note) *yaml.Node { return scalarNode(render.RFC3339(n.StaleAt)) }},
		{key: "stale_by", keep: func(n model.Note) bool { return n.StaleBy != "" }, node: func(n model.Note) *yaml.Node { return scalarNode(string(n.StaleBy)) }},
		{key: "stale_reason", keep: func(n model.Note) bool { return n.StaleReason != "" }, node: func(n model.Note) *yaml.Node { return scalarNode(n.StaleReason) }},
	},
)

// RenderNote renders n as markdown: the noteKeys YAML frontmatter, then the
// body verbatim below the closing delimiter. The output is deterministic byte
// for byte.
func RenderNote(n model.Note) []byte {
	buf := renderFrontmatter(n, noteKeys)
	buf.WriteString(n.Body)
	return buf.Bytes()
}

// ParseNote decodes a note file into the shared ParsedDoc: YAML frontmatter
// between --- delimiters, body verbatim below. Decoding is strict — a missing
// delimiter or an unknown frontmatter key fails with ErrParse, and a note
// carrying the doc-only when field is rejected.
func ParseNote(data []byte) (ParsedDoc, error) {
	p, err := parseFrontmatterDoc(data)
	if err != nil {
		return ParsedDoc{}, err
	}
	if p.When.Set {
		return ParsedDoc{}, fmt.Errorf("%w: notes have no when field", ErrParse)
	}
	return p, nil
}

// parseFrontmatterDoc decodes a note or doc file into the shared ParsedDoc: YAML
// frontmatter between --- delimiters, body verbatim below. Decoding is strict —
// a missing delimiter or an unknown frontmatter key fails with ErrParse.
func parseFrontmatterDoc(data []byte) (ParsedDoc, error) {
	fm, body, err := splitFrontmatter(string(data))
	if err != nil {
		return ParsedDoc{}, err
	}
	var p ParsedDoc
	dec := yaml.NewDecoder(strings.NewReader(fm))
	dec.KnownFields(true)
	switch err := dec.Decode(&p); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return ParsedDoc{}, fmt.Errorf("%w: %w", ErrParse, err)
	}
	p.Body = body
	return p, nil
}

func splitFrontmatter(doc string) (fm, body string, err error) {
	rest, ok := strings.CutPrefix(doc, delimiter)
	if !ok {
		return "", "", fmt.Errorf("%w: missing leading frontmatter delimiter", ErrParse)
	}
	switch {
	case strings.HasPrefix(rest, delimiter):
		return "", rest[len(delimiter):], nil
	case rest == "---":
		return "", "", nil
	}
	if i := strings.Index(rest, "\n---\n"); i >= 0 {
		return rest[:i+1], rest[i+5:], nil
	}
	if strings.HasSuffix(rest, "\n---") {
		return rest[:len(rest)-3], "", nil
	}
	return "", "", fmt.Errorf("%w: missing closing frontmatter delimiter", ErrParse)
}

// check is one immutability guard: it returns ErrImmutableField when a frozen
// field was edited, nil otherwise. diffWith joins a kind's checks before it
// produces any ops.
type check func() error

// fieldDiff produces the ordered ops for one editable field group, returning a
// parse error for an invalid value. diffWith runs a kind's fieldDiffs in slice
// order and concatenates their ops.
type fieldDiff func() ([]model.Op, error)

// diffWith joins every check's error and returns on failure, then runs each
// fieldDiff in slice order, concatenating ops and propagating the first field
// error. Immutability is decided before any op is emitted — the byte contract.
func diffWith(checks []check, fields []fieldDiff) ([]model.Op, error) {
	errs := make([]error, len(checks))
	for i, c := range checks {
		errs[i] = c()
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var ops []model.Op
	for _, f := range fields {
		fieldOps, err := f()
		if err != nil {
			return nil, err
		}
		ops = append(ops, fieldOps...)
	}
	return ops, nil
}

func pin(field string, f Field[string], want string) check {
	return func() error { return immutable(field, f, want) }
}

func pinStrings(field string, f Field[[]string], want []string) check {
	return func() error { return immutableStrings(field, f, want) }
}

func pinComments(f Field[[]ParsedComment], base []model.Comment) check {
	return func() error { return immutableComments(f, base) }
}

func pinWitness(f Field[[]ParsedWitness], base []model.AnchorWitness) check {
	return func() error { return immutableWitness(f, base) }
}

// scalar emits op(value) when a Set string field differs from base.
func scalar(f Field[string], base string, op func(string) model.Op) fieldDiff {
	return func() ([]model.Op, error) {
		if !f.Set {
			return nil, nil
		}
		if v := stringValue(f); v != base {
			return []model.Op{op(v)}, nil
		}
		return nil, nil
	}
}

// body emits SetBody when the parsed body differs from base.
func body(parsed, base string) fieldDiff {
	return func() ([]model.Op, error) {
		if parsed != base {
			return []model.Op{model.SetBody{Body: parsed}}, nil
		}
		return nil, nil
	}
}

// enum parses a Set field through parse and emits op(value) when it differs from
// base, propagating a parse error; it backs statuses, priority, and dates.
func enum[F any, V comparable](f Field[F], base V, parse func(Field[F]) (V, error), op func(V) model.Op) fieldDiff {
	return func() ([]model.Op, error) {
		if !f.Set {
			return nil, nil
		}
		v, err := parse(f)
		if err != nil {
			return nil, err
		}
		if v != base {
			return []model.Op{op(v)}, nil
		}
		return nil, nil
	}
}

// stringSet diffs a Set string-slice field against base, emitting add(value) for
// each addition then remove(value) for each removal, each group sorted.
func stringSet(f Field[[]string], base []string, add, remove func(string) model.Op) fieldDiff {
	return func() ([]model.Op, error) {
		if !f.Set {
			return nil, nil
		}
		adds, removes := diffSets(base, stringsValue(f))
		var ops []model.Op
		for _, v := range adds {
			ops = append(ops, add(v))
		}
		for _, v := range removes {
			ops = append(ops, remove(v))
		}
		return ops, nil
	}
}

// anchorSet diffs each Set anchor kind against base in anchorKinds order,
// emitting AddAnchor then RemoveAnchor per kind.
func anchorSet(anchors func(model.AnchorKind) Field[[]string], base []model.Anchor) fieldDiff {
	return func() ([]model.Op, error) {
		var ops []model.Op
		for _, ak := range anchorKinds {
			field := anchors(ak.kind)
			if !field.Set {
				continue
			}
			adds, removes := diffSets(render.AnchorValues(base, ak.kind), stringsValue(field))
			for _, value := range adds {
				ops = append(ops, model.AddAnchor{Anchor: model.Anchor{Kind: ak.kind, Value: value}})
			}
			for _, value := range removes {
				ops = append(ops, model.RemoveAnchor{Anchor: model.Anchor{Kind: ak.kind, Value: value}})
			}
		}
		return ops, nil
	}
}

// criteria wraps diffCriteria as a fieldDiff.
func criteria(f Field[[]ParsedCriterion], base []model.Criterion) fieldDiff {
	return func() ([]model.Op, error) { return diffCriteria(base, f) }
}

func setTitle(s string) model.Op       { return model.SetTitle{Title: s} }
func setWhen(s string) model.Op        { return model.SetWhen{When: s} }
func setDescription(s string) model.Op { return model.SetDescription{Description: s} }
func addTag(s string) model.Op         { return model.AddTag{Tag: s} }
func removeTag(s string) model.Op      { return model.RemoveTag{Tag: s} }
func addLabel(s string) model.Op       { return model.AddLabel{Label: s} }
func removeLabel(s string) model.Op    { return model.RemoveLabel{Label: s} }

// DiffNote compares an edited note document against the snapshot it was
// rendered from and returns the ops that reproduce the edit. Title, body,
// tags, and anchors are editable; id, author, created, and the verification
// fields (verified_at, verified_by, verified_commit, witness, superseded_by)
// are immutable — echoing them unchanged is fine, changing them fails with
// ErrImmutableField (verification state changes via the CLI, not the
// filesystem). The updated stamp is informational: editors save the stale
// one, so any value is accepted and never diffed. Ops come out in a fixed
// order — set_title, set_body, tag adds then removes, anchor adds then
// removes per kind — each group sorted by value.
func DiffNote(base model.Note, p ParsedDoc) ([]model.Op, error) {
	return diffWith(
		[]check{
			pin("id", p.ID, string(base.ID)),
			pin("author", p.Author, string(base.Author)),
			pin("created", p.Created, render.RFC3339(base.CreatedAt)),
			pin("verified_at", p.VerifiedAt, render.OptTimeString(base.VerifiedAt)),
			pin("verified_by", p.VerifiedBy, string(base.VerifiedBy)),
			pin("verified_commit", p.VerifiedCommit, string(base.VerifiedCommit)),
			pinStrings("superseded_by", p.SupersededBy, render.IDStrings(base.SupersededBy)),
			pinWitness(p.Witness, base.Witness),
			pin("stale_at", p.StaleAt, render.OptTimeString(base.StaleAt)),
			pin("stale_by", p.StaleBy, string(base.StaleBy)),
			pin("stale_reason", p.StaleReason, base.StaleReason),
		},
		[]fieldDiff{
			scalar(p.Title, base.Title, setTitle),
			body(p.Body, base.Body),
			stringSet(p.Tags, base.Tags, addTag, removeTag),
			anchorSet(p.anchors, base.Anchors),
		},
	)
}

// NewNote builds the create op for a brand-new note file. The title comes
// from the frontmatter, falling back to the first "# " heading in the body;
// neither is an error. A new file claiming an id is a contradiction and
// fails; author, created, and updated are informational and ignored.
func NewNote(p ParsedDoc) ([]model.Op, error) {
	if p.ID.Set {
		return nil, fmt.Errorf("%w: id on a new note", ErrParse)
	}
	title := stringValue(p.Title)
	if title == "" {
		title = firstHeading(p.Body)
	}
	if title == "" {
		return nil, fmt.Errorf("%w: new note needs a title or a # heading", ErrParse)
	}
	var anchors []model.Anchor
	for _, ak := range anchorKinds {
		for _, value := range sortedSet(stringsValue(p.anchors(ak.kind))) {
			anchors = append(anchors, model.Anchor{Kind: ak.kind, Value: value})
		}
	}
	return []model.Op{model.CreateNote{
		Nonce:   model.NewNonce(),
		Title:   title,
		Body:    p.Body,
		Tags:    sortedSet(stringsValue(p.Tags)),
		Anchors: anchors,
	}}, nil
}

func firstHeading(body string) string {
	for line := range strings.Lines(body) {
		if heading, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(heading)
		}
	}
	return ""
}

// ParsedDoc is the decoded form of a note or doc file — the frontmatter keys
// note and doc share plus the body below the closing delimiter. The when key
// (the free-text "read this when…" trigger) is a doc field; ParseNote rejects a
// note that sets it. Every frontmatter field is optional so a minimal new file
// parses; the differ treats unset fields as untouched.
type ParsedDoc struct {
	ID             Field[string]          `yaml:"id"`
	Title          Field[string]          `yaml:"title"`
	When           Field[string]          `yaml:"when"`
	Tags           Field[[]string]        `yaml:"tags"`
	Commits        Field[[]string]        `yaml:"commits"`
	Paths          Field[[]string]        `yaml:"paths"`
	Dirs           Field[[]string]        `yaml:"dirs"`
	Branches       Field[[]string]        `yaml:"branches"`
	Author         Field[string]          `yaml:"author"`
	Created        Field[string]          `yaml:"created"`
	Updated        Field[string]          `yaml:"updated"`
	VerifiedAt     Field[string]          `yaml:"verified_at"`
	VerifiedBy     Field[string]          `yaml:"verified_by"`
	VerifiedCommit Field[string]          `yaml:"verified_commit"`
	Witness        Field[[]ParsedWitness] `yaml:"witness"`
	SupersededBy   Field[[]string]        `yaml:"superseded_by"`
	StaleAt        Field[string]          `yaml:"stale_at"`
	StaleBy        Field[string]          `yaml:"stale_by"`
	StaleReason    Field[string]          `yaml:"stale_reason"`
	Body           string                 `yaml:"-"`
}

func (p ParsedDoc) anchors(kind model.AnchorKind) Field[[]string] {
	switch kind {
	case model.AnchorCommit:
		return p.Commits
	case model.AnchorPath:
		return p.Paths
	case model.AnchorDir:
		return p.Dirs
	case model.AnchorBranch:
		return p.Branches
	}
	panic("fusefs: unknown anchor kind " + string(kind))
}

// docKeys mirrors noteKeys with one added key: when renders right after title,
// always (even when empty), so a doc round-trips byte for byte.
var docKeys = slices.Concat(
	[]fmKey[model.Doc]{
		{key: "id", node: func(d model.Doc) *yaml.Node { return scalarNode(string(d.ID)) }},
		{key: "title", node: func(d model.Doc) *yaml.Node { return scalarNode(d.Title) }},
		{key: "when", node: func(d model.Doc) *yaml.Node { return scalarNode(d.When) }},
		{key: "tags", node: func(d model.Doc) *yaml.Node { return flowNode(d.Tags) }},
	},
	anchorKeys(func(d model.Doc) []model.Anchor { return d.Anchors }),
	[]fmKey[model.Doc]{
		{key: "author", node: func(d model.Doc) *yaml.Node { return scalarNode(string(d.Author)) }},
		{key: "created", node: func(d model.Doc) *yaml.Node { return scalarNode(render.RFC3339(d.CreatedAt)) }},
		{key: "updated", node: func(d model.Doc) *yaml.Node { return scalarNode(render.RFC3339(d.UpdatedAt)) }},
		{key: "verified_at", keep: func(d model.Doc) bool { return d.VerifiedAt != 0 }, node: func(d model.Doc) *yaml.Node { return scalarNode(render.RFC3339(d.VerifiedAt)) }},
		{key: "verified_by", keep: func(d model.Doc) bool { return d.VerifiedBy != "" }, node: func(d model.Doc) *yaml.Node { return scalarNode(string(d.VerifiedBy)) }},
		{key: "verified_commit", keep: func(d model.Doc) bool { return d.VerifiedCommit != "" }, node: func(d model.Doc) *yaml.Node { return scalarNode(string(d.VerifiedCommit)) }},
		{key: "witness", keep: func(d model.Doc) bool { return len(d.Witness) > 0 }, node: func(d model.Doc) *yaml.Node { return witnessNode(d.Witness) }},
		{key: "superseded_by", keep: func(d model.Doc) bool { return len(d.SupersededBy) > 0 }, node: func(d model.Doc) *yaml.Node { return flowNode(render.IDStrings(d.SupersededBy)) }},
		{key: "stale_at", keep: func(d model.Doc) bool { return d.StaleAt != 0 }, node: func(d model.Doc) *yaml.Node { return scalarNode(render.RFC3339(d.StaleAt)) }},
		{key: "stale_by", keep: func(d model.Doc) bool { return d.StaleBy != "" }, node: func(d model.Doc) *yaml.Node { return scalarNode(string(d.StaleBy)) }},
		{key: "stale_reason", keep: func(d model.Doc) bool { return d.StaleReason != "" }, node: func(d model.Doc) *yaml.Node { return scalarNode(d.StaleReason) }},
	},
)

// RenderDoc renders d as markdown with YAML frontmatter, mirroring RenderNote
// with the always-present when key (docKeys), and the verbatim body below the
// closing delimiter. The output is deterministic byte for byte.
func RenderDoc(d model.Doc) []byte {
	buf := renderFrontmatter(d, docKeys)
	buf.WriteString(d.Body)
	return buf.Bytes()
}

// NewDocTemplate renders the prefilled buffer `doc add --checkout` writes: the
// editable doc keys (title, when, tags always; non-empty anchor kinds in
// RenderDoc order) and an empty body, carrying no id so it satisfies NewDoc. A
// zero-value call reproduces the empty new-doc template byte for byte.
func NewDocTemplate(title, when string, tags []string, anchors []model.Anchor) []byte {
	fm := &yaml.Node{Kind: yaml.MappingNode}
	put := func(key string, value *yaml.Node) {
		fm.Content = append(fm.Content, scalarNode(key), value)
	}
	put("title", scalarNode(title))
	put("when", scalarNode(when))
	put("tags", flowNode(tags))
	putAnchors(put, anchors)
	return encodeFrontmatter(fm).Bytes()
}

// NewNoteTemplate renders the prefilled buffer `note add --checkout` writes,
// mirroring NewDocTemplate without the when key (notes have no trigger).
func NewNoteTemplate(title string, tags []string, anchors []model.Anchor) []byte {
	fm := &yaml.Node{Kind: yaml.MappingNode}
	put := func(key string, value *yaml.Node) {
		fm.Content = append(fm.Content, scalarNode(key), value)
	}
	put("title", scalarNode(title))
	put("tags", flowNode(tags))
	putAnchors(put, anchors)
	return encodeFrontmatter(fm).Bytes()
}

func putAnchors(put func(string, *yaml.Node), anchors []model.Anchor) {
	for _, ak := range anchorKinds {
		if values := render.AnchorValues(anchors, ak.kind); len(values) > 0 {
			put(ak.key, flowNode(values))
		}
	}
}

// ParseDoc decodes a doc file into the shared ParsedDoc: YAML frontmatter
// between --- delimiters, body verbatim below. Decoding is strict — a missing
// delimiter or an unknown frontmatter key fails with ErrParse.
func ParseDoc(data []byte) (ParsedDoc, error) {
	return parseFrontmatterDoc(data)
}

// DiffDoc compares an edited doc document against the snapshot it was rendered
// from and returns the ops that reproduce the edit. Title, when, body, tags,
// and anchors are editable; id, author, created, and the verification fields
// (verified_at, verified_by, verified_commit, witness, superseded_by) are
// immutable — echoing them unchanged is fine, changing them fails with
// ErrImmutableField (verification state changes via the CLI, not the
// filesystem). The updated stamp is informational: editors save the stale one,
// so any value is accepted and never diffed. Ops come out in a fixed order —
// set_title, set_when, set_body, tag adds then removes, anchor adds then removes
// per kind — each group sorted by value.
func DiffDoc(base model.Doc, p ParsedDoc) ([]model.Op, error) {
	return diffWith(
		[]check{
			pin("id", p.ID, string(base.ID)),
			pin("author", p.Author, string(base.Author)),
			pin("created", p.Created, render.RFC3339(base.CreatedAt)),
			pin("verified_at", p.VerifiedAt, render.OptTimeString(base.VerifiedAt)),
			pin("verified_by", p.VerifiedBy, string(base.VerifiedBy)),
			pin("verified_commit", p.VerifiedCommit, string(base.VerifiedCommit)),
			pinStrings("superseded_by", p.SupersededBy, render.IDStrings(base.SupersededBy)),
			pinWitness(p.Witness, base.Witness),
			pin("stale_at", p.StaleAt, render.OptTimeString(base.StaleAt)),
			pin("stale_by", p.StaleBy, string(base.StaleBy)),
			pin("stale_reason", p.StaleReason, base.StaleReason),
		},
		[]fieldDiff{
			scalar(p.Title, base.Title, setTitle),
			scalar(p.When, base.When, setWhen),
			body(p.Body, base.Body),
			stringSet(p.Tags, base.Tags, addTag, removeTag),
			anchorSet(p.anchors, base.Anchors),
		},
	)
}

// NewDoc builds the create op for a brand-new doc file. The title comes from
// the frontmatter, falling back to the first "# " heading in the body; neither
// is an error. The when trigger is optional. A new file claiming an id is a
// contradiction and fails; author, created, and updated are informational and
// ignored.
func NewDoc(p ParsedDoc) ([]model.Op, error) {
	if p.ID.Set {
		return nil, fmt.Errorf("%w: id on a new doc", ErrParse)
	}
	title := stringValue(p.Title)
	if title == "" {
		title = firstHeading(p.Body)
	}
	if title == "" {
		return nil, fmt.Errorf("%w: new doc needs a title or a # heading", ErrParse)
	}
	var anchors []model.Anchor
	for _, ak := range anchorKinds {
		for _, value := range sortedSet(stringsValue(p.anchors(ak.kind))) {
			anchors = append(anchors, model.Anchor{Kind: ak.kind, Value: value})
		}
	}
	return []model.Op{model.CreateDoc{
		Nonce:   model.NewNonce(),
		Title:   title,
		Body:    p.Body,
		When:    stringValue(p.When),
		Tags:    sortedSet(stringsValue(p.Tags)),
		Anchors: anchors,
	}}, nil
}

// logEntryFencePrefix opens the viewer-invisible HTML comment that fences one
// log entry, carrying the entry's author and timestamp. Entry text that itself
// contains this prefix is rejected at parse time — the fence is the split key,
// so a collision would be ambiguous.
const logEntryFencePrefix = "<!-- cc-notes:entry "

// logEntryFenceRe matches a full fence line, capturing the author, the RFC3339
// timestamp, and an optional model identity. It is anchored and matched per
// whole line so entry text can never be mistaken for a delimiter. Author and
// model are captured greedily (raw); the quote-free ts group between them anchors
// both captures, so a model like `vendor/model"preview` round-trips.
var logEntryFenceRe = regexp.MustCompile(`^<!-- cc-notes:entry author="(.*)" ts="([^"]*)"(?: model="(.*)")? -->$`)

// logEntryFence renders the fence line for one entry. The model attribute is
// emitted only when non-empty and its value is written raw inside quotes (no %q
// escaping), matching the greedy capture in logEntryFenceRe so a quote-bearing
// model round-trips.
func logEntryFence(author, ts, entryModel string) string {
	attr := ""
	if entryModel != "" {
		attr = ` model="` + entryModel + `"`
	}
	return fmt.Sprintf(`<!-- cc-notes:entry author=%q ts=%q%s -->`, author, ts, attr)
}

// ParsedLogEntry is one entry recovered from a log file body: the author,
// timestamp, and optional model captured from its fence line plus the verbatim
// text below it (which keeps its own trailing newline). On a new trailing entry
// author and ts are ignored — they come from the carrying commit — but Model is
// op-carried, so a hand-written model attribute is honored.
type ParsedLogEntry struct {
	Author string
	TS     string
	Model  string
	Text   string
}

// ParsedLog is the decoded form of a log file: the frontmatter keys plus the
// ordered entries below the closing delimiter. It mirrors ParsedDoc minus the
// freshness lifecycle and the single body, with the body replaced by an
// append-only Entries list. Every frontmatter field is optional so a minimal
// new file parses; DiffLog treats unset fields as untouched.
type ParsedLog struct {
	ID       Field[string]    `yaml:"id"`
	Title    Field[string]    `yaml:"title"`
	Tags     Field[[]string]  `yaml:"tags"`
	Commits  Field[[]string]  `yaml:"commits"`
	Paths    Field[[]string]  `yaml:"paths"`
	Dirs     Field[[]string]  `yaml:"dirs"`
	Branches Field[[]string]  `yaml:"branches"`
	Author   Field[string]    `yaml:"author"`
	Created  Field[string]    `yaml:"created"`
	Updated  Field[string]    `yaml:"updated"`
	Entries  []ParsedLogEntry `yaml:"-"`
}

func (p ParsedLog) anchors(kind model.AnchorKind) Field[[]string] {
	switch kind {
	case model.AnchorCommit:
		return p.Commits
	case model.AnchorPath:
		return p.Paths
	case model.AnchorDir:
		return p.Dirs
	case model.AnchorBranch:
		return p.Branches
	}
	panic("fusefs: unknown anchor kind " + string(kind))
}

// logKeys is the Doc keys minus when and the whole freshness block: id, title,
// tags, the non-empty anchor kinds, then author/created/updated.
var logKeys = slices.Concat(
	[]fmKey[model.Log]{
		{key: "id", node: func(l model.Log) *yaml.Node { return scalarNode(string(l.ID)) }},
		{key: "title", node: func(l model.Log) *yaml.Node { return scalarNode(l.Title) }},
		{key: "tags", node: func(l model.Log) *yaml.Node { return flowNode(l.Tags) }},
	},
	anchorKeys(func(l model.Log) []model.Anchor { return l.Anchors }),
	[]fmKey[model.Log]{
		{key: "author", node: func(l model.Log) *yaml.Node { return scalarNode(string(l.Author)) }},
		{key: "created", node: func(l model.Log) *yaml.Node { return scalarNode(render.RFC3339(l.CreatedAt)) }},
		{key: "updated", node: func(l model.Log) *yaml.Node { return scalarNode(render.RFC3339(l.UpdatedAt)) }},
	},
)

// RenderLog renders l as markdown: the logKeys frontmatter, then one block per
// entry — a viewer-invisible fence line carrying the entry's author, timestamp,
// and optional model, then the entry text. Each non-empty entry is terminated
// with a newline if it lacks one, so the following fence or EOF always lands at a
// line start; DiffLog compares stored entries against this same canonicalized
// form. The output is deterministic byte for byte.
func RenderLog(l model.Log) []byte {
	buf := renderFrontmatter(l, logKeys)
	for _, e := range l.Entries {
		buf.WriteString(logEntryFence(string(e.Author), render.RFC3339(e.TS), e.Model))
		buf.WriteString("\n")
		buf.WriteString(ensureTrailingNewline(e.Text))
	}
	return buf.Bytes()
}

// ensureTrailingNewline returns text with a single trailing newline appended
// when it lacks one, leaving empty text empty. Entry text reaches the renderer
// in two shapes: the FUSE editor saves text that already ends in a newline,
// while CLI-created entries (log append, -m, --entry) store the text verbatim
// with no trailing newline. Terminating every non-empty entry keeps the next
// fence — or EOF — anchored at a line start so ParseLog can split it, and is
// the canonical form DiffLog compares stored entries against.
func ensureTrailingNewline(text string) string {
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

// ParseLog decodes a log file: YAML frontmatter between --- delimiters, then the
// fenced entries below. Decoding is strict — a missing delimiter, an unknown
// frontmatter key, body text before the first fence, or entry text colliding
// with the fence sentinel all fail with ErrParse.
func ParseLog(data []byte) (ParsedLog, error) {
	fm, body, err := splitFrontmatter(string(data))
	if err != nil {
		return ParsedLog{}, err
	}
	var p ParsedLog
	dec := yaml.NewDecoder(strings.NewReader(fm))
	dec.KnownFields(true)
	switch err := dec.Decode(&p); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return ParsedLog{}, fmt.Errorf("%w: %w", ErrParse, err)
	}
	entries, err := splitLogEntries(body)
	if err != nil {
		return ParsedLog{}, err
	}
	p.Entries = entries
	return p, nil
}

// splitLogEntries walks the log body line by line: a full line matching the
// fence regex opens a new entry, capturing its author and timestamp; the
// verbatim text between fences (newlines kept) is the entry text. Non-empty text
// before the first fence fails with ErrParse, as does any entry text line
// carrying the fence sentinel.
func splitLogEntries(body string) ([]ParsedLogEntry, error) {
	var entries []ParsedLogEntry
	var text strings.Builder
	open := false
	for line := range strings.Lines(body) {
		stripped := strings.TrimSuffix(line, "\n")
		if m := logEntryFenceRe.FindStringSubmatch(stripped); m != nil {
			if open {
				entries[len(entries)-1].Text = text.String()
			}
			entries = append(entries, ParsedLogEntry{Author: m[1], TS: m[2], Model: m[3]})
			text.Reset()
			open = true
			continue
		}
		if !open {
			if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("%w: log body text before first entry", ErrParse)
			}
			continue
		}
		if strings.Contains(line, logEntryFencePrefix) {
			return nil, fmt.Errorf("%w: log entry text collides with the entry fence sentinel", ErrParse)
		}
		text.WriteString(line)
	}
	if open {
		entries[len(entries)-1].Text = text.String()
	}
	return entries, nil
}

// DiffLog compares an edited log document against the snapshot it was rendered
// from and returns the ops that reproduce the edit. Entries are append-only: the
// first len(base.Entries) parsed entries must reproduce the stored entries
// byte-for-byte (author, ts, model, text), and any modification, reorder,
// removal, or a count below the stored count fails with ErrImmutableField; only
// genuinely-new trailing entries become AppendEntry ops, with their fence
// author/ts ignored (those come from the carrying commit) but their model
// carried into the op. Title, tags, and anchors diff exactly
// like DiffDoc; id, author, and created are immutable; the updated stamp is
// informational. Ops come out in a fixed order — set_title, tag adds then
// removes, anchor adds then removes per kind, then the new entries in order.
func DiffLog(base model.Log, p ParsedLog) ([]model.Op, error) {
	return diffWith(
		[]check{
			pin("id", p.ID, string(base.ID)),
			pin("author", p.Author, string(base.Author)),
			pin("created", p.Created, render.RFC3339(base.CreatedAt)),
		},
		[]fieldDiff{
			logAppendOnly(base, p),
			scalar(p.Title, base.Title, setTitle),
			stringSet(p.Tags, base.Tags, addTag, removeTag),
			anchorSet(p.anchors, base.Anchors),
			logNewEntries(base, p),
		},
	)
}

// logAppendOnly guards the stored entries: the first len(base.Entries) parsed
// entries must reproduce the stored author, ts, model, and text, and the count
// may not drop. It emits no ops, running before the frontmatter fields so a
// tampered entry fails before any op is produced.
func logAppendOnly(base model.Log, p ParsedLog) fieldDiff {
	return func() ([]model.Op, error) {
		if len(p.Entries) < len(base.Entries) {
			return nil, fmt.Errorf("%w: removed existing entries", ErrImmutableField)
		}
		for i, be := range base.Entries {
			pe := p.Entries[i]
			if pe.Author != string(be.Author) || pe.TS != render.RFC3339(be.TS) || pe.Model != be.Model || pe.Text != ensureTrailingNewline(be.Text) {
				return nil, fmt.Errorf("%w: log entry %d is append-only", ErrImmutableField, i)
			}
		}
		return nil, nil
	}
}

// logNewEntries emits an AppendEntry for each trailing entry beyond the base
// count; an empty new entry fails with ErrParse.
func logNewEntries(base model.Log, p ParsedLog) fieldDiff {
	return func() ([]model.Op, error) {
		var ops []model.Op
		for _, pe := range p.Entries[len(base.Entries):] {
			if pe.Text == "" {
				return nil, fmt.Errorf("%w: new log entry is empty", ErrParse)
			}
			ops = append(ops, model.AppendEntry{Text: pe.Text, Model: pe.Model})
		}
		return ops, nil
	}
}

// NewLog builds the create op plus any initial entries for a brand-new log file.
// The title comes from the frontmatter, falling back to the first "# " heading
// in the first entry; neither is an error if the other is present. A new file
// claiming an id is a contradiction and fails; author, created, and updated are
// informational and ignored. Each parsed entry becomes an AppendEntry; an empty
// entry fails with ErrParse.
func NewLog(p ParsedLog) ([]model.Op, error) {
	if p.ID.Set {
		return nil, fmt.Errorf("%w: id on a new log", ErrParse)
	}
	title := stringValue(p.Title)
	if title == "" && len(p.Entries) > 0 {
		title = firstHeading(p.Entries[0].Text)
	}
	if title == "" {
		return nil, fmt.Errorf("%w: new log needs a title or a # heading", ErrParse)
	}
	var anchors []model.Anchor
	for _, ak := range anchorKinds {
		for _, value := range sortedSet(stringsValue(p.anchors(ak.kind))) {
			anchors = append(anchors, model.Anchor{Kind: ak.kind, Value: value})
		}
	}
	ops := []model.Op{model.CreateLog{
		Nonce:   model.NewNonce(),
		Title:   title,
		Tags:    sortedSet(stringsValue(p.Tags)),
		Anchors: anchors,
	}}
	for _, pe := range p.Entries {
		if pe.Text == "" {
			return nil, fmt.Errorf("%w: new log entry is empty", ErrParse)
		}
		ops = append(ops, model.AppendEntry{Text: pe.Text, Model: pe.Model})
	}
	return ops, nil
}

// RenderTask renders t as the CLI's --json document pretty-printed with
// 2-space indent and a trailing newline, byte-compatible with
// `task show --json`. Blocks is a derived cross-entity index this layer
// cannot compute from one task, so it renders empty; DiffTask pins it to
// empty in turn.
func RenderTask(t model.Task) []byte {
	doc := taskDoc{
		ID:           string(t.ID),
		Branch:       string(t.Branch),
		Title:        t.Title,
		Description:  t.Description,
		Type:         string(t.Type),
		Status:       string(t.Status),
		Priority:     int(t.Priority),
		Assignee:     render.OptString(string(t.Assignee)),
		Labels:       render.EmptyNotNil(t.Labels),
		BlockedBy:    render.IDStrings(t.BlockedBy),
		Blocks:       []string{},
		Parent:       render.OptString(string(t.Parent)),
		Comments:     renderComments(t.Comments),
		Commits:      render.SHAStrings(t.Commits),
		Lease:        leaseDoc{Holder: render.OptString(string(t.Assignee)), Heartbeat: render.OptTime(t.HeartbeatAt)},
		CreatedAt:    render.RFC3339(t.CreatedAt),
		UpdatedAt:    render.RFC3339(t.UpdatedAt),
		StartedAt:    render.OptTime(t.StartedAt),
		ClosedAt:     render.OptTime(t.ClosedAt),
		Sprint:       render.OptString(string(t.Sprint)),
		Project:      render.OptString(string(t.Project)),
		Criteria:     renderCriteria(t.Criteria),
		ClosedForced: taskClosedForced(t),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("fusefs: encode task document: %v", err))
	}
	return append(data, '\n')
}

// renderCriteria mirrors internal/cli's criterionDTOs: always non-nil so the
// JSON serializes an empty list rather than null.
func renderCriteria(criteria []model.Criterion) []ParsedCriterion {
	out := make([]ParsedCriterion, len(criteria))
	for i, c := range criteria {
		out[i] = ParsedCriterion{ID: c.ID, Text: c.Text, Script: c.Script, Status: string(c.Status), Note: c.Note}
	}
	return out
}

// taskClosedForced mirrors internal/cli's closedForced: a done task left with at
// least one unmet criterion was force-closed.
func taskClosedForced(t model.Task) bool {
	if t.Status != model.StatusDone {
		return false
	}
	for _, c := range t.Criteria {
		if c.Status != model.CriterionMet {
			return true
		}
	}
	return false
}

// ParseTask decodes a task file. Decoding is strict — the document must be
// a single JSON object, unknown keys at any depth and trailing data fail
// with ErrParse.
func ParseTask(data []byte) (ParsedTask, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ParsedTask{}, fmt.Errorf("%w: task document must be a JSON object", ErrParse)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var p ParsedTask
	if err := dec.Decode(&p); err != nil {
		return ParsedTask{}, fmt.Errorf("%w: %w", ErrParse, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return ParsedTask{}, fmt.Errorf("%w: trailing data after task document", ErrParse)
	}
	return p, nil
}

// DiffTask compares an edited task document against the snapshot it was
// rendered from and returns the ops that reproduce the edit. Title,
// description, status, priority, labels, and criteria are editable; id, branch,
// type, assignee, blocked_by, blocks, parent, comments, sprint, project, and
// every timestamp are immutable — echoing them unchanged is fine, changing them
// fails with ErrImmutableField (coordination and membership fields change via
// the CLI, not the filesystem). closed_forced is informational: parsed, never
// diffed. Ops come out in a fixed order —
// set_title, set_description, set_status, set_priority, label adds then removes
// sorted by value, then the criteria ops (see diffCriteria).
func DiffTask(base model.Task, p ParsedTask) ([]model.Op, error) {
	return diffWith(
		[]check{
			pin("id", p.ID, string(base.ID)),
			pin("branch", p.Branch, string(base.Branch)),
			pin("type", p.Type, string(base.Type)),
			pin("assignee", p.Assignee, string(base.Assignee)),
			pinStrings("blocked_by", p.BlockedBy, render.IDStrings(base.BlockedBy)),
			pinStrings("blocks", p.Blocks, nil),
			pin("parent", p.Parent, string(base.Parent)),
			pinComments(p.Comments, base.Comments),
			pinStrings("commits", p.Commits, render.SHAStrings(base.Commits)),
			pin("lease.holder", p.Lease.Holder, string(base.Assignee)),
			pin("lease.heartbeat", p.Lease.Heartbeat, render.OptTimeString(base.HeartbeatAt)),
			pin("created_at", p.CreatedAt, render.RFC3339(base.CreatedAt)),
			pin("updated_at", p.UpdatedAt, render.RFC3339(base.UpdatedAt)),
			pin("started_at", p.StartedAt, render.OptTimeString(base.StartedAt)),
			pin("closed_at", p.ClosedAt, render.OptTimeString(base.ClosedAt)),
			pin("sprint", p.Sprint, string(base.Sprint)),
			pin("project", p.Project, string(base.Project)),
		},
		[]fieldDiff{
			scalar(p.Title, base.Title, setTitle),
			scalar(p.Description, base.Description, setDescription),
			enum(p.Status, base.Status, parseStatus, func(s model.Status) model.Op { return model.SetStatus{Status: s} }),
			enum(p.Priority, base.Priority, parsePriority, func(pr model.Priority) model.Op { return model.SetPriority{Priority: pr} }),
			stringSet(p.Labels, base.Labels, addLabel, removeLabel),
			criteria(p.Criteria, base.Criteria),
		},
	)
}

// diffCriteria diffs an edited criteria array against the base, matching by id.
// A parsed entry whose id matches a base criterion emits set_criterion_text /
// set_criterion_status / set_criterion_script for each changed field —
// set_criterion_status also fires on a note-only change and always carries the
// file's note, so a status edit that leaves the note key untouched preserves the
// stored note instead of clearing it; an entry
// with an empty id is a new criterion (add_criterion under a fresh nonce, plus
// set_criterion_status when it starts non-pending); an entry with a non-empty
// id absent from the base is rejected with ErrParse — ids are server-assigned,
// the editor may not invent them. A base criterion the parsed array drops is
// removed. Ops come out deterministically: removes sorted by id, then field
// updates sorted by id (text, status, script per id), then adds in parsed
// order. An unset field leaves the criteria untouched; a null one clears them
// all. Invalid statuses fail with ErrParse.
func diffCriteria(base []model.Criterion, f Field[[]ParsedCriterion]) ([]model.Op, error) {
	if !f.Set {
		return nil, nil
	}
	parsed := f.Value
	if f.Null {
		parsed = nil
	}
	baseByID := make(map[string]model.Criterion, len(base))
	for _, c := range base {
		baseByID[c.ID] = c
	}
	matched := make(map[string]ParsedCriterion, len(parsed))
	var adds []ParsedCriterion
	for _, pc := range parsed {
		if pc.ID == "" {
			adds = append(adds, pc)
			continue
		}
		if _, ok := baseByID[pc.ID]; !ok {
			return nil, fmt.Errorf("%w: unknown criterion id %q", ErrParse, pc.ID)
		}
		matched[pc.ID] = pc
	}
	var ops []model.Op
	var removeIDs []string
	for _, c := range base {
		if _, ok := matched[c.ID]; !ok {
			removeIDs = append(removeIDs, c.ID)
		}
	}
	slices.Sort(removeIDs)
	for _, id := range removeIDs {
		ops = append(ops, model.RemoveCriterion{ID: id})
	}
	for _, id := range slices.Sorted(maps.Keys(matched)) {
		pc, bc := matched[id], baseByID[id]
		if pc.Text != bc.Text {
			ops = append(ops, model.SetCriterionText{ID: id, Text: pc.Text})
		}
		if pc.Status != string(bc.Status) || pc.Note != bc.Note {
			status, err := parseCriterionStatus(pc.Status)
			if err != nil {
				return nil, err
			}
			ops = append(ops, model.SetCriterionStatus{ID: id, Status: status, Note: pc.Note})
		}
		if pc.Script != bc.Script {
			ops = append(ops, model.SetCriterionScript{ID: id, Script: pc.Script})
		}
	}
	for _, pc := range adds {
		id := model.NewNonce()
		ops = append(ops, model.AddCriterion{ID: id, Text: pc.Text, Script: pc.Script})
		if pc.Status != "" && pc.Status != string(model.CriterionPending) {
			status, err := parseCriterionStatus(pc.Status)
			if err != nil {
				return nil, err
			}
			ops = append(ops, model.SetCriterionStatus{ID: id, Status: status})
		}
	}
	return ops, nil
}

// NewTask builds the create op for a brand-new task file on branch. The
// title is required; type defaults to task, priority to 2, and status to
// open — a non-open initial status fails. Coordination fields (assignee,
// blocked_by, blocks, parent, comments) are created via the CLI, never via
// a new file; timestamps are informational and ignored.
func NewTask(p ParsedTask, branch model.Branch) ([]model.Op, error) {
	if p.ID.Set {
		return nil, fmt.Errorf("%w: id on a new task", ErrParse)
	}
	if p.Branch.Set && stringValue(p.Branch) != string(branch) {
		return nil, fmt.Errorf("%w: branch %q on a task created in %q", ErrParse, stringValue(p.Branch), branch)
	}
	title := stringValue(p.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: new task needs a title", ErrParse)
	}
	if p.Status.Set && model.Status(stringValue(p.Status)) != model.StatusOpen {
		return nil, fmt.Errorf("%w: new task status %q, want open", ErrParse, stringValue(p.Status))
	}
	taskType := model.TypeTask
	if p.Type.Set {
		var err error
		if taskType, err = parseType(p.Type); err != nil {
			return nil, err
		}
	}
	priority := model.Priority(2)
	if p.Priority.Set {
		var err error
		if priority, err = parsePriority(p.Priority); err != nil {
			return nil, err
		}
	}
	if err := errors.Join(
		cliOnly("task", "assignee", stringValue(p.Assignee) != ""),
		cliOnly("task", "blocked_by", len(stringsValue(p.BlockedBy)) > 0),
		cliOnly("task", "blocks", len(stringsValue(p.Blocks)) > 0),
		cliOnly("task", "parent", stringValue(p.Parent) != ""),
		cliOnly("task", "comments", p.Comments.Set && !p.Comments.Null && len(p.Comments.Value) > 0),
		cliOnly("task", "commits", len(stringsValue(p.Commits)) > 0),
		cliOnly("task", "lease", stringValue(p.Lease.Holder) != "" || stringValue(p.Lease.Heartbeat) != ""),
		cliOnly("task", "sprint", stringValue(p.Sprint) != ""),
		cliOnly("task", "project", stringValue(p.Project) != ""),
		cliOnly("task", "criteria", p.Criteria.Set && !p.Criteria.Null && len(p.Criteria.Value) > 0),
		cliOnly("task", "closed_forced", p.ClosedForced.Set && !p.ClosedForced.Null && p.ClosedForced.Value),
	); err != nil {
		return nil, err
	}
	return []model.Op{model.CreateTask{
		Nonce:       model.NewNonce(),
		Title:       title,
		Description: stringValue(p.Description),
		Type:        taskType,
		Priority:    priority,
		Branch:      branch,
		Labels:      sortedSet(stringsValue(p.Labels)),
	}}, nil
}

// sprintDoc mirrors internal/cli's sprintDTO field for field: the rendered
// sprint file matches `sprint show --json` pretty-printed, so any change there
// lands here too. The tasks reverse index is cross-entity and cannot be
// computed from one sprint, so it renders empty (like RenderTask's blocks).
type sprintDoc struct {
	ID          string          `json:"id"`
	Project     *string         `json:"project"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	StartDate   *string         `json:"start_date"`
	EndDate     *string         `json:"end_date"`
	Labels      []string        `json:"labels"`
	Commits     []string        `json:"commits"`
	Comments    []ParsedComment `json:"comments"`
	Author      string          `json:"author"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	StartedAt   *string         `json:"started_at"`
	ClosedAt    *string         `json:"closed_at"`
	Tasks       []string        `json:"tasks"`
}

// ParsedSprint is the decoded form of a sprint file, mirroring sprintDoc key
// for key. Every field is optional so a minimal new file parses; DiffSprint
// treats unset fields as untouched.
type ParsedSprint struct {
	ID          Field[string]          `json:"id"`
	Project     Field[string]          `json:"project"`
	Title       Field[string]          `json:"title"`
	Description Field[string]          `json:"description"`
	Status      Field[string]          `json:"status"`
	StartDate   Field[string]          `json:"start_date"`
	EndDate     Field[string]          `json:"end_date"`
	Labels      Field[[]string]        `json:"labels"`
	Commits     Field[[]string]        `json:"commits"`
	Comments    Field[[]ParsedComment] `json:"comments"`
	Author      Field[string]          `json:"author"`
	CreatedAt   Field[string]          `json:"created_at"`
	UpdatedAt   Field[string]          `json:"updated_at"`
	StartedAt   Field[string]          `json:"started_at"`
	ClosedAt    Field[string]          `json:"closed_at"`
	Tasks       Field[[]string]        `json:"tasks"`
}

// RenderSprint renders s as the CLI's --json sprint document pretty-printed with
// 2-space indent and a trailing newline. The tasks reverse index renders empty.
func RenderSprint(s model.Sprint) []byte {
	doc := sprintDoc{
		ID:          string(s.ID),
		Project:     render.OptString(string(s.Project)),
		Title:       s.Title,
		Description: s.Description,
		Status:      string(s.Status),
		StartDate:   render.OptTime(s.StartDate),
		EndDate:     render.OptTime(s.EndDate),
		Labels:      render.EmptyNotNil(s.Labels),
		Commits:     render.SHAStrings(s.Commits),
		Comments:    renderComments(s.Comments),
		Author:      string(s.Author),
		CreatedAt:   render.RFC3339(s.CreatedAt),
		UpdatedAt:   render.RFC3339(s.UpdatedAt),
		StartedAt:   render.OptTime(s.StartedAt),
		ClosedAt:    render.OptTime(s.ClosedAt),
		Tasks:       []string{},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("fusefs: encode sprint document: %v", err))
	}
	return append(data, '\n')
}

// ParseSprint decodes a sprint file. Decoding is strict — the document must be
// a single JSON object, unknown keys at any depth and trailing data fail with
// ErrParse.
func ParseSprint(data []byte) (ParsedSprint, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ParsedSprint{}, fmt.Errorf("%w: sprint document must be a JSON object", ErrParse)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var p ParsedSprint
	if err := dec.Decode(&p); err != nil {
		return ParsedSprint{}, fmt.Errorf("%w: %w", ErrParse, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return ParsedSprint{}, fmt.Errorf("%w: trailing data after sprint document", ErrParse)
	}
	return p, nil
}

// DiffSprint compares an edited sprint document against the snapshot it was
// rendered from and returns the ops that reproduce the edit. Title,
// description, status, start_date, end_date, and labels are editable; id,
// project, commits, comments, author, the tasks reverse index, and every
// timestamp are immutable — echoing them unchanged is fine, changing them fails
// with ErrImmutableField (membership changes via the CLI, not the filesystem).
// Ops come out in a fixed order — set_title, set_description, set_sprint_status,
// set_start_date, set_end_date, label adds then removes sorted by value.
func DiffSprint(base model.Sprint, p ParsedSprint) ([]model.Op, error) {
	return diffWith(
		[]check{
			pin("id", p.ID, string(base.ID)),
			pin("project", p.Project, string(base.Project)),
			pinStrings("commits", p.Commits, render.SHAStrings(base.Commits)),
			pinComments(p.Comments, base.Comments),
			pin("author", p.Author, string(base.Author)),
			pin("created_at", p.CreatedAt, render.RFC3339(base.CreatedAt)),
			pin("updated_at", p.UpdatedAt, render.RFC3339(base.UpdatedAt)),
			pin("started_at", p.StartedAt, render.OptTimeString(base.StartedAt)),
			pin("closed_at", p.ClosedAt, render.OptTimeString(base.ClosedAt)),
			pinStrings("tasks", p.Tasks, nil),
		},
		[]fieldDiff{
			scalar(p.Title, base.Title, setTitle),
			scalar(p.Description, base.Description, setDescription),
			enum(p.Status, base.Status, parseSprintStatus, func(s model.SprintStatus) model.Op { return model.SetSprintStatus{Status: s} }),
			enum(p.StartDate, base.StartDate, parseDate, func(d int64) model.Op { return model.SetStartDate{Date: d} }),
			enum(p.EndDate, base.EndDate, parseDate, func(d int64) model.Op { return model.SetEndDate{Date: d} }),
			stringSet(p.Labels, base.Labels, addLabel, removeLabel),
		},
	)
}

// NewSprint builds the create op for a brand-new sprint file. The title is
// required; project, commits, comments, and the tasks reverse index are set via
// the CLI, never via a new file; timestamps are informational and ignored.
func NewSprint(p ParsedSprint) ([]model.Op, error) {
	if p.ID.Set {
		return nil, fmt.Errorf("%w: id on a new sprint", ErrParse)
	}
	title := stringValue(p.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: new sprint needs a title", ErrParse)
	}
	if p.Status.Set && model.SprintStatus(stringValue(p.Status)) != model.SprintPlanned {
		return nil, fmt.Errorf("%w: new sprint status %q, want planned", ErrParse, stringValue(p.Status))
	}
	if err := errors.Join(
		cliOnly("sprint", "project", stringValue(p.Project) != ""),
		cliOnly("sprint", "commits", len(stringsValue(p.Commits)) > 0),
		cliOnly("sprint", "comments", p.Comments.Set && !p.Comments.Null && len(p.Comments.Value) > 0),
		cliOnly("sprint", "tasks", len(stringsValue(p.Tasks)) > 0),
	); err != nil {
		return nil, err
	}
	return []model.Op{model.CreateSprint{
		Nonce:       model.NewNonce(),
		Title:       title,
		Description: stringValue(p.Description),
		Labels:      sortedSet(stringsValue(p.Labels)),
	}}, nil
}

// projectDoc mirrors internal/cli's projectDTO field for field: the rendered
// project file matches `project show --json` pretty-printed. The sprints and
// tasks reverse indexes are cross-entity and cannot be computed from one
// project, so they render empty (like RenderTask's blocks).
type projectDoc struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	Labels      []string        `json:"labels"`
	Commits     []string        `json:"commits"`
	Comments    []ParsedComment `json:"comments"`
	Author      string          `json:"author"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	ClosedAt    *string         `json:"closed_at"`
	Sprints     []string        `json:"sprints"`
	Tasks       []string        `json:"tasks"`
}

// ParsedProject is the decoded form of a project file, mirroring projectDoc key
// for key. Every field is optional so a minimal new file parses; DiffProject
// treats unset fields as untouched.
type ParsedProject struct {
	ID          Field[string]          `json:"id"`
	Title       Field[string]          `json:"title"`
	Description Field[string]          `json:"description"`
	Status      Field[string]          `json:"status"`
	Labels      Field[[]string]        `json:"labels"`
	Commits     Field[[]string]        `json:"commits"`
	Comments    Field[[]ParsedComment] `json:"comments"`
	Author      Field[string]          `json:"author"`
	CreatedAt   Field[string]          `json:"created_at"`
	UpdatedAt   Field[string]          `json:"updated_at"`
	ClosedAt    Field[string]          `json:"closed_at"`
	Sprints     Field[[]string]        `json:"sprints"`
	Tasks       Field[[]string]        `json:"tasks"`
}

// RenderProject renders pr as the CLI's --json project document pretty-printed
// with 2-space indent and a trailing newline. The sprints and tasks reverse
// indexes render empty.
func RenderProject(pr model.Project) []byte {
	doc := projectDoc{
		ID:          string(pr.ID),
		Title:       pr.Title,
		Description: pr.Description,
		Status:      string(pr.Status),
		Labels:      render.EmptyNotNil(pr.Labels),
		Commits:     render.SHAStrings(pr.Commits),
		Comments:    renderComments(pr.Comments),
		Author:      string(pr.Author),
		CreatedAt:   render.RFC3339(pr.CreatedAt),
		UpdatedAt:   render.RFC3339(pr.UpdatedAt),
		ClosedAt:    render.OptTime(pr.ClosedAt),
		Sprints:     []string{},
		Tasks:       []string{},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("fusefs: encode project document: %v", err))
	}
	return append(data, '\n')
}

// ParseProject decodes a project file. Decoding is strict — the document must
// be a single JSON object, unknown keys at any depth and trailing data fail
// with ErrParse.
func ParseProject(data []byte) (ParsedProject, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ParsedProject{}, fmt.Errorf("%w: project document must be a JSON object", ErrParse)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var p ParsedProject
	if err := dec.Decode(&p); err != nil {
		return ParsedProject{}, fmt.Errorf("%w: %w", ErrParse, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return ParsedProject{}, fmt.Errorf("%w: trailing data after project document", ErrParse)
	}
	return p, nil
}

// DiffProject compares an edited project document against the snapshot it was
// rendered from and returns the ops that reproduce the edit. Title,
// description, status, and labels are editable; id, commits, comments, author,
// the sprints and tasks reverse indexes, and every timestamp are immutable —
// echoing them unchanged is fine, changing them fails with ErrImmutableField.
// Ops come out in a fixed order — set_title, set_description,
// set_project_status, label adds then removes sorted by value.
func DiffProject(base model.Project, p ParsedProject) ([]model.Op, error) {
	return diffWith(
		[]check{
			pin("id", p.ID, string(base.ID)),
			pinStrings("commits", p.Commits, render.SHAStrings(base.Commits)),
			pinComments(p.Comments, base.Comments),
			pin("author", p.Author, string(base.Author)),
			pin("created_at", p.CreatedAt, render.RFC3339(base.CreatedAt)),
			pin("updated_at", p.UpdatedAt, render.RFC3339(base.UpdatedAt)),
			pin("closed_at", p.ClosedAt, render.OptTimeString(base.ClosedAt)),
			pinStrings("sprints", p.Sprints, nil),
			pinStrings("tasks", p.Tasks, nil),
		},
		[]fieldDiff{
			scalar(p.Title, base.Title, setTitle),
			scalar(p.Description, base.Description, setDescription),
			enum(p.Status, base.Status, parseProjectStatus, func(s model.ProjectStatus) model.Op { return model.SetProjectStatus{Status: s} }),
			stringSet(p.Labels, base.Labels, addLabel, removeLabel),
		},
	)
}

// NewProject builds the create op for a brand-new project file. The title is
// required; commits, comments, and the sprints/tasks reverse indexes are set
// via the CLI, never via a new file; timestamps are informational and ignored.
func NewProject(p ParsedProject) ([]model.Op, error) {
	if p.ID.Set {
		return nil, fmt.Errorf("%w: id on a new project", ErrParse)
	}
	title := stringValue(p.Title)
	if title == "" {
		return nil, fmt.Errorf("%w: new project needs a title", ErrParse)
	}
	if p.Status.Set && model.ProjectStatus(stringValue(p.Status)) != model.ProjectActive {
		return nil, fmt.Errorf("%w: new project status %q, want active", ErrParse, stringValue(p.Status))
	}
	if err := errors.Join(
		cliOnly("project", "commits", len(stringsValue(p.Commits)) > 0),
		cliOnly("project", "comments", p.Comments.Set && !p.Comments.Null && len(p.Comments.Value) > 0),
		cliOnly("project", "sprints", len(stringsValue(p.Sprints)) > 0),
		cliOnly("project", "tasks", len(stringsValue(p.Tasks)) > 0),
	); err != nil {
		return nil, err
	}
	return []model.Op{model.CreateProject{
		Nonce:       model.NewNonce(),
		Title:       title,
		Description: stringValue(p.Description),
		Labels:      sortedSet(stringsValue(p.Labels)),
	}}, nil
}

const runbookStepFencePrefix = "<!-- cc-notes:step "

// runbookStepFence renders the viewer-invisible marker line carrying a step's
// short id, mirroring the log entry fence idiom.
func runbookStepFence(id string) string {
	return runbookStepFencePrefix + render.ShortWireID(id) + " -->"
}

// runbookKeys is the runbook frontmatter contract: id, title, status, labels,
// the non-empty anchor kinds, created, updated.
var runbookKeys = slices.Concat(
	[]fmKey[model.Runbook]{
		{key: "id", node: func(r model.Runbook) *yaml.Node { return scalarNode(string(r.ID)) }},
		{key: "title", node: func(r model.Runbook) *yaml.Node { return scalarNode(r.Title) }},
		{key: "status", node: func(r model.Runbook) *yaml.Node { return scalarNode(string(r.Status)) }},
		{key: "labels", node: func(r model.Runbook) *yaml.Node { return flowNode(r.Labels) }},
	},
	anchorKeys(func(r model.Runbook) []model.Anchor { return r.Anchors }),
	[]fmKey[model.Runbook]{
		{key: "created", node: func(r model.Runbook) *yaml.Node { return scalarNode(render.RFC3339(r.CreatedAt)) }},
		{key: "updated", node: func(r model.Runbook) *yaml.Node { return scalarNode(render.RFC3339(r.UpdatedAt)) }},
	},
)

// RenderRunbook renders rb as read-only markdown: the runbookKeys frontmatter,
// the description, a numbered "## Steps" section whose items each carry a
// viewer-invisible step-id marker and an optional fenced sh block, then a
// "## Runs" section with one summary line per run in fold order. The file is
// read-only — no ParseRunbook or DiffRunbook — so it carries no round-trip
// obligation. Output is deterministic byte for byte.
func RenderRunbook(rb model.Runbook) []byte {
	buf := renderFrontmatter(rb, runbookKeys)

	if rb.Description != "" {
		buf.WriteString(ensureTrailingNewline(rb.Description))
		buf.WriteString("\n")
	}

	buf.WriteString("## Steps\n\n")
	if len(rb.Steps) == 0 {
		buf.WriteString("_No steps._\n")
	}
	for i, s := range rb.Steps {
		buf.WriteString(runbookStepFence(s.ID))
		buf.WriteString("\n")
		fmt.Fprintf(buf, "%d. %s\n", i+1, s.Text)
		if s.Command != "" {
			buf.WriteString("\n```sh\n")
			buf.WriteString(ensureTrailingNewline(s.Command))
			buf.WriteString("```\n")
		}
		if i < len(rb.Steps)-1 {
			buf.WriteString("\n")
		}
	}

	buf.WriteString("\n## Runs\n\n")
	if len(rb.Runs) == 0 {
		buf.WriteString("_No runs yet._\n")
	}
	for _, r := range rb.Runs {
		buf.WriteString(runbookRunLine(r))
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

// runbookRunLine renders one run summary line: short id, status, runner, the
// started→finished window (an unfinished run reads "in progress"), the
// per-outcome step tally, and the served task when the run cites one.
func runbookRunLine(r model.RunbookRun) string {
	finished := render.OptTimeString(r.FinishedAt)
	if finished == "" {
		finished = "in progress"
	}
	var done, skipped, failed int
	for _, res := range r.Results {
		switch res.Status {
		case model.StepDone:
			done++
		case model.StepSkipped:
			skipped++
		case model.StepFailed:
			failed++
		}
	}
	line := fmt.Sprintf("- %s %s — %s, %s → %s, %d done / %d skipped / %d failed",
		render.ShortWireID(r.ID), r.Status, r.Runner, render.RFC3339(r.StartedAt), finished, done, skipped, failed)
	if r.Task != "" {
		line += fmt.Sprintf(" (task %s)", r.Task.Short())
	}
	return line
}

// investigationKeys is the investigation frontmatter contract: id, status,
// labels, the non-empty anchor kinds, then the ordered findings and their
// current dispositions.
var investigationKeys = slices.Concat(
	[]fmKey[model.Investigation]{
		{key: "id", node: func(i model.Investigation) *yaml.Node { return scalarNode(string(i.ID)) }},
		{key: "status", node: func(i model.Investigation) *yaml.Node { return scalarNode(string(i.Status)) }},
		{key: "labels", node: func(i model.Investigation) *yaml.Node { return flowNode(i.Tags) }},
	},
	anchorKeys(func(i model.Investigation) []model.Anchor { return i.Anchors }),
	[]fmKey[model.Investigation]{
		{key: "findings", node: func(i model.Investigation) *yaml.Node { return investigationFindingsNode(i.Findings) }},
		{key: "follow_ups", keep: func(i model.Investigation) bool { return len(i.FollowUps) > 0 }, node: func(i model.Investigation) *yaml.Node { return flowNode(render.IDStrings(i.FollowUps)) }},
	},
)

func investigationFindingsNode(findings []model.Finding) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, finding := range findings {
		m := &yaml.Node{Kind: yaml.MappingNode}
		m.Content = append(
			m.Content,
			scalarNode("id"), scalarNode(finding.ID),
			scalarNode("text"), scalarNode(finding.Text),
			scalarNode("status"), scalarNode(string(finding.Status)),
		)
		if finding.Note != "" {
			m.Content = append(m.Content, scalarNode("why"), scalarNode(finding.Note))
		}
		n.Content = append(n.Content, m)
	}
	return n
}

// RenderInvestigation renders inv as read-only markdown: structured
// frontmatter, the immutable premise, the chronological evidence timeline,
// attachment links, and the current verdict. The file is read-only — no
// ParseInvestigation or DiffInvestigation — so it carries no round-trip
// obligation. Output is deterministic byte for byte.
func RenderInvestigation(inv model.Investigation) []byte {
	buf := renderFrontmatter(inv, investigationKeys)
	fmt.Fprintf(buf, "# %s\n\n", inv.Title)
	buf.WriteString(ensureTrailingNewline(inv.Premise))
	buf.WriteString("\n## Timeline\n\n")
	if len(inv.Entries) == 0 && len(inv.Attachments) == 0 {
		buf.WriteString("_No entries._\n")
	}
	for _, entry := range inv.Entries {
		fmt.Fprintf(buf, "### %s — %s\n\n", render.RFC3339(entry.TS), entry.Author)
		buf.WriteString(ensureTrailingNewline(entry.Text))
		buf.WriteString("\n")
	}
	for _, attachment := range inv.Attachments {
		label := strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(attachment.Name)
		fmt.Fprintf(buf, "- [%s](../attachments/%s/%s)\n", label, inv.ID.Short(), url.PathEscape(attachment.Name))
	}

	if !bytes.HasSuffix(buf.Bytes(), []byte("\n\n")) {
		buf.WriteString("\n")
	}
	buf.WriteString("## Verdict\n\n### Root cause\n\n")
	if inv.RootCause == "" {
		buf.WriteString("_Not established._\n")
	} else {
		buf.WriteString(ensureTrailingNewline(inv.RootCause))
	}
	buf.WriteString("\n### Resolution\n\n")
	if inv.Body == "" {
		buf.WriteString("_Not recorded._\n")
	} else {
		buf.WriteString(ensureTrailingNewline(inv.Body))
	}
	buf.WriteString("\n### Fix commits\n\n")
	if len(inv.FixCommits) == 0 {
		buf.WriteString("_None._\n")
	}
	for _, sha := range inv.FixCommits {
		fmt.Fprintf(buf, "- %s\n", sha)
	}
	return buf.Bytes()
}

func cliOnly(kind, field string, set bool) error {
	if set {
		return fmt.Errorf("%w: %s on a new %s changes via the CLI", ErrParse, field, kind)
	}
	return nil
}

func parseCriterionStatus(status string) (model.CriterionStatus, error) {
	switch s := model.CriterionStatus(status); s {
	case model.CriterionPending, model.CriterionMet, model.CriterionFailed:
		return s, nil
	default:
		return "", fmt.Errorf("%w: criterion status %q", ErrParse, status)
	}
}

func parseSprintStatus(f Field[string]) (model.SprintStatus, error) {
	switch status := model.SprintStatus(stringValue(f)); status {
	case model.SprintPlanned, model.SprintActive, model.SprintCompleted, model.SprintCancelled:
		return status, nil
	default:
		return "", fmt.Errorf("%w: sprint status %q", ErrParse, stringValue(f))
	}
}

func parseProjectStatus(f Field[string]) (model.ProjectStatus, error) {
	switch status := model.ProjectStatus(stringValue(f)); status {
	case model.ProjectActive, model.ProjectCompleted, model.ProjectArchived, model.ProjectCancelled:
		return status, nil
	default:
		return "", fmt.Errorf("%w: project status %q", ErrParse, stringValue(f))
	}
}

// parseDate parses a rendered RFC3339 date field back to unix seconds. An unset
// field is the caller's concern; a null or empty value clears the date to 0.
func parseDate(f Field[string]) (int64, error) {
	if f.Null || f.Value == "" {
		return 0, nil
	}
	ts, err := time.Parse(time.RFC3339, f.Value)
	if err != nil {
		return 0, fmt.Errorf("%w: date %q", ErrParse, f.Value)
	}
	return ts.Unix(), nil
}

func parseStatus(f Field[string]) (model.Status, error) {
	switch status := model.Status(stringValue(f)); status {
	case model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled:
		return status, nil
	default:
		return "", fmt.Errorf("%w: status %q", ErrParse, stringValue(f))
	}
}

func parseType(f Field[string]) (model.TaskType, error) {
	switch taskType := model.TaskType(stringValue(f)); taskType {
	case model.TypeTask, model.TypeBug, model.TypeEpic, model.TypeQuestion:
		return taskType, nil
	default:
		return "", fmt.Errorf("%w: task type %q", ErrParse, stringValue(f))
	}
}

func parsePriority(f Field[int]) (model.Priority, error) {
	if f.Null || f.Value < 0 || f.Value > 3 {
		return 0, fmt.Errorf("%w: priority must be 0-3", ErrParse)
	}
	return model.Priority(f.Value), nil
}

func immutable(field string, f Field[string], want string) error {
	if f.Set && stringValue(f) != want {
		return fmt.Errorf("%w: %s", ErrImmutableField, field)
	}
	return nil
}

func immutableStrings(field string, f Field[[]string], want []string) error {
	if f.Set && !slices.Equal(stringsValue(f), want) {
		return fmt.Errorf("%w: %s", ErrImmutableField, field)
	}
	return nil
}

func immutableComments(f Field[[]ParsedComment], base []model.Comment) error {
	if !f.Set {
		return nil
	}
	claimed := f.Value
	if f.Null {
		claimed = nil
	}
	if !slices.Equal(claimed, renderComments(base)) {
		return fmt.Errorf("%w: comments", ErrImmutableField)
	}
	return nil
}

func renderComments(comments []model.Comment) []ParsedComment {
	out := make([]ParsedComment, len(comments))
	for i, c := range comments {
		out[i] = ParsedComment{Author: string(c.Author), TS: render.RFC3339(c.TS), Body: c.Body}
	}
	return out
}

func immutableWitness(f Field[[]ParsedWitness], base []model.AnchorWitness) error {
	if !f.Set {
		return nil
	}
	claimed := f.Value
	if f.Null {
		claimed = nil
	}
	if !slices.Equal(claimed, renderWitness(base)) {
		return fmt.Errorf("%w: witness", ErrImmutableField)
	}
	return nil
}

func renderWitness(base []model.AnchorWitness) []ParsedWitness {
	out := make([]ParsedWitness, len(base))
	for i, a := range base {
		out[i] = ParsedWitness{Kind: string(a.Anchor.Kind), Value: a.Anchor.Value, OID: string(a.OID)}
	}
	return out
}

func stringValue(f Field[string]) string {
	if f.Null {
		return ""
	}
	return f.Value
}

func stringsValue(f Field[[]string]) []string {
	if f.Null {
		return nil
	}
	return f.Value
}

func diffSets(have, want []string) (adds, removes []string) {
	haveSet := toSet(have)
	wantSet := toSet(want)
	for value := range wantSet {
		if !haveSet[value] {
			adds = append(adds, value)
		}
	}
	for value := range haveSet {
		if !wantSet[value] {
			removes = append(removes, value)
		}
	}
	slices.Sort(adds)
	slices.Sort(removes)
	return adds, removes
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func sortedSet(values []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(values)))
}

func scalarNode(value string) *yaml.Node {
	n := &yaml.Node{}
	n.SetString(value)
	return n
}

func flowNode(values []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, v := range values {
		n.Content = append(n.Content, scalarNode(v))
	}
	return n
}

func witnessNode(witness []model.AnchorWitness) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, w := range witness {
		m := &yaml.Node{Kind: yaml.MappingNode}
		m.Content = append(
			m.Content,
			scalarNode("kind"), scalarNode(string(w.Anchor.Kind)),
			scalarNode("value"), scalarNode(w.Anchor.Value),
			scalarNode("oid"), scalarNode(string(w.OID)),
		)
		n.Content = append(n.Content, m)
	}
	return n
}
