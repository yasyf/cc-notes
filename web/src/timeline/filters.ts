// Pure pre-layout filter for the timeline. The interactive legend and lane
// labels drive a TimelineFilters value; applyFilters drops events and entities
// the caller has hidden BEFORE layout runs, so sub-row packing tightens over the
// survivors. Lanes themselves are never removed — a lane's rail stays so branch
// structure reads even when all its items are filtered out.

import type { Graph } from "../api";

// MARKER_KINDS mirrors layout.ts: the entity kinds that render as point markers,
// so only their event types populate the event-type legend.
const MARKER_KINDS = new Set(["note", "doc", "log", "sprint", "runbook"]);

// TimelineFilters records what the viewer has hidden: entity kinds and event
// types toggled off in the legend, plus an optional single-lane focus. Empty
// sets and a null lane impose no constraint.
export interface TimelineFilters {
  hiddenKinds: ReadonlySet<string>;
  hiddenTypes: ReadonlySet<string>;
  lane: string | null;
}

export const NO_FILTERS: TimelineFilters = {
  hiddenKinds: new Set(),
  hiddenTypes: new Set(),
  lane: null,
};

// filtersActive reports whether any constraint is set, so callers can skip the
// copy when nothing is filtered.
export function filtersActive(f: TimelineFilters): boolean {
  return f.hiddenKinds.size > 0 || f.hiddenTypes.size > 0 || f.lane !== null;
}

// trunkName mirrors layout's resolveTrunk: the lane named by repo.trunk, else the
// first lane (the Go builder emits the trunk first), else "" for an empty graph.
function trunkName(graph: Graph): string {
  const names = new Set(graph.lanes.map((l) => l.name));
  if (names.has(graph.repo.trunk)) return graph.repo.trunk;
  return graph.lanes.length > 0 ? graph.lanes[0].name : "";
}

// applyFilters returns a graph whose events and entities are narrowed to the
// active filters; lanes and repo pass through untouched. When nothing is
// filtered it returns the input graph unchanged (referentially stable, so a
// useMemo over it does not re-lay-out needlessly).
export function applyFilters(graph: Graph, f: TimelineFilters): Graph {
  if (!filtersActive(f)) return graph;

  const laneNames = new Set(graph.lanes.map((l) => l.name));
  const trunk = trunkName(graph);
  // laneOf mirrors layout's attribute(): empty or unknown branch resolves to the
  // trunk, a known branch to itself — so a lane focus keeps exactly the items
  // that would render on that lane.
  const laneOf = (branch: string): string =>
    branch !== "" && laneNames.has(branch) ? branch : trunk;

  const keepKind = (kind: string) => !f.hiddenKinds.has(kind);
  const onLane = (branch: string) => f.lane === null || laneOf(branch) === f.lane;

  const events = graph.events.filter(
    (e) => keepKind(e.entity.kind) && !f.hiddenTypes.has(e.type) && onLane(e.branch),
  );

  const entities = graph.entities.filter((e) => {
    if (!keepKind(e.kind)) return false;
    if (f.lane === null) return true;
    // Tasks attribute by branch; sprint/project bands live on the trunk row, so
    // they survive a lane focus only when the trunk itself is focused.
    if (e.kind === "task") return laneOf(e.branch ?? "") === f.lane;
    return f.lane === trunk;
  });

  return { ...graph, events, entities };
}

// presentKinds is the set of entity kinds that actually render in the graph, so
// the legend offers a kind filter only for kinds on screen: marker kinds with an
// event, tasks that have a lifeline (started), and sprint/project bands with a
// date range. Derived from the UNFILTERED graph so a hidden kind still lists (and
// can be re-enabled).
export function presentKinds(graph: Graph): Set<string> {
  const kinds = new Set<string>();
  for (const e of graph.events) {
    if (MARKER_KINDS.has(e.entity.kind)) kinds.add(e.entity.kind);
  }
  for (const e of graph.entities) {
    if (e.kind === "task" && (e.started_at ?? 0) > 0) kinds.add("task");
    if ((e.kind === "sprint" || e.kind === "project") && (e.start_date ?? 0) > 0) {
      kinds.add(e.kind);
    }
  }
  return kinds;
}

// presentTypes is the set of event types that render as markers, for the legend's
// event-type filters. Task-lifecycle events (which surface on spans, not markers)
// are excluded. Derived from the UNFILTERED graph.
export function presentTypes(graph: Graph): Set<string> {
  const types = new Set<string>();
  for (const e of graph.events) {
    if (MARKER_KINDS.has(e.entity.kind)) types.add(e.type);
  }
  return types;
}
