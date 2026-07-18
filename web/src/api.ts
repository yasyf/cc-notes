// Wire types mirror internal/viz/types.go, the entity-detail DTOs, and the
// folded entity snapshots in github.com/yasyf/cc-notes/model: snake_case JSON
// keys and unix-second integer times. The Go side marshals a nil slice or nil
// map as JSON null, so the Raw* types below type those fields honestly
// (array/map | null) and the fetchers normalize every such field to a non-null
// [] or {} before it reaches component code. Only genuinely-optional pointers
// (a lane's fork/merge/tip) stay null by design.
//
// The one exception is a folded entity Snapshot (carried on EntityDetail and in
// the /api/entities buckets): it is canonical model JSON — the storage format —
// so it is passed through verbatim, never normalized, and its own omitempty/nil
// fields stay exactly as the Go marshaler emitted them.

export interface RepoInfo {
  root: string;
  trunk: string;
  head: string;
  generated_at: string;
  truncated: boolean;
}

export interface Point {
  sha: string;
  time: number;
}

export interface MergePoint {
  sha: string;
  time: number;
  into: string;
  kind: string;
}

// Lane carries no slice or map fields — only the by-design nullable fork, merge,
// and tip pointers — so it needs no normalization.
export interface Lane {
  name: string;
  parent: string;
  fork: Point | null;
  merge: MergePoint | null;
  status: string;
  inferred: boolean;
  tip: Point | null;
  start: number;
  end: number;
  commits: number;
}

export interface EntityRef {
  kind: string;
  id: string;
  short: string;
  title: string;
}

export interface Event {
  entity: EntityRef;
  type: string;
  time: number;
  branch: string;
  sha: string;
  detail: Record<string, string>;
}

export interface EntitySummary {
  kind: string;
  id: string;
  short: string;
  title: string;
  status?: string;
  branch?: string;
  assignee?: string;
  started_at?: number;
  closed_at?: number;
  sprint?: string;
  project?: string;
  verified_at?: number;
  stale?: boolean;
  superseded?: boolean;
  start_date?: number;
  end_date?: number;
}

export interface Graph {
  repo: RepoInfo;
  lanes: Lane[];
  events: Event[];
  entities: EntitySummary[];
}

// CommitPage is one commit in a DAG page: identity + parents, the attributed
// lane (null when unclaimed), the cc-task ids its trailer names, and the
// lifecycle events landing on it. Mirrors internal/viz.commitPage; author is a
// bare actor string. parents/tasks/events are non-null after normalization.
export interface CommitPage {
  sha: string;
  parents: string[];
  author: string;
  time: number;
  summary: string;
  branch: string | null;
  tasks: string[];
  events: Event[];
}

// CommitsPage is the /api/commits payload: a page of commits newest first, the
// cursor to pass as ?before for the next (older) page, and whether the walk hit
// the DAG horizon. Mirrors internal/viz.commitsResponse.
export interface CommitsPage {
  commits: CommitPage[];
  next_before: string | null;
  truncated: boolean;
}

// TrailValue is one value in a change delta: a scalar (string, unix-second
// number, or bool), null (a create entry's pre-image, or a cleared time field),
// or a folded sub-object (an attachment, anchor, comment, or criterion) carried
// as its canonical JSON object. Mirrors the Go `any` the trail change holds.
export type TrailValue = string | number | boolean | null | Record<string, unknown>;

// TrailChange is one field delta in an entity's change trail: a scalar carries
// from→to, a set carries added/removed. Mirrors internal/viz.trailChange.
export interface TrailChange {
  field: string;
  scalar: boolean;
  from: TrailValue;
  to: TrailValue;
  added: TrailValue[];
  removed: TrailValue[];
}

// TrailEntry is one change-trail commit. Mirrors internal/viz.trailEntry.
export interface TrailEntry {
  sha: string;
  author: string;
  session?: string;
  time: number;
  lamport: number;
  kind: string;
  covers: number;
  changes: TrailChange[];
}

// The interfaces below mirror the folded entity snapshots in
// github.com/yasyf/cc-notes/model: snake_case keys, unix-second integer times,
// and `?` exactly where the Go struct tag carries omitempty. They ride on the
// /api/entity and /api/entities payloads and are passed through verbatim — the
// canonical model JSON is the storage format, so the fetchers never rewrite it.

// Anchor pins a note/doc/log to a repo location. Mirrors model.Anchor.
export interface Anchor {
  kind: string;
  value: string;
}

// AnchorWitness records the git oid of an anchor's content at verify time.
// Mirrors model.AnchorWitness.
export interface AnchorWitness {
  anchor: Anchor;
  oid: string;
}

