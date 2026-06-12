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
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yasyf/cc-notes/internal/model"
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
	{"branches", model.AnchorBranch},
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

// ParsedNote is the decoded form of a note file: the frontmatter keys plus
// the body below the closing delimiter. Every frontmatter field is optional
// so a minimal new file parses; DiffNote treats unset fields as untouched.
type ParsedNote struct {
	ID       Field[string]   `yaml:"id"`
	Title    Field[string]   `yaml:"title"`
	Tags     Field[[]string] `yaml:"tags"`
	Commits  Field[[]string] `yaml:"commits"`
	Paths    Field[[]string] `yaml:"paths"`
	Branches Field[[]string] `yaml:"branches"`
	Author   Field[string]   `yaml:"author"`
	Created  Field[string]   `yaml:"created"`
	Updated  Field[string]   `yaml:"updated"`
	Body     string          `yaml:"-"`
}

func (p ParsedNote) anchors(kind model.AnchorKind) Field[[]string] {
	switch kind {
	case model.AnchorCommit:
		return p.Commits
	case model.AnchorPath:
		return p.Paths
	case model.AnchorBranch:
		return p.Branches
	}
	panic("fusefs: unknown anchor kind " + string(kind))
}

// ParsedComment is one comment in a task document. TS stays the rendered
// RFC3339 string so an echoed comment compares exactly.
type ParsedComment struct {
	Author string `json:"author"`
	TS     string `json:"ts"`
	Body   string `json:"body"`
}

// ParsedTask is the decoded form of a task file, mirroring the CLI's --json
// DTO key for key. Every field is optional so a minimal new file parses;
// DiffTask treats unset fields as untouched.
type ParsedTask struct {
	ID          Field[string]          `json:"id"`
	Branch      Field[string]          `json:"branch"`
	Title       Field[string]          `json:"title"`
	Description Field[string]          `json:"description"`
	Type        Field[string]          `json:"type"`
	Status      Field[string]          `json:"status"`
	Priority    Field[int]             `json:"priority"`
	Assignee    Field[string]          `json:"assignee"`
	Labels      Field[[]string]        `json:"labels"`
	BlockedBy   Field[[]string]        `json:"blocked_by"`
	Blocks      Field[[]string]        `json:"blocks"`
	Parent      Field[string]          `json:"parent"`
	Comments    Field[[]ParsedComment] `json:"comments"`
	CreatedAt   Field[string]          `json:"created_at"`
	UpdatedAt   Field[string]          `json:"updated_at"`
	StartedAt   Field[string]          `json:"started_at"`
	ClosedAt    Field[string]          `json:"closed_at"`
}

// taskDoc mirrors internal/cli's taskDTO field for field: the rendered task
// file must stay byte-compatible with `task show --json` pretty-printed
// (TestRenderTaskMatchesCLIJSON pins it), so any change there lands here
// too.
type taskDoc struct {
	ID          string          `json:"id"`
	Branch      string          `json:"branch"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Priority    int             `json:"priority"`
	Assignee    *string         `json:"assignee"`
	Labels      []string        `json:"labels"`
	BlockedBy   []string        `json:"blocked_by"`
	Blocks      []string        `json:"blocks"`
	Parent      *string         `json:"parent"`
	Comments    []ParsedComment `json:"comments"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	StartedAt   *string         `json:"started_at"`
	ClosedAt    *string         `json:"closed_at"`
}

// RenderNote renders n as markdown with YAML frontmatter: fixed key order
// (id, title, tags, commits, paths, branches, author, created, updated),
// anchor values split by kind with empty kinds omitted, tags as a flow
// sequence, RFC3339 UTC timestamps, and the body verbatim below the closing
// delimiter. The output is deterministic byte for byte.
func RenderNote(n model.Note) []byte {
	fm := &yaml.Node{Kind: yaml.MappingNode}
	put := func(key string, value *yaml.Node) {
		fm.Content = append(fm.Content, scalarNode(key), value)
	}
	put("id", scalarNode(string(n.ID)))
	put("title", scalarNode(n.Title))
	put("tags", flowNode(n.Tags))
	for _, ak := range anchorKinds {
		if values := anchorValues(n.Anchors, ak.kind); len(values) > 0 {
			put(ak.key, flowNode(values))
		}
	}
	put("author", scalarNode(string(n.Author)))
	put("created", scalarNode(stamp(n.CreatedAt)))
	put("updated", scalarNode(stamp(n.UpdatedAt)))

	var buf bytes.Buffer
	buf.WriteString(delimiter)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		panic(fmt.Sprintf("fusefs: encode note frontmatter: %v", err))
	}
	if err := enc.Close(); err != nil {
		panic(fmt.Sprintf("fusefs: close frontmatter encoder: %v", err))
	}
	buf.WriteString(delimiter)
	buf.WriteString(n.Body)
	return buf.Bytes()
}

