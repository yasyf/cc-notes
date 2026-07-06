// Wire types mirror internal/viz/types.go and the entity-detail DTOs: snake_case
// JSON keys and unix-second integer times. The Go side marshals a nil slice or
// nil map as JSON null, so the Raw* types below type those fields honestly
// (array/map | null) and the fetchers normalize every such field to a non-null
// [] or {} before it reaches component code. Only genuinely-optional pointers
// (a lane's fork/merge/tip) stay null by design.

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

// TrailChange is one field delta in an entity's change trail: a scalar carries
// from→to, a set carries added/removed. Mirrors internal/viz.trailChange.
export interface TrailChange {
  field: string;
  scalar: boolean;
  from: string;
  to: string;
  added: string[];
  removed: string[];
}

// TrailEntry is one change-trail commit. Mirrors internal/viz.trailEntry.
export interface TrailEntry {
  sha: string;
  author: string;
  time: number;
  lamport: number;
  kind: string;
  covers: number;
  changes: TrailChange[];
}

// EntityDetail is the /api/entity/{kind}/{id} payload: the legend summary plus
// the full change trail, oldest first. Mirrors internal/viz.entityResponse.
export interface EntityDetail {
  summary: EntitySummary;
  trail: TrailEntry[];
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

// RawTrailChange is TrailChange as it arrives on the wire: added and removed are
// Go slices that marshal as null when nil (a scalar change carries neither).
interface RawTrailChange extends Omit<TrailChange, "added" | "removed"> {
  added: string[] | null;
  removed: string[] | null;
}

// RawTrailEntry is TrailEntry as it arrives on the wire: changes marshals as null
// when nil.
interface RawTrailEntry extends Omit<TrailEntry, "changes"> {
  changes: RawTrailChange[] | null;
}

// RawEntityDetail is EntityDetail as it arrives on the wire: trail marshals as
// null when nil.
interface RawEntityDetail {
  summary: EntitySummary;
  trail: RawTrailEntry[] | null;
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

// normalizeEntity fills the trail, each entry's changes, and each change's added
// and removed with [] wherever the wire carried null.
export function normalizeEntity(raw: RawEntityDetail): EntityDetail {
  return {
    summary: raw.summary,
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

export async function fetchEntity(kind: string, id: string): Promise<EntityDetail> {
  return normalizeEntity(
    await getJSON<RawEntityDetail>(
      `/api/entity/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`,
    ),
  );
}