// Comment is one append-only comment on a task, sprint, or project; ts is unix
// seconds. Mirrors model.Comment.
export interface Comment {
  author: string;
  ts: number;
  body: string;
}

// LogEntry is one append-only entry in a log; ts is unix seconds. Mirrors
// model.LogEntry.
export interface LogEntry {
  author: string;
  ts: number;
  text: string;
}

// Criterion is one structured acceptance criterion on a task. Mirrors
// model.Criterion.
export interface Criterion {
  id: string;
  text: string;
  script: string;
  status: string;
}

// Finding is one suspect hypothesis or review finding under test within an
// investigation. Mirrors model.Finding.
export interface Finding {
  id: string;
  text: string;
  status: string;
  note?: string;
}

// Attachment is one named large-content reference; size is bytes. Fetch its
// bytes via blobURL(oid, name). Mirrors model.Attachment.
export interface Attachment {
  name: string;
  oid: string;
  size: number;
}

// NoteSnapshot is the folded snapshot of a note. Mirrors model.Note.
export interface NoteSnapshot {
  id: string;
  title: string;
  body: string;
  tags: string[];
  anchors: Anchor[];
  author: string;
  created_at: number;
  updated_at: number;
  deleted: boolean;
  verified_at: number;
  verified_by: string;
  verified_commit: string;
  witness: AnchorWitness[];
  superseded_by: string[];
  stale_at: number;
  stale_by: string;
  stale_reason: string;
  head: string;
  attachments?: Attachment[];
}

// DocSnapshot is the folded snapshot of a doc: a Note plus the free-text `when`
// trigger. Mirrors model.Doc.
export interface DocSnapshot {
  id: string;
  title: string;
  body: string;
  when: string;
  tags: string[];
  anchors: Anchor[];
  author: string;
  created_at: number;
  updated_at: number;
  deleted: boolean;
  verified_at: number;
  verified_by: string;
  verified_commit: string;
  witness: AnchorWitness[];
  superseded_by: string[];
  stale_at: number;
  stale_by: string;
  stale_reason: string;
  head: string;
  attachments?: Attachment[];
}

// LogSnapshot is the folded snapshot of a log. Mirrors model.Log.
export interface LogSnapshot {
  id: string;
  title: string;
  entries: LogEntry[];
  tags: string[];
  anchors: Anchor[];
  author: string;
  created_at: number;
  updated_at: number;
  deleted: boolean;
  head: string;
  attachments?: Attachment[];
}

// TaskSnapshot is the folded snapshot of a task. Mirrors model.Task.
export interface TaskSnapshot {
  id: string;
  branch: string;
  title: string;
  description: string;
  type: string;
  status: string;
  priority: number;
  assignee: string;
  heartbeat_at: number;
  heartbeat_lamport: number;
  labels: string[];
  blocked_by: string[];
  parent: string;
  comments: Comment[];
  created_at: number;
  updated_at: number;
  started_at: number;
  closed_at: number;
  commits: string[];
  head: string;
  sprint: string;
  project: string;
  criteria: Criterion[];
}

// SprintSnapshot is the folded snapshot of a sprint. Mirrors model.Sprint.
export interface SprintSnapshot {
  id: string;
  project: string;
  title: string;
  description: string;
  status: string;
  start_date: number;
  end_date: number;
  labels: string[];
  commits: string[];
  comments: Comment[];
  author: string;
  created_at: number;
  updated_at: number;
  started_at: number;
  closed_at: number;
  head: string;
}

// ProjectSnapshot is the folded snapshot of a project. Mirrors model.Project.
export interface ProjectSnapshot {
  id: string;
  title: string;
  description: string;
  status: string;
  labels: string[];
  commits: string[];
  comments: Comment[];
  author: string;
  created_at: number;
  updated_at: number;
  closed_at: number;
  head: string;
}

// RunbookStepSnapshot is one ordered step of a runbook. Mirrors model.RunbookStep.
export interface RunbookStepSnapshot {
  id: string;
  text: string;
  command: string;
  position: string;
}

// RunbookStepResultSnapshot is the recorded outcome of one step within a run; ts
// is unix seconds. Mirrors model.RunbookStepResult.
export interface RunbookStepResultSnapshot {
  step_id: string;
  status: string;
  note: string;
  actor: string;
  ts: number;
}

// RunbookRunSnapshot is one tracked execution of a runbook. results is an ordered
// list, keyed by step within the run. Mirrors model.RunbookRun.
export interface RunbookRunSnapshot {
  id: string;
  task: string;
  status: string;
  runner: string;
  started_at: number;
  finished_at: number;
  results: RunbookStepResultSnapshot[];
}

