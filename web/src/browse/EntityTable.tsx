// Dense, sortable table across every entity kind. Column sorting is driven from
// Browse (which decides search-relevance vs column order), so this component
// renders already-ordered rows and reports header clicks. sortRows and the
// SortState type live here with the column comparators.

import { relativeTime } from "../dag/badges";
import { nowSec, shortId } from "../detail/format";
import { StatusBadge } from "../detail/parts";
import { DISPLAY_KINDS } from "../kinds";
import type { Selection } from "../store";
import { compareDefault, statusRank, type Row } from "./index";
import { Highlight, KindBadge, PriorityBadge } from "./parts";
import type { Span } from "./search";

export type ColKey =
  | "title"
  | "kind"
  | "status"
  | "priority"
  | "assignee"
  | "branch"
  | "group"
  | "updated"
  | "verified";

export interface SortState {
  col: ColKey | null; // null = the default status/priority/updated order
  dir: "asc" | "desc";
}

// DEFAULT_DIR is the direction a freshly-clicked column starts in: numeric and
// time columns read best newest/most-urgent first.
const DEFAULT_DIR: Record<ColKey, "asc" | "desc"> = {
  title: "asc",
  kind: "asc",
  status: "asc",
  priority: "asc",
  assignee: "asc",
  branch: "asc",
  group: "asc",
  updated: "desc",
  verified: "desc",
};

const COLUMNS: { key: ColKey; label: string }[] = [
  { key: "title", label: "Title" },
  { key: "kind", label: "Kind" },
  { key: "status", label: "Status" },
  { key: "priority", label: "Priority" },
  { key: "assignee", label: "Assignee" },
  { key: "branch", label: "Branch" },
  { key: "group", label: "Sprint / Project" },
  { key: "updated", label: "Updated" },
  { key: "verified", label: "Verified" },
];

function groupId(row: Row): string {
  return row.sprint !== "" ? row.sprint : row.project;
}

function colCompare(col: ColKey, a: Row, b: Row, titles: Map<string, string>): number {
  switch (col) {
    case "title":
      return a.titleLower.localeCompare(b.titleLower);
    case "kind":
      return DISPLAY_KINDS.indexOf(a.kind) - DISPLAY_KINDS.indexOf(b.kind);
    case "status":
      return statusRank(a.status) - statusRank(b.status) || a.status.localeCompare(b.status);
    case "priority":
      return (a.priority ?? 99) - (b.priority ?? 99);
    case "assignee":
      return a.assignee.localeCompare(b.assignee);
    case "branch":
      return a.branch.localeCompare(b.branch);
    case "group": {
      const la = titles.get(groupId(a)) ?? groupId(a);
      const lb = titles.get(groupId(b)) ?? groupId(b);
      return la.localeCompare(lb);
    }
    case "updated":
      return a.updated - b.updated;
    case "verified":
      return a.verifiedAt - b.verifiedAt;
  }
}

// sortRows returns rows in the requested order, tie-broken deterministically by
// id. A null column falls back to the default status/priority/updated order.
export function sortRows(rows: readonly Row[], sort: SortState, titles: Map<string, string>): Row[] {
  const out = [...rows];
  if (sort.col === null) {
    out.sort(compareDefault);
    return out;
  }
  const col = sort.col;
  out.sort((a, b) => {
    const c = colCompare(col, a, b, titles);
    if (c !== 0) return sort.dir === "asc" ? c : -c;
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
  return out;
}

// nextSort toggles direction when the same column is clicked again, else selects
// the new column at its natural default direction.
export function nextSort(sort: SortState, col: ColKey): SortState {
  if (sort.col === col) return { col, dir: sort.dir === "asc" ? "desc" : "asc" };
  return { col, dir: DEFAULT_DIR[col] };
}

interface Props {
  rows: readonly Row[];
  titles: Map<string, string>;
  sort: SortState;
  bySearch: boolean;
  spans: Map<string, Span[]>;
  selection: Selection | null;
  onSort: (col: ColKey) => void;
  onSelect: (sel: Selection) => void;
}

export function EntityTable({
  rows,
  titles,
  sort,
  bySearch,
  spans,
  selection,
  onSort,
  onSelect,
}: Props) {
  const now = nowSec();
  return (
    <div className="table-wrap">
      <table className="etable">
        <thead>
          <tr>
            {COLUMNS.map((c) => {
              const active = !bySearch && sort.col === c.key;
              return (
                <th key={c.key} className={`etable-th etable-th-${c.key}`}>
                  <button type="button" className="etable-sort" onClick={() => onSort(c.key)}>
                    {c.label}
                    {active && <span className="etable-arrow">{sort.dir === "asc" ? "▲" : "▼"}</span>}
                  </button>
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const gid = groupId(r);
            const selected = selection !== null && selection.kind === r.kind && selection.id === r.id;
            return (
              <tr
                key={`${r.kind}:${r.id}`}
                className={selected ? "etable-row etable-row-on" : "etable-row"}
                onClick={() => onSelect({ kind: r.kind, id: r.id, title: r.title })}
              >
                <td className="etable-title">
                  <Highlight text={r.title || shortId(r.id)} spans={spans.get(r.id) ?? []} />
                </td>
                <td>
                  <KindBadge kind={r.kind} />
                </td>
                <td>{r.status !== "" ? <StatusBadge status={r.status} /> : <Dash />}</td>
                <td>{r.priority !== null ? <PriorityBadge priority={r.priority} /> : <Dash />}</td>
                <td className="etable-assignee">{r.assignee || <Dash />}</td>
                <td className="etable-branch">
                  {r.branch !== "" ? <code>{r.branch}</code> : <Dash />}
                </td>
                <td className="etable-group">
                  {gid !== "" ? titles.get(gid) ?? shortId(gid) : <Dash />}
                </td>
                <td className="etable-time">{relativeTime(r.updated, now)}</td>
                <td className="etable-time">
                  {r.verifiable ? (
                    r.verifiedAt > 0 ? (
                      relativeTime(r.verifiedAt, now)
                    ) : (
                      <span className="etable-never">never</span>
                    )
                  ) : (
                    <Dash />
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {rows.length === 0 && <p className="browse-empty">No entities match.</p>}
    </div>
  );
}

function Dash() {
  return <span className="etable-dash">—</span>;
}
