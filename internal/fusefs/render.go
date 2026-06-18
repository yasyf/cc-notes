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
	{"dirs", model.AnchorDir},
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
	ID             Field[string]          `yaml:"id"`
	Title          Field[string]          `yaml:"title"`
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
	Body           string                 `yaml:"-"`
}

// ParsedWitness is one entry in a note document's witness sequence: the anchor
// kind and value plus the git oid recorded for it at verify time.
type ParsedWitness struct {
	Kind  string `yaml:"kind"`
	Value string `yaml:"value"`
	OID   string `yaml:"oid"`
}

func (p ParsedNote) anchors(kind model.AnchorKind) Field[[]string] {
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

// ParsedComment is one comment in a task document. TS stays the rendered
// RFC3339 string so an echoed comment compares exactly.
type ParsedComment struct {
	Author string `json:"author"`
	TS     string `json:"ts"`
	Body   string `json:"body"`
}

// ParsedCriterion is one acceptance criterion in a task document, mirroring the
// CLI's criterionDTO key for key.
type ParsedCriterion struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Script string `json:"script"`
	Status string `json:"status"`
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

// RenderNote renders n as markdown with YAML frontmatter: fixed key order
// (id, title, tags, commits, paths, dirs, branches, author, created, updated,
// verified_at, verified_by, verified_commit, witness, superseded_by), anchor
// values split by kind with empty kinds omitted, tags as a flow sequence,
// RFC3339 UTC timestamps, and the body verbatim below the closing delimiter.
// The verification keys are omitted when empty so a never-verified note stays
// clean; witness renders as a block sequence in stored anchor order, never
// re-sorted. The output is deterministic byte for byte.
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
	if n.VerifiedAt != 0 {
		put("verified_at", scalarNode(stamp(n.VerifiedAt)))
	}
	if n.VerifiedBy != "" {
		put("verified_by", scalarNode(string(n.VerifiedBy)))
	}
	if n.VerifiedCommit != "" {
		put("verified_commit", scalarNode(string(n.VerifiedCommit)))
	}
	if len(n.Witness) > 0 {
		put("witness", witnessNode(n.Witness))
	}
	if len(n.SupersededBy) > 0 {
		put("superseded_by", flowNode(idStrings(n.SupersededBy)))
	}

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
// tags, and anchors are editable; id, author, created, and the verification
// fields (verified_at, verified_by, verified_commit, witness, superseded_by)
// are immutable — echoing them unchanged is fine, changing them fails with
// ErrImmutableField (verification state changes via the CLI, not the
// filesystem). The updated stamp is informational: editors save the stale
// one, so any value is accepted and never diffed. Ops come out in a fixed
// order — set_title, set_body, tag adds then removes, anchor adds then
// removes per kind — each group sorted by value.
func DiffNote(base model.Note, p ParsedNote) ([]model.Op, error) {
	if err := errors.Join(
		immutable("id", p.ID, string(base.ID)),
		immutable("author", p.Author, string(base.Author)),
		immutable("created", p.Created, stamp(base.CreatedAt)),
		immutable("verified_at", p.VerifiedAt, stampOrEmpty(base.VerifiedAt)),
		immutable("verified_by", p.VerifiedBy, string(base.VerifiedBy)),
		immutable("verified_commit", p.VerifiedCommit, string(base.VerifiedCommit)),
		immutableStrings("superseded_by", p.SupersededBy, idStrings(base.SupersededBy)),
		immutableWitness(p.Witness, base.Witness),
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
		ID:           string(t.ID),
		Branch:       string(t.Branch),
		Title:        t.Title,
		Description:  t.Description,
		Type:         string(t.Type),
		Status:       string(t.Status),
		Priority:     int(t.Priority),
		Assignee:     optString(string(t.Assignee)),
		Labels:       emptyNotNil(t.Labels),
		BlockedBy:    idStrings(t.BlockedBy),
		Blocks:       []string{},
		Parent:       optString(string(t.Parent)),
		Comments:     renderComments(t.Comments),
		Commits:      shaStrings(t.Commits),
		Lease:        leaseDoc{Holder: optString(string(t.Assignee)), Heartbeat: optStamp(t.HeartbeatAt)},
		CreatedAt:    stamp(t.CreatedAt),
		UpdatedAt:    stamp(t.UpdatedAt),
		StartedAt:    optStamp(t.StartedAt),
		ClosedAt:     optStamp(t.ClosedAt),
		Sprint:       optString(string(t.Sprint)),
		Project:      optString(string(t.Project)),
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
		out[i] = ParsedCriterion{ID: c.ID, Text: c.Text, Script: c.Script, Status: string(c.Status)}
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
		return ParsedTask{}, fmt.Errorf("%w: %v", ErrParse, err)
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
// the CLI, not the filesystem). closed_forced is informational, like the
// updated stamp: parsed, never diffed. Ops come out in a fixed order —
// set_title, set_description, set_status, set_priority, label adds then removes
// sorted by value, then the criteria ops (see diffCriteria).
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
		immutableStrings("commits", p.Commits, shaStrings(base.Commits)),
		immutable("lease.holder", p.Lease.Holder, string(base.Assignee)),
		immutable("lease.heartbeat", p.Lease.Heartbeat, stampOrEmpty(base.HeartbeatAt)),
		immutable("created_at", p.CreatedAt, stamp(base.CreatedAt)),
		immutable("updated_at", p.UpdatedAt, stamp(base.UpdatedAt)),
		immutable("started_at", p.StartedAt, stampOrEmpty(base.StartedAt)),
		immutable("closed_at", p.ClosedAt, stampOrEmpty(base.ClosedAt)),
		immutable("sprint", p.Sprint, string(base.Sprint)),
		immutable("project", p.Project, string(base.Project)),
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
	criteriaOps, err := diffCriteria(base.Criteria, p.Criteria)
	if err != nil {
		return nil, err
	}
	ops = append(ops, criteriaOps...)
	return ops, nil
}

// diffCriteria diffs an edited criteria array against the base, matching by id.
// A parsed entry whose id matches a base criterion emits set_criterion_text /
// set_criterion_status / set_criterion_script for each changed field; an entry
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
		if pc.Status != string(bc.Status) {
			status, err := parseCriterionStatus(pc.Status)
			if err != nil {
				return nil, err
			}
			ops = append(ops, model.SetCriterionStatus{ID: id, Status: status})
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
		Project:     optString(string(s.Project)),
		Title:       s.Title,
		Description: s.Description,
		Status:      string(s.Status),
		StartDate:   optStamp(s.StartDate),
		EndDate:     optStamp(s.EndDate),
		Labels:      emptyNotNil(s.Labels),
		Commits:     shaStrings(s.Commits),
		Comments:    renderComments(s.Comments),
		Author:      string(s.Author),
		CreatedAt:   stamp(s.CreatedAt),
		UpdatedAt:   stamp(s.UpdatedAt),
		StartedAt:   optStamp(s.StartedAt),
		ClosedAt:    optStamp(s.ClosedAt),
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
		return ParsedSprint{}, fmt.Errorf("%w: %v", ErrParse, err)
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
	if err := errors.Join(
		immutable("id", p.ID, string(base.ID)),
		immutable("project", p.Project, string(base.Project)),
		immutableStrings("commits", p.Commits, shaStrings(base.Commits)),
		immutableComments(p.Comments, base.Comments),
		immutable("author", p.Author, string(base.Author)),
		immutable("created_at", p.CreatedAt, stamp(base.CreatedAt)),
		immutable("updated_at", p.UpdatedAt, stamp(base.UpdatedAt)),
		immutable("started_at", p.StartedAt, stampOrEmpty(base.StartedAt)),
		immutable("closed_at", p.ClosedAt, stampOrEmpty(base.ClosedAt)),
		immutableStrings("tasks", p.Tasks, nil),
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
		status, err := parseSprintStatus(p.Status)
		if err != nil {
			return nil, err
		}
		if status != base.Status {
			ops = append(ops, model.SetSprintStatus{Status: status})
		}
	}
	if p.StartDate.Set {
		date, err := parseDate(p.StartDate)
		if err != nil {
			return nil, err
		}
		if date != base.StartDate {
			ops = append(ops, model.SetStartDate{Date: date})
		}
	}
	if p.EndDate.Set {
		date, err := parseDate(p.EndDate)
		if err != nil {
			return nil, err
		}
		if date != base.EndDate {
			ops = append(ops, model.SetEndDate{Date: date})
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
		Labels:      emptyNotNil(pr.Labels),
		Commits:     shaStrings(pr.Commits),
		Comments:    renderComments(pr.Comments),
		Author:      string(pr.Author),
		CreatedAt:   stamp(pr.CreatedAt),
		UpdatedAt:   stamp(pr.UpdatedAt),
		ClosedAt:    optStamp(pr.ClosedAt),
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
		return ParsedProject{}, fmt.Errorf("%w: %v", ErrParse, err)
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
	if err := errors.Join(
		immutable("id", p.ID, string(base.ID)),
		immutableStrings("commits", p.Commits, shaStrings(base.Commits)),
		immutableComments(p.Comments, base.Comments),
		immutable("author", p.Author, string(base.Author)),
		immutable("created_at", p.CreatedAt, stamp(base.CreatedAt)),
		immutable("updated_at", p.UpdatedAt, stamp(base.UpdatedAt)),
		immutable("closed_at", p.ClosedAt, stampOrEmpty(base.ClosedAt)),
		immutableStrings("sprints", p.Sprints, nil),
		immutableStrings("tasks", p.Tasks, nil),
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
		status, err := parseProjectStatus(p.Status)
		if err != nil {
			return nil, err
		}
		if status != base.Status {
			ops = append(ops, model.SetProjectStatus{Status: status})
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
		out[i] = ParsedComment{Author: string(c.Author), TS: stamp(c.TS), Body: c.Body}
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

func witnessNode(witness []model.AnchorWitness) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, w := range witness {
		m := &yaml.Node{Kind: yaml.MappingNode}
		m.Content = append(m.Content,
			scalarNode("kind"), scalarNode(string(w.Anchor.Kind)),
			scalarNode("value"), scalarNode(w.Anchor.Value),
			scalarNode("oid"), scalarNode(string(w.OID)),
		)
		n.Content = append(n.Content, m)
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

func shaStrings(shas []model.SHA) []string {
	out := make([]string, 0, len(shas))
	for _, s := range shas {
		out = append(out, string(s))
	}
	return out
}

func emptyNotNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}
