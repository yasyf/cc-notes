import { describe, expect, it } from "vitest";
import type { RunbookRunSnapshot, RunbookStepSnapshot } from "./api";
import {
  normalizeCommits,
  normalizeEntities,
  normalizeEntity,
  normalizeGraph,
  projectRunSteps,
} from "./api";

// The Go server marshals nil slices and nil maps as JSON null. These payloads are
// captured shapes the wire can actually emit; the normalizers must turn every
// such null into a non-null [] or {} while leaving the by-design nullable lane
// pointers untouched.

describe("normalizeGraph", () => {
  it("fills null top-level slices and a null event detail map", () => {
    // graph with events: null (and null lanes/entities) — a repo with no history.
    const raw = JSON.parse(
      `{"repo":{"root":"/r","trunk":"main","head":"main","generated_at":"","truncated":false},
        "lanes":null,"events":null,"entities":null}`,
    );
    const g = normalizeGraph(raw);
    expect(g.lanes).toEqual([]);
    expect(g.events).toEqual([]);
    expect(g.entities).toEqual([]);
  });

  it("fills a per-event null detail map but keeps present details", () => {
    const raw = JSON.parse(
      `{"repo":{"root":"/r","trunk":"main","head":"main","generated_at":"","truncated":false},
        "lanes":[{"name":"main","parent":"","fork":null,"merge":null,"status":"active",
          "inferred":false,"tip":null,"start":0,"end":0,"commits":0}],
        "events":[
          {"entity":{"kind":"note","id":"n1","short":"n1","title":"n"},"type":"created",
            "time":10,"branch":"main","sha":"s1","detail":null},
          {"entity":{"kind":"log","id":"l1","short":"l1","title":"l"},"type":"entry",
            "time":20,"branch":"main","sha":"s2","detail":{"text":"hi"}}
        ],
        "entities":null}`,
    );
    const g = normalizeGraph(raw);
    expect(g.lanes[0].fork).toBeNull(); // by-design pointer stays null
    expect(g.events[0].detail).toEqual({});
    expect(g.events[1].detail).toEqual({ text: "hi" });
  });
});

describe("normalizeCommits", () => {
  it("fills a null commits slice and keeps by-design nullables", () => {
    // A repo with no commits in the window: commits null, no next page.
    const page = normalizeCommits(
      JSON.parse(`{"commits":null,"next_before":null,"truncated":false}`),
    );
    expect(page.commits).toEqual([]);
    expect(page.next_before).toBeNull();
    expect(page.truncated).toBe(false);
  });

  it("fills per-commit null parents/tasks/events and a null event detail map", () => {
    // Go marshals every nil slice/map as JSON null: a root commit carries null
    // parents, an unclaimed commit null branch, and an event with null detail.
    const raw = JSON.parse(
      `{"commits":[
          {"sha":"c2","parents":["c1"],"author":"ann","time":20,"summary":"merge",
            "branch":"main","tasks":["t1"],
            "events":[{"entity":{"kind":"task","id":"t1","short":"t1","title":"t"},
              "type":"closed","time":20,"branch":"main","sha":"c2","detail":null}]},
          {"sha":"c1","parents":null,"author":"ann","time":10,"summary":"root",
            "branch":null,"tasks":null,"events":null}
        ],
        "next_before":"c0","truncated":true}`,
    );
    const page = normalizeCommits(raw);

    const [c2, c1] = page.commits;
    expect(c2.parents).toEqual(["c1"]);
    expect(c2.tasks).toEqual(["t1"]);
    expect(c2.events[0].detail).toEqual({}); // null detail -> {}
    expect(c2.branch).toBe("main");

    expect(c1.parents).toEqual([]); // null parents -> []
    expect(c1.tasks).toEqual([]);
    expect(c1.events).toEqual([]);
    expect(c1.branch).toBeNull(); // by-design nullable stays null

    expect(page.next_before).toBe("c0");
    expect(page.truncated).toBe(true);
  });
});

