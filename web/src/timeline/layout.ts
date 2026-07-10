// Pure swimlane layout engine: Graph + injected `now` -> a typed LayoutResult
// the SVG renderer consumes verbatim. No DOM, no React, no Date.now — the same
// input always yields the same output (every list deterministically ordered), so
// the whole engine is table-testable.

import type { Graph, Lane } from "../api";

// Kinds whose events render as point markers on a lane. Tasks render as spans,
// projects as bands, so neither contributes markers.
const MARKER_KINDS = new Set(["note", "doc", "log", "sprint", "runbook"]);
// Kinds that contribute background bands from their date range.
const BAND_KINDS = new Set(["sprint", "project"]);
// Fallback marker footprint divisor: a marker occupies domain/DOMAIN_MARKER_DIV
// seconds for sub-row collision when no explicit width is injected.
const DOMAIN_MARKER_DIV = 150;
const DAY = 86400;
const EMPTY_SET: ReadonlySet<string> = new Set();

export interface EntityRef {
  kind: string;
  id: string;
  short: string;
  title: string;
}

export interface SpanItem {
  ref: EntityRef;
  start: number;
  end: number; // resolved: open span extends to `now`
  open: boolean; // closed_at was 0
  status: string; // final task status, colours the bar
  assignee?: string; // task assignee, surfaced in the tooltip
  subRow: number;
  orphanBranch?: string; // set when the task's branch matched no lane
  clamped?: boolean; // an endpoint was clamped into [domainStart, now]
}

export interface MarkerItem {
  ref: EntityRef;
  type: string; // event type
  time: number;
  sha: string;
  detail: Record<string, string>;
  subRow: number;
  orphanBranch?: string; // set when event.branch matched no lane
  clamped?: boolean; // time was clamped into [domainStart, now]
}

// LaneClass is the lane's visual family for the renderer: a live lane, a
// merged-away lane, a mined deleted lane (real DAG evidence), or a task-rumor
// deleted lane (inferred, no fork/tip). Derived from status + inferred.
export type LaneClass = "active" | "merged" | "deleted" | "deleted-inferred";

export interface LaneRow {
  name: string;
  parent: string; // resolved parent lane name ("" for the trunk root)
  status: string; // active | merged | deleted
  inferred: boolean;
  laneClass: LaneClass;
  isTrunk: boolean;
  row: number; // packed global row index (siblings may share a row)
  depth: number; // tree depth, trunk = 0 — label indent
  start: number; // effective start
  end: number; // effective end (open -> now)
  open: boolean; // lane.end was 0
  commits: number; // attributed commit count (0 hides the label count)
  collapsed: boolean; // rendered as a single thin row, spans/markers suppressed
  autoCollapsed: boolean; // deleted lane wholly before the window — collapsed by
  // default, still listed and expandable ("older than view")
  fork: { time: number } | null;
  merge: { time: number; into: string; kind: string } | null;
  spans: SpanItem[];
  markers: MarkerItem[];
  subRows: number; // sub-rows used, >= 1
}

export type ConnectorKind = "fork" | "merge" | "fast-forward" | "inferred";

export interface Connector {
  kind: ConnectorKind;
  time: number;
  childRow: number;
  parentRow: number;
  dashed: boolean; // inferred merges render dashed
  clamped?: boolean; // time was clamped into [domainStart, now]
}

export interface Band {
  ref: EntityRef;
  kind: "sprint" | "project";
  start: number;
  end: number;
  open: boolean;
  row: number; // trunk row
}

export interface LayoutResult {
  lanes: LaneRow[];
  connectors: Connector[];
  bands: Band[];
  domain: [number, number];
  rowCount: number;
  markerWidth: number;
  now: number;
}

