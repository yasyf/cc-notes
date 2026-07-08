import { describe, expect, it } from "vitest";
import type {
  Event,
  EntitySummary,
  Graph,
  Lane,
  RepoInfo,
} from "../api";
import { layout } from "./layout";

const NOW = 1000;

function repo(trunk = "main"): RepoInfo {
  return { root: "/r", trunk, head: trunk, generated_at: "", truncated: false };
}

function lane(name: string, over: Partial<Lane> = {}): Lane {
  return {
    name,
    parent: "",
    fork: null,
    merge: null,
    status: "active",
    inferred: false,
    tip: null,
    start: 0,
    end: 0,
    commits: 0,
    ...over,
  };
}

function graph(over: Partial<Graph> = {}): Graph {
  return {
    repo: repo(),
    lanes: [],
    events: [],
    entities: [],
    ...over,
  };
}

function noteEvent(id: string, time: number, branch: string): Event {
  return {
    entity: { kind: "note", id, short: id.slice(0, 7), title: `note ${id}` },
    type: "created",
    time,
    branch,
    sha: `sha-${id}`,
    detail: {},
  };
}

function task(id: string, over: Partial<EntitySummary>): EntitySummary {
  return {
    kind: "task",
    id,
    short: id.slice(0, 7),
    title: `task ${id}`,
    ...over,
  };
}

function rowsOf(g: Graph) {
  return layout({ graph: g, now: NOW }).lanes.map((l) => ({
    name: l.name,
    row: l.row,
    depth: l.depth,
    parent: l.parent,
  }));
}

describe("layout lane rows", () => {
  it("groups children under their parent, recursively by fork time", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 10 }),
        lane("feat-b", { parent: "main", fork: { sha: "b", time: 40 }, start: 40 }),
        lane("feat-a", { parent: "main", fork: { sha: "a", time: 20 }, start: 20 }),
        lane("sub-a", { parent: "feat-a", fork: { sha: "sa", time: 25 }, start: 25 }),
      ],
    });
    expect(rowsOf(g)).toEqual([
      { name: "main", row: 0, depth: 0, parent: "" },
      { name: "feat-a", row: 1, depth: 1, parent: "main" },
      { name: "sub-a", row: 2, depth: 2, parent: "feat-a" },
      { name: "feat-b", row: 3, depth: 1, parent: "main" },
    ]);
  });

  it("reuses a freed row for a non-overlapping sibling", () => {
    const g = graph({
      lanes: [
        lane("main"),
        lane("a", { parent: "main", fork: { sha: "a", time: 5 }, start: 5, end: 10, status: "merged" }),
        lane("b", { parent: "main", fork: { sha: "b", time: 20 }, start: 20, end: 30, status: "merged" }),
      ],
    });
    const result = layout({ graph: g, now: NOW });
    expect(result.lanes.map((l) => [l.name, l.row])).toEqual([
      ["main", 0],
      ["a", 1],
      ["b", 1],
    ]);
    expect(result.rowCount).toBe(2);
  });

  it("stacks overlapping same-parent lanes onto distinct rows", () => {
    const g = graph({
      lanes: [
        lane("main"),
        lane("a", { parent: "main", fork: { sha: "a", time: 5 }, start: 5, end: 20 }),
        lane("b", { parent: "main", fork: { sha: "b", time: 10 }, start: 10, end: 30 }),
      ],
    });
    const result = layout({ graph: g, now: NOW });
    expect(result.lanes.map((l) => [l.name, l.row])).toEqual([
      ["main", 0],
      ["a", 1],
      ["b", 2],
    ]);
    expect(result.rowCount).toBe(3);
  });

  it("extends an open lane (end 0) to now", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 10 }),
        lane("b", { parent: "main", fork: { sha: "b", time: 5 }, start: 5, end: 0 }),
      ],
    });
    const result = layout({ graph: g, now: NOW });
    const b = result.lanes.find((l) => l.name === "b");
    expect(b?.end).toBe(NOW);
    expect(b?.open).toBe(true);
    expect(result.lanes.find((l) => l.name === "main")?.open).toBe(true);
    expect(result.domain[1]).toBeGreaterThanOrEqual(NOW);
  });

  it("presents a deleted, inferred lane with its extent and dashed merge", () => {
    const g = graph({
      lanes: [
        lane("main"),
        lane("old-feat", {
          parent: "",
          status: "deleted",
          inferred: true,
          start: 50,
          end: 80,
          merge: { sha: "m", time: 80, into: "main", kind: "inferred" },
        }),
      ],
    });
    const result = layout({ graph: g, now: NOW });
    const old = result.lanes.find((l) => l.name === "old-feat");
    expect(old).toMatchObject({
      parent: "main",
      status: "deleted",
      inferred: true,
      start: 50,
      end: 80,
      open: false,
      row: 1,
    });
    expect(result.connectors).toEqual([
      { kind: "inferred", time: 80, childRow: 1, parentRow: 0, dashed: true },
    ]);
  });
});