describe("normalizeEntity", () => {
  it("fills a null set side of a non-scalar change and passes the snapshot through", () => {
    // A claimed task's comments change: added present, removed null — the payload
    // that crashed Panel before normalization. The snapshot is folded model JSON
    // and must arrive verbatim.
    const raw = JSON.parse(
      `{"summary":{"kind":"task","id":"t1","short":"t1","title":"t","status":"in_progress"},
        "snapshot":{"id":"t1","title":"t","status":"in_progress","priority":1,
          "labels":["ui"],"comments":[{"author":"a","ts":1,"body":"hi"}]},
        "trail":[
          {"sha":"c1","author":"a","time":1,"lamport":1,"kind":"edit","covers":0,
            "changes":[
              {"field":"comments","scalar":false,"from":null,"to":null,"added":["hi"],"removed":null},
              {"field":"status","scalar":true,"from":"open","to":"in_progress","added":null,"removed":null}
            ]}
        ]}`,
    );
    const d = normalizeEntity(raw);
    const [comments, status] = d.trail[0].changes;
    expect(comments.added).toEqual(["hi"]);
    expect(comments.removed).toEqual([]);
    expect(status.added).toEqual([]);
    expect(status.removed).toEqual([]);
    expect(status.from).toBe("open");
    expect(status.to).toBe("in_progress");
    // Snapshot passthrough: identical object, nested fields intact.
    expect(d.snapshot).toEqual({
      id: "t1",
      title: "t",
      status: "in_progress",
      priority: 1,
      labels: ["ui"],
      comments: [{ author: "a", ts: 1, body: "hi" }],
    });
  });

  it("carries typed trail values: null create pre-image, numbers, and an object element", () => {
    // An entity's creation trail: title created (from null), verified_at as a
    // unix-second number, and an attachment added as a folded sub-object — none
    // of which a string-only TrailChange could represent.
    const raw = JSON.parse(
      `{"summary":{"kind":"note","id":"n1","short":"n1","title":"n"},
        "snapshot":{"id":"n1","title":"hello","tags":[]},
        "trail":[
          {"sha":"c1","author":"a","time":1,"lamport":1,"kind":"create","covers":0,
            "changes":[
              {"field":"title","scalar":true,"from":null,"to":"hello","added":null,"removed":null},
              {"field":"verified_at","scalar":true,"from":0,"to":1700000000,"added":null,"removed":null},
              {"field":"attachments","scalar":false,"from":null,"to":null,
                "added":[{"name":"trace.png","oid":"ab12","size":48}],"removed":null}
            ]}
        ]}`,
    );
    const d = normalizeEntity(raw);
    const [title, verifiedAt, attachments] = d.trail[0].changes;
    expect(title.from).toBeNull(); // create pre-image stays null
    expect(title.to).toBe("hello");
    expect(verifiedAt.from).toBe(0); // numeric scalar preserved, not "0"
    expect(verifiedAt.to).toBe(1700000000);
    expect(attachments.added).toEqual([{ name: "trace.png", oid: "ab12", size: 48 }]);
    expect(attachments.removed).toEqual([]);
  });

  it("fills a null trail and a null changes slice", () => {
    const nullTrail = normalizeEntity(
      JSON.parse(
        `{"summary":{"kind":"note","id":"n1","short":"n1","title":"n"},
          "snapshot":{"id":"n1","title":"n","tags":[]},"trail":null}`,
      ),
    );
    expect(nullTrail.trail).toEqual([]);

    const nullChanges = normalizeEntity(
      JSON.parse(
        `{"summary":{"kind":"note","id":"n1","short":"n1","title":"n"},
          "snapshot":{"id":"n1","title":"n","tags":[]},
          "trail":[{"sha":"c1","author":"a","time":1,"lamport":1,"kind":"create",
            "covers":0,"changes":null}]}`,
      ),
    );
    expect(nullChanges.trail[0].changes).toEqual([]);
  });
});