export interface LayoutInput {
  graph: Graph;
  now: number; // unix seconds, injected for determinism
  markerWidth?: number; // marker footprint in seconds; derived from domain when absent
  // collapsed carries lane names the caller has toggled away from their default
  // collapse state: a normal lane in the set renders collapsed; an auto-collapsed
  // lane in the set renders expanded. So one caller-held set drives both
  // directions, and layout resolves the effective `collapsed` per lane.
  collapsed?: ReadonlySet<string>;
}

interface Attribution {
  lane: string;
  orphanBranch?: string;
}

// layout packs a graph into swimlane rows, sub-rows, connectors, and bands.
export function layout(input: LayoutInput): LayoutResult {
  const { graph, now } = input;
  const laneByName = new Map<string, Lane>();
  for (const l of graph.lanes) laneByName.set(l.name, l);

  const trunk = resolveTrunk(graph, laneByName);
  if (trunk === null) {
    return {
      lanes: [],
      connectors: [],
      bands: [],
      domain: [now - DAY, now],
      rowCount: 0,
      markerWidth: input.markerWidth ?? DAY,
      now,
    };
  }

  // clampFloor is the window's left edge (domainStart): the trunk's effective
  // start, which the Go builder floors at the oldest ref-backed lane fork. Marks
  // and connectors are clamped into [clampFloor, now] and marks that end before
  // it are dropped, so nothing dangles left of the trunk rail. A non-positive
  // trunk start (a repo whose window walk found no commits) disables the floor —
  // NEGATIVE_INFINITY leaves the low side unclamped, preserving the data-derived
  // domain.
  const clampFloor = trunk.start > 0 ? trunk.start : Number.NEGATIVE_INFINITY;

  const effEnd = (l: Lane) => (l.end === 0 ? now : l.end);
  const parentOf = (l: Lane): string => {
    if (l.name === trunk.name) return "";
    if (l.parent !== "" && l.parent !== l.name && laneByName.has(l.parent)) {
      return l.parent;
    }
    return trunk.name;
  };

  const children = new Map<string, Lane[]>();
  for (const l of graph.lanes) {
    if (l.name === trunk.name) continue;
    const p = parentOf(l);
    const arr = children.get(p);
    if (arr) arr.push(l);
    else children.set(p, [l]);
  }
  for (const arr of children.values()) {
    arr.sort(
      (a, b) => a.start - b.start || sortForkTime(a, b) || cmp(a.name, b.name),
    );
  }

  // Pre-order DFS with per-parent-group greedy row reuse. A row may be reused by
  // a later lane only when it belongs to the same parent group and its last
  // occupant's effective end precedes the newcomer's effective start.
  const rows: { parent: string; lastEnd: number }[] = [];
  const rowOf = new Map<string, number>();
  const depthOf = new Map<string, number>();
  const visited = new Set<string>();

  const place = (lane: Lane, parent: string, depth: number) => {
    if (visited.has(lane.name)) return;
    visited.add(lane.name);
    depthOf.set(lane.name, depth);
    const start = lane.start;
    let chosen = -1;
    for (let r = 0; r < rows.length; r++) {
      if (rows[r].parent === parent && rows[r].lastEnd < start) {
        chosen = r;
        break;
      }
    }
    if (chosen === -1) {
      chosen = rows.length;
      rows.push({ parent, lastEnd: Number.NEGATIVE_INFINITY });
    }
    rows[chosen].lastEnd = effEnd(lane);
    rowOf.set(lane.name, chosen);
    for (const child of children.get(lane.name) ?? []) {
      place(child, lane.name, depth + 1);
    }
  };
  place(trunk, "", 0);
  // Any lane the DFS missed (defensive against a malformed forest) attaches flat
  // under the trunk in name order, so no lane is silently dropped.
  for (const l of graph.lanes) {
    if (!visited.has(l.name)) place(l, trunk.name, 1);
  }

  const trunkRow = rowOf.get(trunk.name) ?? 0;
  const attribute = (branch: string): Attribution => {
    if (branch === "") return { lane: trunk.name };
    if (laneByName.has(branch)) return { lane: branch };
    return { lane: trunk.name, orphanBranch: branch };
  };

  // Bucket spans and markers by their attributed lane.
  const spansByLane = new Map<string, SpanItem[]>();
  const markersByLane = new Map<string, MarkerItem[]>();
  const pushSpan = (lane: string, s: SpanItem) => {
    const arr = spansByLane.get(lane);
    if (arr) arr.push(s);
    else spansByLane.set(lane, [s]);
  };
  const pushMarker = (lane: string, m: MarkerItem) => {
    const arr = markersByLane.get(lane);
    if (arr) arr.push(m);
    else markersByLane.set(lane, [m]);
  };

  for (const e of graph.entities) {
    if (e.kind !== "task") continue;
    const started = e.started_at ?? 0;
    if (started <= 0) continue; // never-claimed tasks have no lifeline segment
    const closed = e.closed_at ?? 0;
    const rawEnd = closed > 0 ? closed : now;
    if (rawEnd < clampFloor) continue; // span ends before the window: drop
    const start = Math.max(started, clampFloor);
    const end = Math.min(rawEnd, now);
    const at = attribute(e.branch ?? "");
    pushSpan(at.lane, {
      ref: { kind: e.kind, id: e.id, short: e.short, title: e.title },
      start,
      end,
      open: closed <= 0,
      status: e.status ?? "",
      subRow: 0,
      ...(e.assignee !== undefined && e.assignee !== "" ? { assignee: e.assignee } : {}),
      ...(at.orphanBranch !== undefined ? { orphanBranch: at.orphanBranch } : {}),
      ...(start !== started || end !== rawEnd ? { clamped: true } : {}),
    });
  }

  for (const e of graph.events) {
    if (!MARKER_KINDS.has(e.entity.kind)) continue;
    if (e.time < clampFloor) continue; // marker before the window: drop
    const time = Math.min(e.time, now);
    const at = attribute(e.branch);
    pushMarker(at.lane, {
      ref: {
        kind: e.entity.kind,
        id: e.entity.id,
        short: e.entity.short,
        title: e.entity.title,
      },
      type: e.type,
      time,
      sha: e.sha,
      detail: e.detail,
      subRow: 0,
      ...(at.orphanBranch !== undefined ? { orphanBranch: at.orphanBranch } : {}),
      ...(time !== e.time ? { clamped: true } : {}),
    });
  }

  const bands: Band[] = [];
  for (const e of graph.entities) {
    if (!BAND_KINDS.has(e.kind)) continue;
    const start = e.start_date ?? 0;
    if (start <= 0) continue;
    const end = e.end_date ?? 0;
    bands.push({
      ref: { kind: e.kind, id: e.id, short: e.short, title: e.title },
      kind: e.kind === "project" ? "project" : "sprint",
      start,
      end: end > 0 ? end : now,
      open: end <= 0,
      row: trunkRow,
    });
  }
  bands.sort(
    (a, b) => a.start - b.start || a.end - b.end || cmp(a.ref.id, b.ref.id),
  );

  const domain = computeDomain(graph, effEnd, spansByLane, markersByLane, bands, now, clampFloor);
  const markerWidth =
    input.markerWidth ??
    Math.max(1, Math.round((domain[1] - domain[0]) / DOMAIN_MARKER_DIV));

  // Assemble one LaneRow per lane, packing its spans+markers onto sub-rows. A
  // collapsed lane suppresses its items and reports a single sub-row, so the
  // shared row tightens; a deleted lane wholly before the window auto-collapses.
  const toggled = input.collapsed ?? EMPTY_SET;
  const laneRows: LaneRow[] = [];
  for (const l of graph.lanes) {
    const cls = classifyLane(l);
    const deleted = cls === "deleted" || cls === "deleted-inferred";
    const autoCollapsed =
      deleted && Number.isFinite(clampFloor) && effEnd(l) < clampFloor;
    const collapsed = autoCollapsed ? !toggled.has(l.name) : toggled.has(l.name);
    const spans = collapsed ? [] : (spansByLane.get(l.name) ?? []).slice();
    const markers = collapsed ? [] : (markersByLane.get(l.name) ?? []).slice();
    const subRows = collapsed ? 1 : packSubRows(spans, markers, markerWidth);
    spans.sort((a, b) => a.start - b.start || a.end - b.end || cmp(a.ref.id, b.ref.id));
    markers.sort(
      (a, b) => a.time - b.time || cmp(a.type, b.type) || cmp(a.ref.id, b.ref.id),
    );
    laneRows.push({
      name: l.name,
      parent: parentOf(l),
      status: l.status,
      inferred: l.inferred,
      laneClass: cls,
      isTrunk: l.name === trunk.name,
      row: rowOf.get(l.name) ?? 0,
      depth: depthOf.get(l.name) ?? 0,
      start: l.start,
      end: effEnd(l),
      open: l.end === 0,
      commits: l.commits,
      collapsed,
      autoCollapsed,
      fork: l.fork !== null ? { time: l.fork.time } : null,
      merge:
        l.merge !== null
          ? { time: l.merge.time, into: l.merge.into, kind: l.merge.kind }
          : null,
      spans,
      markers,
      subRows,
    });
  }
  laneRows.sort(
    (a, b) => a.row - b.row || a.start - b.start || cmp(a.name, b.name),
  );

  const connectors = buildConnectors(graph.lanes, trunk.name, rowOf, clampFloor, now);

  return {
    lanes: laneRows,
    connectors,
    bands,
    domain,
    rowCount: rows.length,
    markerWidth,
    now,
  };
}