describe("layout deleted lanes and collapse", () => {
  it("connects a mined deleted lane with real fork and merge points", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 10 }),
        lane("gone", {
          parent: "",
          status: "deleted",
          inferred: false,
          fork: { sha: "f", time: 20 },
          merge: { sha: "m", time: 60, into: "main", kind: "merge" },
          start: 20,
          end: 60,
          commits: 4,
        }),
      ],
    });
    const result = layout({ graph: g, now: NOW });
    const gone = result.lanes.find((l) => l.name === "gone");
    expect(gone?.laneClass).toBe("deleted");
    expect(gone?.collapsed).toBe(false);
    expect(gone?.autoCollapsed).toBe(false);
    expect(gone?.commits).toBe(4);
    expect(result.connectors).toEqual([
      { kind: "fork", time: 20, childRow: 1, parentRow: 0, dashed: false },
      { kind: "merge", time: 60, childRow: 1, parentRow: 0, dashed: false },
    ]);
  });

  it("classifies a mined deleted lane and a task-rumor lane as distinct families", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 10 }),
        lane("mined", {
          parent: "",
          status: "deleted",
          inferred: false,
          fork: { sha: "f", time: 20 },
          merge: { sha: "m", time: 50, into: "main", kind: "merge" },
          start: 20,
          end: 50,
        }),
        lane("rumor", {
          parent: "",
          status: "deleted",
          inferred: true,
          merge: { sha: "r", time: 40, into: "main", kind: "inferred" },
          start: 30,
          end: 40,
        }),
      ],
    });
    const result = layout({ graph: g, now: NOW });
    const byName = new Map(result.lanes.map((l) => [l.name, l]));
    expect(byName.get("mined")?.laneClass).toBe("deleted");
    expect(byName.get("rumor")?.laneClass).toBe("deleted-inferred");
    expect(result.connectors).toEqual([
      { kind: "fork", time: 20, childRow: 1, parentRow: 0, dashed: false },
      { kind: "inferred", time: 40, childRow: 2, parentRow: 0, dashed: true },
      { kind: "merge", time: 50, childRow: 1, parentRow: 0, dashed: false },
    ]);
  });

  it("suppresses items and tightens a collapsed lane to a single sub-row", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 5 }),
        lane("feat", { parent: "main", fork: { sha: "f", time: 10 }, start: 10, end: 0 }),
      ],
      entities: [
        task("t1", { branch: "feat", status: "done", started_at: 12, closed_at: 40 }),
        task("t2", { branch: "feat", status: "done", started_at: 15, closed_at: 45 }),
      ],
    });
    const open = layout({ graph: g, now: NOW });
    const featOpen = open.lanes.find((l) => l.name === "feat");
    expect(featOpen?.subRows).toBe(2);
    expect(featOpen?.spans).toHaveLength(2);
    expect(featOpen?.collapsed).toBe(false);

    const shut = layout({ graph: g, now: NOW, collapsed: new Set(["feat"]) });
    const featShut = shut.lanes.find((l) => l.name === "feat");
    expect(featShut?.collapsed).toBe(true);
    expect(featShut?.subRows).toBe(1);
    expect(featShut?.spans).toEqual([]);
    expect(featShut?.markers).toEqual([]);
  });

  it("auto-collapses a deleted lane wholly before the window, and a toggle expands it", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 100, end: 0 }),
        lane("ancient", {
          parent: "main",
          status: "deleted",
          inferred: false,
          fork: { sha: "f", time: 20 },
          merge: { sha: "m", time: 50, into: "main", kind: "merge" },
          start: 20,
          end: 50,
        }),
      ],
    });
    const result = layout({ graph: g, now: NOW });
    const auto = result.lanes.find((l) => l.name === "ancient");
    expect(auto?.autoCollapsed).toBe(true);
    expect(auto?.collapsed).toBe(true);

    const expanded = layout({ graph: g, now: NOW, collapsed: new Set(["ancient"]) });
    const shown = expanded.lanes.find((l) => l.name === "ancient");
    expect(shown?.autoCollapsed).toBe(true);
    expect(shown?.collapsed).toBe(false);
  });
});

