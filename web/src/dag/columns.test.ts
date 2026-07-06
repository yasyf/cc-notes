import { describe, expect, it } from "vitest";
import { assignColumns, type CommitInput, type DagLayout } from "./columns";

// The algorithm is a pure fold over commits ordered newest -> oldest. Every case
// pins exact column and edge values against a hand-traced expectation; the merge
// and octopus fixtures double as the reuse and multi-parent proofs.

function run(commits: CommitInput[]): DagLayout {
  return assignColumns(commits);
}

describe("assignColumns", () => {
  it("keeps a linear chain in a single column", () => {
    const out = run([
      { sha: "C", parents: ["B"] },
      { sha: "B", parents: ["A"] },
      { sha: "A", parents: [] },
    ]);
    expect(out).toEqual({
      totalColumns: 1,
      rows: [
        { sha: "C", column: 0, edges: [{ fromColumn: 0, toColumn: 0, kind: "pass" }] },
        { sha: "B", column: 0, edges: [{ fromColumn: 0, toColumn: 0, kind: "pass" }] },
        { sha: "A", column: 0, edges: [] },
      ],
    });
  });

  it("forks a merge to a new column, merges back to 0, and reuses the freed column", () => {
    // Two feature branches: D merges G1 (off C) at the top, B merges G2 (off A)
    // at the bottom. G1 frees column 1, which B's merge must reuse (not column 2).
    const out = run([
      { sha: "D", parents: ["C", "G1"] },
      { sha: "G1", parents: ["C"] },
      { sha: "C", parents: ["B"] },
      { sha: "B", parents: ["A", "G2"] },
      { sha: "G2", parents: ["A"] },
      { sha: "A", parents: [] },
    ]);
    expect(out).toEqual({
      totalColumns: 2,
      rows: [
        {
          sha: "D",
          column: 0,
          edges: [
            { fromColumn: 0, toColumn: 0, kind: "pass" },
            { fromColumn: 0, toColumn: 1, kind: "fork" },
          ],
        },
        {
          sha: "G1",
          column: 1,
          edges: [
            { fromColumn: 0, toColumn: 0, kind: "pass" },
            { fromColumn: 1, toColumn: 0, kind: "merge" },
          ],
        },
        { sha: "C", column: 0, edges: [{ fromColumn: 0, toColumn: 0, kind: "pass" }] },
        {
          sha: "B",
          column: 0,
          edges: [
            { fromColumn: 0, toColumn: 0, kind: "pass" },
            { fromColumn: 0, toColumn: 1, kind: "fork" },
          ],
        },
        {
          sha: "G2",
          column: 1,
          edges: [
            { fromColumn: 0, toColumn: 0, kind: "pass" },
            { fromColumn: 1, toColumn: 0, kind: "merge" },
          ],
        },
        { sha: "A", column: 0, edges: [] },
      ],
    });
    // The reuse invariant, called out explicitly.
    expect(out.rows[3].edges).toContainEqual({ fromColumn: 0, toColumn: 1, kind: "fork" });
    expect(out.totalColumns).toBe(2);
  });

  it("places two independent roots in parallel columns", () => {
    // Interleaved chains so both are active at once — the only way roots share
    // the graph vertically.
    const out = run([
      { sha: "A2", parents: ["A1"] },
      { sha: "B2", parents: ["B1"] },
      { sha: "A1", parents: [] },
      { sha: "B1", parents: [] },
    ]);
    expect(out.totalColumns).toBe(2);
    expect(out.rows[0]).toEqual({
      sha: "A2",
      column: 0,
      edges: [{ fromColumn: 0, toColumn: 0, kind: "pass" }],
    });
    expect(out.rows[1]).toEqual({
      sha: "B2",
      column: 1,
      edges: [
        { fromColumn: 0, toColumn: 0, kind: "pass" },
        { fromColumn: 1, toColumn: 1, kind: "pass" },
      ],
    });
    expect(out.rows[2]).toEqual({
      sha: "A1",
      column: 0,
      edges: [{ fromColumn: 1, toColumn: 1, kind: "pass" }],
    });
    expect(out.rows[3]).toEqual({ sha: "B1", column: 1, edges: [] });
  });

  it("opens two extra columns for an octopus merge", () => {
    const out = run([
      { sha: "O", parents: ["X", "Y", "Z"] },
      { sha: "X", parents: [] },
      { sha: "Y", parents: [] },
      { sha: "Z", parents: [] },
    ]);
    expect(out.totalColumns).toBe(3);
    expect(out.rows[0]).toEqual({
      sha: "O",
      column: 0,
      edges: [
        { fromColumn: 0, toColumn: 0, kind: "pass" },
        { fromColumn: 0, toColumn: 1, kind: "fork" },
        { fromColumn: 0, toColumn: 2, kind: "fork" },
      ],
    });
  });

  it("terminates a parent outside the page as an open edge", () => {
    const single = run([{ sha: "X", parents: ["Y"] }]);
    expect(single.rows[0].edges).toEqual([{ fromColumn: 0, toColumn: 0, kind: "open" }]);
    expect(single.totalColumns).toBe(1);

    // A merge whose second parent is off-page: the in-page first parent still
    // continues, the missing parent dangles open.
    const merge = run([
      { sha: "M", parents: ["A", "Q"] },
      { sha: "A", parents: [] },
    ]);
    expect(merge.rows[0].edges).toEqual([
      { fromColumn: 0, toColumn: 0, kind: "pass" },
      { fromColumn: 0, toColumn: 0, kind: "open" },
    ]);
  });

  it("dangles a parent already drawn above as open (timestamp-tie ordering)", () => {
    // A timestamp tie can make the multi-tip walk emit a parent (P) before a
    // cross-branch child (C). P is already placed above, so C's edge to it can't
    // be a downward rail — it degrades to an open stub rather than a phantom rail
    // re-expecting P forever.
    const out = run([
      { sha: "P", parents: [] },
      { sha: "C", parents: ["P"] },
    ]);
    expect(out.rows[0]).toEqual({ sha: "P", column: 0, edges: [] });
    expect(out.rows[1].edges).toEqual([{ fromColumn: out.rows[1].column, toColumn: out.rows[1].column, kind: "open" }]);
  });

  it("dedupes duplicate parents without opening a spurious column", () => {
    const out = run([
      { sha: "D", parents: ["X", "X"] },
      { sha: "X", parents: [] },
    ]);
    expect(out.totalColumns).toBe(1);
    expect(out.rows[0].edges).toEqual([{ fromColumn: 0, toColumn: 0, kind: "pass" }]);
  });

  it("is invariant to paging: concatenated pages equal a single fetch", () => {
    const full: CommitInput[] = [
      { sha: "D", parents: ["C", "G1"] },
      { sha: "G1", parents: ["C"] },
      { sha: "C", parents: ["B"] },
      { sha: "B", parents: ["A", "G2"] },
      { sha: "G2", parents: ["A"] },
      { sha: "A", parents: [] },
    ];
    const page1 = full.slice(0, 3);
    const page2 = full.slice(3);
    const concatenated = [...page1, ...page2];

    expect(run(concatenated)).toEqual(run(full));

    // Paging actually matters: page 1 alone dangles C's parent B as open, while
    // the concatenated run resolves it to a straight pass.
    const firstPageOnly = run(page1);
    expect(firstPageOnly.rows[2].edges).toEqual([{ fromColumn: 0, toColumn: 0, kind: "open" }]);
    expect(run(full).rows[2].edges).toEqual([{ fromColumn: 0, toColumn: 0, kind: "pass" }]);
  });

  it("is deterministic across repeated runs", () => {
    const commits: CommitInput[] = [
      { sha: "D", parents: ["C", "G1"] },
      { sha: "G1", parents: ["C"] },
      { sha: "C", parents: ["B"] },
      { sha: "B", parents: ["A", "G2"] },
      { sha: "G2", parents: ["A"] },
      { sha: "A", parents: [] },
    ];
    expect(run(commits)).toEqual(run(commits.map((c) => ({ ...c, parents: [...c.parents] }))));
  });
});