// resolveTrunk returns the lane named by repo.trunk, falling back to the first
// lane (the Go builder always emits the trunk first). null when there are none.
function resolveTrunk(graph: Graph, byName: Map<string, Lane>): Lane | null {
  const named = byName.get(graph.repo.trunk);
  if (named !== undefined) return named;
  return graph.lanes.length > 0 ? graph.lanes[0] : null;
}

// packSubRows greedily packs spans and markers onto the fewest sub-rows: items
// sorted by effective start take the first sub-row whose last occupant ends
// before they begin. Mutates each item's subRow; returns the sub-row count (>=1).
function packSubRows(
  spans: SpanItem[],
  markers: MarkerItem[],
  markerWidth: number,
): number {
  interface Packable {
    start: number;
    end: number;
    tie: number; // spans before markers for a stable order at equal start
    id: string;
    assign: (subRow: number) => void;
  }
  const items: Packable[] = [];
  for (const s of spans) {
    items.push({
      start: s.start,
      end: s.end,
      tie: 0,
      id: s.ref.id,
      assign: (r) => {
        s.subRow = r;
      },
    });
  }
  for (const m of markers) {
    items.push({
      start: m.time,
      end: m.time + markerWidth,
      tie: 1,
      id: `${m.ref.id}:${m.type}:${m.time}`,
      assign: (r) => {
        m.subRow = r;
      },
    });
  }
  items.sort(
    (a, b) => a.start - b.start || a.tie - b.tie || cmp(a.id, b.id),
  );
  const lastEnd: number[] = [];
  for (const it of items) {
    let row = -1;
    for (let r = 0; r < lastEnd.length; r++) {
      if (lastEnd[r] < it.start) {
        row = r;
        break;
      }
    }
    if (row === -1) {
      row = lastEnd.length;
      lastEnd.push(it.end);
    } else {
      lastEnd[row] = it.end;
    }
    it.assign(row);
  }
  return Math.max(1, lastEnd.length);
}