// RunbookSnapshot is the folded snapshot of a runbook. Mirrors model.Runbook.
export interface RunbookSnapshot {
  id: string;
  title: string;
  description: string;
  status: string;
  steps: RunbookStepSnapshot[];
  runs: RunbookRunSnapshot[];
  labels: string[];
  comments: Comment[];
  author: string;
  created_at: number;
  updated_at: number;
  archived_at: number;
  head: string;
}

// InvestigationSnapshot is the folded snapshot of an investigation. Mirrors
// model.Investigation.
export interface InvestigationSnapshot {
  id: string;
  title: string;
  premise: string;
  body: string;
  status: string;
  root_cause: string;
  findings: Finding[];
  entries: LogEntry[];
  follow_ups: string[];
  fix_commits: string[];
  commits: string[];
  tags: string[];
  anchors: Anchor[];
  superseded_by: string[];
  author: string;
  created_at: number;
  updated_at: number;
  closed_at: number;
  closed_by: string;
  deleted: boolean;
  head: string;
  attachments?: Attachment[];
}

// ProjectedRunStep is one current step with the run's recorded status/note, or
// "pending" when the run has no result for it.
export interface ProjectedRunStep {
  stepId: string;
  text: string;
  command: string;
  status: string;
  note: string;
}

const runStepPending = "pending";

// projectRunSteps projects a run's recorded results onto the runbook's current
// steps in procedure order: every current step once, "pending" when absent;
// results for removed steps are omitted. Mirrors the CLI's newRunbookRunDTO.
export function projectRunSteps(
  steps: RunbookStepSnapshot[],
  run: RunbookRunSnapshot,
): ProjectedRunStep[] {
  const byStep = new Map(run.results.map((r) => [r.step_id, r]));
  return steps.map((step) => {
    const res = byStep.get(step.id);
    return {
      stepId: step.id,
      text: step.text,
      command: step.command,
      status: res ? res.status : runStepPending,
      note: res ? res.note : "",
    };
  });
}

// Snapshot is the full folded entity carried on an EntityDetail and in the
// /api/entities buckets. Discriminate it by the summary.kind the caller already
// holds (note | doc | log | task | sprint | project | runbook | investigation) — the snapshots
// carry no intrinsic tag. Mirrors model.Snapshot.
export type Snapshot =
  | NoteSnapshot
  | DocSnapshot
  | LogSnapshot
  | TaskSnapshot
  | SprintSnapshot
  | ProjectSnapshot
  | RunbookSnapshot
  | InvestigationSnapshot;

// EntityDetail is the /api/entity/{kind}/{id} payload: the legend summary, the
// full folded snapshot, and the change trail, oldest first. Mirrors
// internal/viz.entityResponse.
export interface EntityDetail {
  summary: EntitySummary;
  snapshot: Snapshot;
  trail: TrailEntry[];
}

// StateResponse is the /api/entities payload: every live entity's full folded
// snapshot, bucketed by kind. Each bucket marshals as null when its Go slice is
// nil and is normalized to []. Mirrors the /api/entities response in
// internal/viz.
export interface StateResponse {
  notes: NoteSnapshot[];
  docs: DocSnapshot[];
  logs: LogSnapshot[];
  tasks: TaskSnapshot[];
  sprints: SprintSnapshot[];
  projects: ProjectSnapshot[];
  runbooks: RunbookSnapshot[];
  investigations: InvestigationSnapshot[];
}

// RawEvent is Event as it arrives on the wire: detail is a Go map that marshals
// as null when nil.
interface RawEvent extends Omit<Event, "detail"> {
  detail: Record<string, string> | null;
}

// RawGraph is Graph as it arrives on the wire: the three top-level slices marshal
// as null when nil, and each event's detail map likewise.
interface RawGraph {
  repo: RepoInfo;
  lanes: Lane[] | null;
  events: RawEvent[] | null;
  entities: EntitySummary[] | null;
}

// RawCommitPage is CommitPage as it arrives on the wire: parents, tasks, and
// events marshal as null when the Go slice is nil (and each event's detail map
// likewise, via RawEvent); branch is nullable by design.
interface RawCommitPage extends Omit<CommitPage, "parents" | "tasks" | "events"> {
  parents: string[] | null;
  tasks: string[] | null;
  events: RawEvent[] | null;
}

// RawCommitsPage is CommitsPage as it arrives on the wire: the commits slice
// marshals as null for a repo with no commits in the window.
interface RawCommitsPage {
  commits: RawCommitPage[] | null;
  next_before: string | null;
  truncated: boolean;
}

// RawTrailChange is TrailChange as it arrives on the wire: added and removed are
// Go slices that marshal as null when nil (a scalar change carries neither).
interface RawTrailChange extends Omit<TrailChange, "added" | "removed"> {
  added: TrailValue[] | null;
  removed: TrailValue[] | null;
}

