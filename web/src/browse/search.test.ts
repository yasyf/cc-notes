import { describe, expect, it } from "vitest";
import { search, type SearchTarget } from "./search";

function mk(id: string, title: string, body = ""): SearchTarget {
  return {
    id,
    kind: "task",
    title,
    titleLower: title.toLowerCase(),
    bodyLower: body.toLowerCase(),
  };
}

function ids(targets: SearchTarget[], q: string): string[] {
  return search(targets, q).map((h) => h.id);
}

describe("search ranking", () => {
  it("returns nothing for an empty query", () => {
    expect(search([mk("a", "sync")], "")).toEqual([]);
  });

  it("returns nothing for a whitespace-only query", () => {
    expect(search([mk("a", "sync")], "   ")).toEqual([]);
  });

  it("excludes non-matches", () => {
    expect(ids([mk("a", "sync"), mk("b", "build")], "zzz")).toEqual([]);
  });

  it("orders the full tier ladder best-first", () => {
    const targets = [
      mk("subseq", "s.y.n.c"), // subsequence only
      mk("field", "other", "sync here"), // substring in a non-title field
      mk("sub", "resync"), // mid-word title substring
      mk("word", "re sync now"), // title word-prefix
      mk("prefix", "syncer"), // whole-title prefix
      mk("exact", "sync"), // exact title
    ];
    expect(ids(targets, "sync")).toEqual([
      "exact",
      "prefix",
      "word",
      "sub",
      "field",
      "subseq",
    ]);
  });

  it("is case-insensitive", () => {
    expect(ids([mk("a", "Fix Sync")], "SYNC")).toEqual(["a"]);
  });

  it("ranks a field substring above a title-only subsequence", () => {
    const targets = [mk("subseq", "s_y_n_c"), mk("field", "abc", "xsyncx")];
    expect(ids(targets, "sync")).toEqual(["field", "subseq"]);
  });

  it("breaks same-tier ties by earlier match position", () => {
    const targets = [mk("p3", "xxxsync"), mk("p1", "xsync")];
    expect(ids(targets, "sync")).toEqual(["p1", "p3"]);
  });

  it("breaks remaining ties by id", () => {
    const targets = [mk("zid", "sync-a"), mk("aid", "sync-b")];
    expect(ids(targets, "sync")).toEqual(["aid", "zid"]);
  });

  it("highlights a title word-prefix match", () => {
    const hits = search([mk("a", "Fix Sync")], "sync");
    expect(hits[0]?.score).toBe(4);
    expect(hits[0]?.spans).toEqual([[4, 8]]);
  });

  it("highlights a subsequence match with merged runs", () => {
    const hits = search([mk("a", "task")], "tsk");
    expect(hits[0]?.score).toBe(1);
    expect(hits[0]?.spans).toEqual([
      [0, 1],
      [2, 4],
    ]);
  });
});