// ParseNote decodes a note file: YAML frontmatter between --- delimiters,
// body verbatim below. Decoding is strict — a missing delimiter or an
// unknown frontmatter key fails with ErrParse.
func ParseNote(data []byte) (ParsedNote, error) {
	fm, body, err := splitFrontmatter(string(data))
	if err != nil {
		return ParsedNote{}, err
	}
	var p ParsedNote
	dec := yaml.NewDecoder(strings.NewReader(fm))
	dec.KnownFields(true)
	switch err := dec.Decode(&p); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return ParsedNote{}, fmt.Errorf("%w: %v", ErrParse, err)
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

// DiffNote compares an edited note document against the snapshot it was
// rendered from and returns the ops that reproduce the edit. Title, body,
// tags, and anchors are editable; id, author, and created are immutable —
// echoing them unchanged is fine, changing them fails with
// ErrImmutableField. The updated stamp is informational: editors save the
// stale one, so any value is accepted and never diffed. Ops come out in a
// fixed order — set_title, set_body, tag adds then removes, anchor adds
// then removes per kind — each group sorted by value.
func DiffNote(base model.Note, p ParsedNote) ([]model.Op, error) {
	if err := errors.Join(
		immutable("id", p.ID, string(base.ID)),
		immutable("author", p.Author, string(base.Author)),
		immutable("created", p.Created, stamp(base.CreatedAt)),
	); err != nil {
		return nil, err
	}
	var ops []model.Op
	if p.Title.Set {
		if title := stringValue(p.Title); title != base.Title {
			ops = append(ops, model.SetTitle{Title: title})
		}
	}
	if p.Body != base.Body {
		ops = append(ops, model.SetBody{Body: p.Body})
	}
	if p.Tags.Set {
		adds, removes := diffSets(base.Tags, stringsValue(p.Tags))
		for _, tag := range adds {
			ops = append(ops, model.AddTag{Tag: tag})
		}
		for _, tag := range removes {
			ops = append(ops, model.RemoveTag{Tag: tag})
		}
	}
	for _, ak := range anchorKinds {
		field := p.anchors(ak.kind)
		if !field.Set {
			continue
		}
		adds, removes := diffSets(anchorValues(base.Anchors, ak.kind), stringsValue(field))
		for _, value := range adds {
			ops = append(ops, model.AddAnchor{Anchor: model.Anchor{Kind: ak.kind, Value: value}})
		}
		for _, value := range removes {
			ops = append(ops, model.RemoveAnchor{Anchor: model.Anchor{Kind: ak.kind, Value: value}})
		}
	}
	return ops, nil
}

// NewNote builds the create op for a brand-new note file. The title comes
// from the frontmatter, falling back to the first "# " heading in the body;
// neither is an error. A new file claiming an id is a contradiction and
// fails; author, created, and updated are informational and ignored.
func NewNote(p ParsedNote) ([]model.Op, error) {
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

// RenderTask renders t as the CLI's --json document pretty-printed with
// 2-space indent and a trailing newline, byte-compatible with
// `task show --json`. Blocks is a derived cross-entity index this layer
// cannot compute from one task, so it renders empty; DiffTask pins it to
// empty in turn.
func RenderTask(t model.Task) []byte {
	doc := taskDoc{
		ID:          string(t.ID),
		Branch:      string(t.Branch),
		Title:       t.Title,
		Description: t.Description,
		Type:        string(t.Type),
		Status:      string(t.Status),
		Priority:    int(t.Priority),
		Assignee:    optString(string(t.Assignee)),
		Labels:      emptyNotNil(t.Labels),
		BlockedBy:   idStrings(t.BlockedBy),
		Blocks:      []string{},
		Parent:      optString(string(t.Parent)),
		Comments:    renderComments(t.Comments),
		CreatedAt:   stamp(t.CreatedAt),
		UpdatedAt:   stamp(t.UpdatedAt),
		StartedAt:   optStamp(t.StartedAt),
		ClosedAt:    optStamp(t.ClosedAt),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("fusefs: encode task document: %v", err))
	}
	return append(data, '\n')
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
		return ParsedTask{}, fmt.Errorf("%w: %v", ErrParse, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return ParsedTask{}, fmt.Errorf("%w: trailing data after task document", ErrParse)
	}
	return p, nil
}

// DiffTask compares an edited task document against the snapshot it was
// rendered from and returns the ops that reproduce the edit. Title,
// description, status, priority, and labels are editable; id, branch, type,
// assignee, blocked_by, blocks, parent, comments, and every timestamp are
// immutable — echoing them unchanged is fine, changing them fails with
// ErrImmutableField (coordination fields change via the CLI, not the
// filesystem). Ops come out in a fixed order — set_title, set_description,
// set_status, set_priority, label adds then removes sorted by value.
func DiffTask(base model.Task, p ParsedTask) ([]model.Op, error) {
	if err := errors.Join(
		immutable("id", p.ID, string(base.ID)),
		immutable("branch", p.Branch, string(base.Branch)),
		immutable("type", p.Type, string(base.Type)),
		immutable("assignee", p.Assignee, string(base.Assignee)),
		immutableStrings("blocked_by", p.BlockedBy, idStrings(base.BlockedBy)),
		immutableStrings("blocks", p.Blocks, nil),
		immutable("parent", p.Parent, string(base.Parent)),
		immutableComments(p.Comments, base.Comments),
		immutable("created_at", p.CreatedAt, stamp(base.CreatedAt)),
		immutable("updated_at", p.UpdatedAt, stamp(base.UpdatedAt)),
		immutable("started_at", p.StartedAt, stampOrEmpty(base.StartedAt)),
		immutable("closed_at", p.ClosedAt, stampOrEmpty(base.ClosedAt)),
	); err != nil {
		return nil, err
	}
	var ops []model.Op
	if p.Title.Set {
		if title := stringValue(p.Title); title != base.Title {
			ops = append(ops, model.SetTitle{Title: title})
		}
	}
	if p.Description.Set {
		if description := stringValue(p.Description); description != base.Description {
			ops = append(ops, model.SetDescription{Description: description})
		}
	}
	if p.Status.Set {
		status, err := parseStatus(p.Status)
		if err != nil {
			return nil, err
		}
		if status != base.Status {
			ops = append(ops, model.SetStatus{Status: status})
		}
	}
	if p.Priority.Set {
		priority, err := parsePriority(p.Priority)
		if err != nil {
			return nil, err
		}
		if priority != base.Priority {
			ops = append(ops, model.SetPriority{Priority: priority})
		}
	}
	if p.Labels.Set {
		adds, removes := diffSets(base.Labels, stringsValue(p.Labels))
		for _, label := range adds {
			ops = append(ops, model.AddLabel{Label: label})
		}
		for _, label := range removes {
			ops = append(ops, model.RemoveLabel{Label: label})
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
		cliOnly("assignee", stringValue(p.Assignee) != ""),
		cliOnly("blocked_by", len(stringsValue(p.BlockedBy)) > 0),
		cliOnly("blocks", len(stringsValue(p.Blocks)) > 0),
		cliOnly("parent", stringValue(p.Parent) != ""),
		cliOnly("comments", p.Comments.Set && !p.Comments.Null && len(p.Comments.Value) > 0),
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

func cliOnly(field string, set bool) error {
	if set {
		return fmt.Errorf("%w: %s on a new task changes via the CLI", ErrParse, field)
	}
	return nil
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
		out[i] = ParsedComment{Author: string(c.Author), TS: stamp(c.TS), Body: c.Body}
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
	haveSet := stringSet(have)
	wantSet := stringSet(want)
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

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func sortedSet(values []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(values)))
}

func anchorValues(anchors []model.Anchor, kind model.AnchorKind) []string {
	var values []string
	for _, a := range anchors {
		if a.Kind == kind {
			values = append(values, a.Value)
		}
	}
	return values
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

func stamp(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) }

func stampOrEmpty(ts int64) string {
	if ts == 0 {
		return ""
	}
	return stamp(ts)
}

func optStamp(ts int64) *string {
	if ts == 0 {
		return nil
	}
	s := stamp(ts)
	return &s
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func idStrings(ids []model.EntityID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func emptyNotNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}