// RawTrailEntry is TrailEntry as it arrives on the wire: changes marshals as null
// when nil.
interface RawTrailEntry extends Omit<TrailEntry, "changes"> {
  changes: RawTrailChange[] | null;
}

// RawEntityDetail is EntityDetail as it arrives on the wire: trail marshals as
// null when nil. snapshot is passed through verbatim — its own nil slices are
// canonical model JSON, not normalized here.
interface RawEntityDetail {
  summary: EntitySummary;
  snapshot: Snapshot;
  trail: RawTrailEntry[] | null;
}

// RawStateResponse is StateResponse as it arrives on the wire: every kind bucket
// is a Go slice that marshals as null when nil. The snapshots inside are passed
// through verbatim.
interface RawStateResponse {
  notes: NoteSnapshot[] | null;
  docs: DocSnapshot[] | null;
  logs: LogSnapshot[] | null;
  tasks: TaskSnapshot[] | null;
  sprints: SprintSnapshot[] | null;
  projects: ProjectSnapshot[] | null;
  runbooks: RunbookSnapshot[] | null;
  investigations: InvestigationSnapshot[] | null;
}

// normalizeGraph fills every nil slice with [] and every nil detail map with {},
// so downstream code sees non-null arrays and maps.
export function normalizeGraph(raw: RawGraph): Graph {
  return {
    repo: raw.repo,
    lanes: raw.lanes ?? [],
    events: (raw.events ?? []).map((e) => ({ ...e, detail: e.detail ?? {} })),
    entities: raw.entities ?? [],
  };
}

// normalizeCommits fills every nil parents/tasks/events slice with [] and every
// nil event detail map with {}, so downstream code sees non-null arrays and
// maps. The by-design nullables — each commit's branch and the page's
// next_before — stay null.
export function normalizeCommits(raw: RawCommitsPage): CommitsPage {
  return {
    commits: (raw.commits ?? []).map((c) => ({
      ...c,
      parents: c.parents ?? [],
      tasks: c.tasks ?? [],
      events: (c.events ?? []).map((e) => ({ ...e, detail: e.detail ?? {} })),
    })),
    next_before: raw.next_before,
    truncated: raw.truncated,
  };
}

// normalizeEntity fills the trail, each entry's changes, and each change's added
// and removed with [] wherever the wire carried null.
export function normalizeEntity(raw: RawEntityDetail): EntityDetail {
  return {
    summary: raw.summary,
    snapshot: raw.snapshot,
    trail: (raw.trail ?? []).map((entry) => ({
      ...entry,
      changes: (entry.changes ?? []).map((c) => ({
        ...c,
        added: c.added ?? [],
        removed: c.removed ?? [],
      })),
    })),
  };
}

// normalizeEntities fills every nil kind bucket with []. The folded snapshots
// inside are left verbatim.
export function normalizeEntities(raw: RawStateResponse): StateResponse {
  return {
    notes: raw.notes ?? [],
    docs: raw.docs ?? [],
    logs: raw.logs ?? [],
    tasks: raw.tasks ?? [],
    sprints: raw.sprints ?? [],
    projects: raw.projects ?? [],
    runbooks: raw.runbooks ?? [],
    investigations: raw.investigations ?? [],
  };
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    throw new Error(`${path}: ${res.status} ${res.statusText}`);
  }
  return (await res.json()) as T;
}

export function fetchRepo(): Promise<RepoInfo> {
  return getJSON<RepoInfo>("/api/repo");
}

export async function fetchGraph(since?: number): Promise<Graph> {
  const q = since !== undefined ? `?since=${since}` : "";
  return normalizeGraph(await getJSON<RawGraph>(`/api/graph${q}`));
}

export async function fetchCommits(
  before?: string,
  limit?: number,
): Promise<CommitsPage> {
  const params = new URLSearchParams();
  if (limit !== undefined) params.set("limit", String(limit));
  if (before !== undefined) params.set("before", before);
  const q = params.toString();
  return normalizeCommits(
    await getJSON<RawCommitsPage>(`/api/commits${q ? `?${q}` : ""}`),
  );
}

export async function fetchEntity(kind: string, id: string): Promise<EntityDetail> {
  return normalizeEntity(
    await getJSON<RawEntityDetail>(
      `/api/entity/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`,
    ),
  );
}

export async function fetchEntities(): Promise<StateResponse> {
  return normalizeEntities(await getJSON<RawStateResponse>("/api/entities"));
}

// blobURL builds the /api/blob/{oid} URL for an attachment's bytes, appending
// the optional download name as a query parameter.
export function blobURL(oid: string, name?: string): string {
  const q = name !== undefined ? `?name=${encodeURIComponent(name)}` : "";
  return `/api/blob/${encodeURIComponent(oid)}${q}`;
}
