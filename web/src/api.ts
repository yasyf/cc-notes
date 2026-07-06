// Wire types mirror internal/viz/types.go exactly: snake_case JSON keys and
// unix-second integer times. omitempty fields on the Go side are optional here.

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

export function fetchGraph(): Promise<Graph> {
  return getJSON<Graph>("/api/graph");
}