describe("layout entity items", () => {
  it("renders a claimed-open task as a span running to now", () => {
    const g = graph({
      entities: [
        task("t1", { branch: "main", status: "in_progress", started_at: 100, closed_at: 0 }),
      ],
      lanes: [lane("main", { start: 10 })],
    });
    const result = layout({ graph: g, now: NOW });
    const main = result.lanes.find((l) => l.name === "main");
    expect(main?.spans).toEqual([
      {
        ref: { kind: "task", id: "t1", short: "t1", title: "task t1" },
        start: 100,
        end: NOW,
        open: true,
        status: "in_progress",
        subRow: 0,
      },
    ]);
  });

  it("skips a never-claimed task (no started_at)", () => {
    const g = graph({
      entities: [task("t0", { branch: "main", status: "open", started_at: 0 })],
      lanes: [lane("main")],
    });
    const result = layout({ graph: g, now: NOW });
    expect(result.lanes.find((l) => l.name === "main")?.spans).toEqual([]);
  });

  it("packs colliding markers onto distinct sub-rows and reuses a freed one", () => {
    const g = graph({
      lanes: [lane("main")],
      events: [
        noteEvent("n1", 100, "main"),
        noteEvent("n2", 105, "main"),
        noteEvent("n3", 200, "main"),
      ],
    });
    const result = layout({ graph: g, now: NOW, markerWidth: 10 });
    const main = result.lanes.find((l) => l.name === "main");
    const byId = new Map(main?.markers.map((m) => [m.ref.id, m.subRow]));
    expect(byId.get("n1")).toBe(0);
    expect(byId.get("n2")).toBe(1);
    expect(byId.get("n3")).toBe(0);
    expect(main?.subRows).toBe(2);
  });

  it("attributes an unknown branch to the trunk with an orphan flag", () => {
    const g = graph({
      lanes: [lane("main")],
      events: [noteEvent("g1", 100, "ghost-branch")],
    });
    const result = layout({ graph: g, now: NOW });
    const main = result.lanes.find((l) => l.name === "main");
    expect(main?.markers).toEqual([
      {
        ref: { kind: "note", id: "g1", short: "g1", title: "note g1" },
        type: "created",
        time: 100,
        sha: "sha-g1",
        detail: {},
        subRow: 0,
        orphanBranch: "ghost-branch",
      },
    ]);
  });

  it("attributes an empty branch to the trunk without a flag", () => {
    const g = graph({
      lanes: [lane("main")],
      events: [noteEvent("e1", 100, "")],
    });
    const result = layout({ graph: g, now: NOW });
    const marker = result.lanes.find((l) => l.name === "main")?.markers[0];
    expect(marker?.orphanBranch).toBeUndefined();
  });

  it("emits sprint/project bands on the trunk row from their date ranges", () => {
    const g = graph({
      lanes: [lane("main")],
      entities: [
        {
          kind: "sprint",
          id: "s1",
          short: "s1",
          title: "Sprint 1",
          start_date: 100,
          end_date: 300,
        },
        {
          kind: "project",
          id: "p1",
          short: "p1",
          title: "Proj 1",
          start_date: 50,
          end_date: 0,
        },
      ],
    });
    const result = layout({ graph: g, now: NOW });
    expect(result.bands).toEqual([
      {
        ref: { kind: "project", id: "p1", short: "p1", title: "Proj 1" },
        kind: "project",
        start: 50,
        end: NOW,
        open: true,
        row: 0,
      },
      {
        ref: { kind: "sprint", id: "s1", short: "s1", title: "Sprint 1" },
        kind: "sprint",
        start: 100,
        end: 300,
        open: false,
        row: 0,
      },
    ]);
  });
});