// buildConnectors emits a fork descriptor for every forked lane and a merge
// descriptor for every merged lane, each joining the lane's row to its parent's.
// Each connector's time is clamped into [clampFloor, now] so its endpoint lands
// on the visible rail rather than dangling in empty space left of the trunk.
function buildConnectors(
  lanes: Lane[],
  trunkName: string,
  rowOf: Map<string, number>,
  clampFloor: number,
  now: number,
): Connector[] {
  const clamp = (t: number) => Math.min(Math.max(t, clampFloor), now);
  const connectors: Connector[] = [];
  for (const l of lanes) {
    const childRow = rowOf.get(l.name);
    if (childRow === undefined) continue;
    if (l.fork !== null) {
      const parentRow = rowOf.get(l.parent) ?? rowOf.get(trunkName) ?? 0;
      const time = clamp(l.fork.time);
      connectors.push({
        kind: "fork",
        time,
        childRow,
        parentRow,
        dashed: false,
        ...(time !== l.fork.time ? { clamped: true } : {}),
      });
    }
    if (l.merge !== null) {
      const parentRow = rowOf.get(l.merge.into) ?? rowOf.get(trunkName) ?? 0;
      const kind = mergeKind(l.merge.kind);
      const time = clamp(l.merge.time);
      connectors.push({
        kind,
        time,
        childRow,
        parentRow,
        dashed: kind === "inferred",
        ...(time !== l.merge.time ? { clamped: true } : {}),
      });
    }
  }
  connectors.sort(
    (a, b) =>
      a.time - b.time ||
      a.childRow - b.childRow ||
      a.parentRow - b.parentRow ||
      cmp(a.kind, b.kind),
  );
  return connectors;
}

