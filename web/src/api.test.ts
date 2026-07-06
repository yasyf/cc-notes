import { describe, expect, it } from "vitest";
import { normalizeCommits, normalizeEntity, normalizeGraph } from "./api";

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
  it("fills a null set side of a non-scalar change (removed:null)", () => {
    // A claimed task's comments change: added present, removed null — the payload
    // that crashed Panel before normalization.
    const raw = JSON.parse(
      `{"summary":{"kind":"task","id":"t1","short":"t1","title":"t","status":"in_progress"},
        "trail":[
          {"sha":"c1","author":"a","time":1,"lamport":1,"kind":"edit","covers":0,
            "changes":[
              {"field":"comments","scalar":false,"from":"","to":"","added":["hi"],"removed":null},
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
  });

  it("fills a null trail and a null changes slice", () => {
    const nullTrail = normalizeEntity(
      JSON.parse(
        `{"summary":{"kind":"note","id":"n1","short":"n1","title":"n"},"trail":null}`,
      ),
    );
    expect(nullTrail.trail).toEqual([]);

    const nullChanges = normalizeEntity(
      JSON.parse(
        `{"summary":{"kind":"note","id":"n1","short":"n1","title":"n"},
          "trail":[{"sha":"c1","author":"a","time":1,"lamport":1,"kind":"create",
            "covers":0,"changes":null}]}`,
      ),
    );
    expect(nullChanges.trail[0].changes).toEqual([]);
  });
});
