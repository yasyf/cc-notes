import { describe, expect, it } from "vitest";
import { normalizeEntity, normalizeGraph } from "./api";

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