function mergeKind(raw: string): ConnectorKind {
  if (raw === "fast-forward") return "fast-forward";
  if (raw === "inferred") return "inferred";
  return "merge";
}

// classifyLane maps a lane's status + inferred flag to its visual family. A
// deleted lane splits by inferred: real DAG-mined lanes are "deleted", task-rumor
// lanes (no fork/tip) are "deleted-inferred" and render most muted.
function classifyLane(l: Lane): LaneClass {
  if (l.status === "deleted") return l.inferred ? "deleted-inferred" : "deleted";
  if (l.status === "merged") return "merged";
  return "active";
}

// computeDomain spans every meaningful time in the graph, floored so a marker
// footprint never pushes the axis and clamped to at least [now-1d, now]. The
// lower bound is pinned to clampFloor (the window's left edge) when it is finite,
// so a lane, mark, or connector that predates the window never drags the axis
// left of the trunk rail; a non-finite clampFloor falls back to the earliest
// data time.
function computeDomain(
  graph: Graph,
  effEnd: (l: Lane) => number,
  spansByLane: Map<string, SpanItem[]>,
  markersByLane: Map<string, MarkerItem[]>,
  bands: Band[],
  now: number,
  clampFloor: number,
): [number, number] {
  let min = Number.POSITIVE_INFINITY;
  let max = now;
  const lo = (t: number) => {
    if (t > 0 && t < min) min = t;
  };
  const hi = (t: number) => {
    if (t > max) max = t;
  };
  for (const l of graph.lanes) {
    lo(l.start);
    hi(effEnd(l));
    if (l.fork !== null) lo(l.fork.time);
    if (l.merge !== null) {
      lo(l.merge.time);
      hi(l.merge.time);
    }
  }
  for (const arr of spansByLane.values())
    for (const s of arr) {
      lo(s.start);
      hi(s.end);
    }
  for (const arr of markersByLane.values())
    for (const m of arr) {
      lo(m.time);
      hi(m.time);
    }
  for (const b of bands) {
    lo(b.start);
    hi(b.end);
  }
  if (!Number.isFinite(min)) min = now - DAY;
  let start = Number.isFinite(clampFloor) ? clampFloor : min;
  if (start >= max) start = max - DAY;
  return [start, max];
}

function sortForkTime(a: Lane, b: Lane): number {
  const at = a.fork !== null ? a.fork.time : a.start;
  const bt = b.fork !== null ? b.fork.time : b.start;
  return at - bt;
}

function cmp(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}