describe("layout window clamping", () => {
  it("clamps a pre-window merged lane's connectors and a straddling span, and drops marks before the window", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 100, end: 0 }),
        lane("old", {
          parent: "main",
          status: "merged",
          fork: { sha: "f", time: 40 },
          merge: { sha: "m", time: 60, into: "main", kind: "merge" },
          start: 40,
          end: 60,
        }),
      ],
      entities: [
        task("straddle", { branch: "main", status: "done", started_at: 50, closed_at: 150 }),
        task("before", { branch: "main", status: "done", started_at: 10, closed_at: 40 }),
      ],
      events: [noteEvent("early", 30, "main")],
    });
    const result = layout({ graph: g, now: NOW });

    // The window floors at the trunk's start.
    expect(result.domain[0]).toBe(100);

    // The pre-window fork/merge connectors clamp to the window edge, flagged, so
    // they land on the trunk rail instead of dangling left of it.
    expect(result.connectors.filter((c) => c.kind === "fork")).toEqual([
      { kind: "fork", time: 100, childRow: 1, parentRow: 0, dashed: false, clamped: true },
    ]);
    expect(result.connectors.filter((c) => c.kind === "merge")).toEqual([
      { kind: "merge", time: 100, childRow: 1, parentRow: 0, dashed: false, clamped: true },
    ]);

    const main = result.lanes.find((l) => l.name === "main");
    // The straddling span keeps its end but clamps its start and is flagged; the
    // fully-before span is dropped.
    expect(main?.spans).toEqual([
      {
        ref: { kind: "task", id: "straddle", short: "straddl", title: "task straddle" },
        start: 100,
        end: 150,
        open: false,
        status: "done",
        subRow: 0,
        clamped: true,
      },
    ]);
    // The pre-window marker is dropped.
    expect(main?.markers).toEqual([]);
  });
});

describe("layout determinism", () => {
  it("produces deep-equal output across two runs", () => {
    const g = graph({
      lanes: [
        lane("main", { start: 5 }),
        lane("feat-b", { parent: "main", fork: { sha: "b", time: 40 }, start: 40, end: 60, status: "merged", merge: { sha: "mb", time: 60, into: "main", kind: "merge" } }),
        lane("feat-a", { parent: "main", fork: { sha: "a", time: 20 }, start: 20, end: 0 }),
        lane("dead", { parent: "", status: "deleted", inferred: true, start: 12, end: 30, merge: { sha: "md", time: 30, into: "main", kind: "inferred" } }),
      ],
      events: [
        noteEvent("n1", 22, "feat-a"),
        noteEvent("n2", 24, "feat-a"),
        noteEvent("n3", 8, ""),
      ],
      entities: [
        task("t1", { branch: "feat-a", status: "done", started_at: 21, closed_at: 55 }),
        {
          kind: "sprint",
          id: "s1",
          short: "s1",
          title: "Sprint 1",
          start_date: 5,
          end_date: 0,
        },
      ],
    });
    const a = layout({ graph: g, now: NOW });
    const b = layout({ graph: g, now: NOW });
    expect(a).toEqual(b);
  });
});
