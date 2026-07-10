import { describe, expect, it } from "vitest";
import type { EntitySummary, Event, Graph, Lane, RepoInfo } from "../api";
import {
  applyFilters,
  filtersActive,
  NO_FILTERS,
  presentKinds,
  presentTypes,
  type TimelineFilters,
} from "./filters";

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

function ev(kind: string, type: string, branch: string): Event {
  return {
    entity: { kind, id: `${kind}-${type}`, short: kind, title: `${kind} ${type}` },
    type,
    time: 100,
    branch,
    sha: "s",
    detail: {},
  };
}

function task(id: string, over: Partial<EntitySummary>): EntitySummary {
  return { kind: "task", id, short: id, title: id, ...over };
}

function graph(over: Partial<Graph> = {}): Graph {
  return { repo: repo(), lanes: [lane("main")], events: [], entities: [], ...over };
}

function filters(over: Partial<TimelineFilters>): TimelineFilters {
  return { ...NO_FILTERS, ...over };
}

describe("applyFilters", () => {
  it("returns the same graph reference when nothing is filtered", () => {
    const g = graph({ events: [ev("note", "created", "")] });
    expect(applyFilters(g, NO_FILTERS)).toBe(g);
  });

  it("drops events and entities of a hidden kind", () => {
    const g = graph({
      events: [ev("note", "created", ""), ev("doc", "created", "")],
      entities: [task("t1", { branch: "main", started_at: 10 })],
    });
    const out = applyFilters(g, filters({ hiddenKinds: new Set(["note", "task"]) }));
    expect(out.events.map((e) => e.entity.kind)).toEqual(["doc"]);
    expect(out.entities).toEqual([]);
  });

  it("drops markers of a hidden event type", () => {
    const g = graph({
      events: [ev("note", "created", ""), ev("note", "edited", "")],
    });
    const out = applyFilters(g, filters({ hiddenTypes: new Set(["edited"]) }));
    expect(out.events.map((e) => e.type)).toEqual(["created"]);
  });

  it("keeps only a focused lane's events, tasks, and (for trunk) bands", () => {
    const g = graph({
      lanes: [lane("main"), lane("feat")],
      events: [ev("note", "created", "feat"), ev("note", "created", "main"), ev("log", "entry", "")],
      entities: [
        task("t1", { branch: "feat", started_at: 10 }),
        task("t2", { branch: "main", started_at: 10 }),
        { kind: "sprint", id: "s1", short: "s1", title: "S1", start_date: 5 },
      ],
    });

    const feat = applyFilters(g, filters({ lane: "feat" }));
    expect(feat.events.map((e) => e.branch)).toEqual(["feat"]);
    expect(feat.entities.map((e) => e.id)).toEqual(["t1"]);

    // Focusing the trunk keeps its own branch items, the empty-branch orphan
    // event, and the sprint band that lives on the trunk row.
    const main = applyFilters(g, filters({ lane: "main" }));
    expect(main.events.map((e) => e.branch).sort()).toEqual(["", "main"]);
    expect(main.entities.map((e) => e.id).sort()).toEqual(["s1", "t2"]);
  });
});

describe("filtersActive", () => {
  it("is false only for the empty filter", () => {
    expect(filtersActive(NO_FILTERS)).toBe(false);
    expect(filtersActive(filters({ lane: "x" }))).toBe(true);
    expect(filtersActive(filters({ hiddenKinds: new Set(["note"]) }))).toBe(true);
    expect(filtersActive(filters({ hiddenTypes: new Set(["created"]) }))).toBe(true);
  });
});

describe("present sets", () => {
  it("lists rendered kinds only: marker events, started tasks, dated bands", () => {
    const g = graph({
      events: [ev("note", "created", ""), ev("task", "status", "")],
      entities: [
        task("t1", { started_at: 10 }),
        task("t2", { started_at: 0 }),
        { kind: "sprint", id: "s1", short: "s", title: "S", start_date: 5 },
        { kind: "project", id: "p1", short: "p", title: "P", start_date: 0 },
      ],
    });
    expect([...presentKinds(g)].sort()).toEqual(["note", "sprint", "task"]);
  });

  it("lists only marker event types, excluding task-lifecycle events", () => {
    const g = graph({
      events: [ev("note", "created", ""), ev("doc", "edited", ""), ev("task", "closed", "")],
    });
    expect([...presentTypes(g)].sort()).toEqual(["created", "edited"]);
  });

  it("counts runbook as a present marker kind and its run events as present types", () => {
    const g = graph({
      events: [ev("runbook", "run_started", ""), ev("runbook", "run_finished", "")],
    });
    expect([...presentKinds(g)]).toEqual(["runbook"]);
    expect([...presentTypes(g)].sort()).toEqual(["run_finished", "run_started"]);
  });
});
