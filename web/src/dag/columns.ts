// Pure git-graph column assignment: a list of commits ordered newest -> oldest
// (as GET /api/commits returns them) -> a typed DagLayout the SVG renderer
// consumes verbatim. No DOM, no React, no randomness — the same input always
// yields the same output, so the whole engine is table-testable.
//
// Algorithm: maintain an ordered list of active columns, each holding the sha it
// expects to draw next (or null when free). For each commit, newest first:
//   * it takes the leftmost column already expecting its sha, or opens a new
//     rightmost column (a fresh tip);
//   * its first parent inherits that column (a straight rail) unless the parent
//     is already expected elsewhere, in which case the commit's line merges into
//     that column and its own column frees;
//   * each additional parent joins a column already expecting it (the merge
//     edge) or opens a column — reusing the leftmost freed slot before appending;
//   * a parent absent from the page terminates as an "open" edge (paging
//     boundary).
// Columns freed by a commit are reusable by later (older) branches, so interior
// slots do not leak.

export type EdgeKind = "pass" | "fork" | "merge" | "open";

// ColumnEdge connects a column at this commit's row (fromColumn, at the dot's
// vertical centre) to a column at the next, older row (toColumn, the row's
// bottom). A pass runs straight, a fork diverges right, a merge converges left,
// and an open dangles at the paging boundary (fromColumn === toColumn).
export interface ColumnEdge {
  fromColumn: number;
  toColumn: number;
  kind: EdgeKind;
}

// CommitInput is the minimal shape the algorithm reads: a sha and its parents,
// newest first. Order of parents is significant — parents[0] is the first parent
// and inherits the commit's column.
export interface CommitInput {
  sha: string;
  parents: string[];
}

// CommitLayout is one placed commit: its column, and the edges descending from
// its row to the next.
export interface CommitLayout {
  sha: string;
  column: number;
  edges: ColumnEdge[];
}

// DagLayout is the full placement: one row per input commit (same order) plus
// the total column count the renderer sizes the graph gutter to.
export interface DagLayout {
  rows: CommitLayout[];
  totalColumns: number;
}

function edgeKind(from: number, to: number): EdgeKind {
  if (from === to) return "pass";
  return to > from ? "fork" : "merge";
}

// assignColumns places every commit into a column and derives its descending
// edges. Commits must arrive newest -> oldest; parents older than the page
// boundary yield "open" edges.
export function assignColumns(commits: CommitInput[]): DagLayout {
  const inPage = new Set(commits.map((c) => c.sha));
  const columns: (string | null)[] = []; // active columns; null = freed slot
  const emitted = new Set<string>(); // commits already placed (rows above)
  const rows: CommitLayout[] = [];
  let totalColumns = 0;

  const leftmostExpecting = (sha: string): number => columns.indexOf(sha);
  const leftmostFree = (): number => columns.indexOf(null);

  for (const commit of commits) {
    // The commit's own column: the leftmost slot expecting it, or a fresh
    // rightmost column when no child in the page pointed here (a tip).
    let col = leftmostExpecting(commit.sha);
    if (col < 0) {
      col = columns.length;
      columns.push(commit.sha);
    }

    // Columns entering this row (before mutation) drive the pass-through rails.
    const entering: number[] = [];
    for (let i = 0; i < columns.length; i++) {
      if (columns[i] !== null && columns[i] !== commit.sha) entering.push(i);
    }

    // Free every slot this commit consumed (duplicate expectations converge here).
    for (let i = 0; i < columns.length; i++) {
      if (columns[i] === commit.sha) columns[i] = null;
    }

    const edges: ColumnEdge[] = [];
    const pushEdge = (from: number, to: number, kind: EdgeKind) => {
      edges.push({ fromColumn: from, toColumn: to, kind });
    };

    // Pass-through rails: untouched columns continue straight down.
    for (const i of entering) pushEdge(i, i, "pass");

    // Parents, deduped, in order. The first parent inherits `col`.
    const seen = new Set<string>();
    let first = true;
    for (const parent of commit.parents) {
      if (seen.has(parent)) continue;
      seen.add(parent);

      // A parent below the page boundary, or one already drawn above (a
      // timestamp tie can make the multi-tip walk emit a parent before a
      // cross-branch child), can't continue as a downward rail — dangle it open.
      if (!inPage.has(parent) || emitted.has(parent)) {
        pushEdge(col, col, "open");
        first = false;
        continue;
      }

      const existing = leftmostExpecting(parent);
      if (existing >= 0) {
        // Parent already has a column: this line merges into it.
        pushEdge(col, existing, edgeKind(col, existing));
        if (first) columns[col] = null; // first-parent line left its column
      } else if (first) {
        columns[col] = parent; // reclaim the commit's column for a straight rail
        pushEdge(col, col, "pass");
      } else {
        let slot = leftmostFree();
        if (slot < 0) {
          slot = columns.length;
          columns.push(parent);
        } else {
          columns[slot] = parent;
        }
        pushEdge(col, slot, edgeKind(col, slot));
      }
      first = false;
    }

    dedupeEdges(edges);
    rows.push({ sha: commit.sha, column: col, edges });
    emitted.add(commit.sha);
    totalColumns = Math.max(totalColumns, columns.length);
  }

  return { rows, totalColumns };
}

// dedupeEdges collapses identical (from, to, kind) triples in place — two
// out-of-page parents both dangle in the same column, for instance.
function dedupeEdges(edges: ColumnEdge[]): void {
  const seen = new Set<string>();
  let n = 0;
  for (const e of edges) {
    const key = `${e.fromColumn}:${e.toColumn}:${e.kind}`;
    if (seen.has(key)) continue;
    seen.add(key);
    edges[n++] = e;
  }
  edges.length = n;
}