describe("normalizeEntities", () => {
  it("fills every null kind bucket with []", () => {
    // An empty repo: the Go side marshals each nil kind slice as null.
    const state = normalizeEntities(
      JSON.parse(
        `{"notes":null,"docs":null,"logs":null,"tasks":null,"sprints":null,"projects":null,"runbooks":null}`,
      ),
    );
    expect(state.notes).toEqual([]);
    expect(state.docs).toEqual([]);
    expect(state.logs).toEqual([]);
    expect(state.tasks).toEqual([]);
    expect(state.sprints).toEqual([]);
    expect(state.projects).toEqual([]);
    expect(state.runbooks).toEqual([]);
  });

  it("keeps present buckets and passes their snapshots through verbatim", () => {
    const state = normalizeEntities(
      JSON.parse(
        `{"notes":[{"id":"n1","title":"n","tags":["x"]}],
          "docs":null,"logs":null,
          "tasks":[{"id":"t1","title":"t","status":"open","criteria":[{"id":"c1","text":"ships","script":"","status":"pending"}]}],
          "sprints":null,"projects":null,
          "runbooks":[{"id":"rb1","title":"deploy","status":"active",
            "steps":[{"id":"s1","text":"build","command":"make","position":"a0"}],
            "runs":[{"id":"r1","task":"","status":"running","runner":"ann","started_at":5,"finished_at":0,
              "results":[{"step_id":"s1","status":"done","note":"","actor":"ann","ts":6}]}]}]}`,
      ),
    );
    expect(state.docs).toEqual([]);
    expect(state.notes).toEqual([{ id: "n1", title: "n", tags: ["x"] }]);
    expect(state.tasks[0].criteria).toEqual([
      { id: "c1", text: "ships", script: "", status: "pending" },
    ]);
    expect(state.runbooks[0].steps).toEqual([
      { id: "s1", text: "build", command: "make", position: "a0" },
    ]);
    expect(state.runbooks[0].runs[0].results).toEqual([
      { step_id: "s1", status: "done", note: "", actor: "ann", ts: 6 },
    ]);
  });
});

describe("projectRunSteps", () => {
  const steps: RunbookStepSnapshot[] = [
    { id: "a", text: "step A", command: "do-a", position: "a0" },
    { id: "b", text: "step B", command: "", position: "a1" },
    { id: "c", text: "step C", command: "", position: "a2" },
  ];

  function run(over: Partial<RunbookRunSnapshot> = {}): RunbookRunSnapshot {
    return {
      id: "r1",
      task: "",
      status: "running",
      runner: "ann",
      started_at: 1,
      finished_at: 0,
      results: [],
      ...over,
    };
  }

  it("projects results into procedure order with pending for absent steps", () => {
    // Results recorded C then A (sparse, out of order): the projection lists
    // every current step in step order, with B pending between them.
    const projected = projectRunSteps(
      steps,
      run({
        results: [
          { step_id: "c", status: "done", note: "shipped", actor: "ann", ts: 3 },
          { step_id: "a", status: "failed", note: "flaky", actor: "ann", ts: 2 },
        ],
      }),
    );
    expect(projected).toEqual([
      { stepId: "a", text: "step A", command: "do-a", status: "failed", note: "flaky" },
      { stepId: "b", text: "step B", command: "", status: "pending", note: "" },
      { stepId: "c", text: "step C", command: "", status: "done", note: "shipped" },
    ]);
  });

  it("omits results whose step no longer exists", () => {
    const projected = projectRunSteps(
      steps,
      run({
        results: [
          { step_id: "b", status: "skipped", note: "", actor: "ann", ts: 2 },
          { step_id: "gone", status: "done", note: "orphan", actor: "ann", ts: 4 },
        ],
      }),
    );
    expect(projected.map((s) => s.stepId)).toEqual(["a", "b", "c"]);
    expect(projected.find((s) => s.stepId === "b")?.status).toBe("skipped");
    expect(projected.some((s) => s.note === "orphan")).toBe(false);
  });

  it("marks every step pending when the run has no results", () => {
    const projected = projectRunSteps(steps, run());
    expect(projected.map((s) => s.status)).toEqual(["pending", "pending", "pending"]);
  });
});
