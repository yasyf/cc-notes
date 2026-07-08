// Faceted filter bar for the Browse tab. Facets combine AND across facets and OR
// within one; the three quality flags (stale / superseded / never-verified) are
// one OR-within facet. Only non-empty values are offered, so a facet never
// matches status-less or unassigned kinds by accident. The Filters type and its
// pure predicate live here and are consumed by Browse.

import { shortId } from "../detail/format";
import { KINDS, statusRank, type Row } from "./index";

export type FlagKey = "stale" | "superseded" | "never-verified";
export type FacetKey =
  | "kind"
  | "status"
  | "priority"
  | "assignee"
  | "sprint"
  | "project"
  | "tag"
  | "flags";

export interface Filters {
  kind: Set<string>;
  status: Set<string>;
  priority: Set<string>;
  assignee: Set<string>;
  sprint: Set<string>;
  project: Set<string>;
  tag: Set<string>;
  flags: Set<string>;
}

export function emptyFilters(): Filters {
  return {
    kind: new Set(),
    status: new Set(),
    priority: new Set(),
    assignee: new Set(),
    sprint: new Set(),
    project: new Set(),
    tag: new Set(),
    flags: new Set(),
  };
}

export function filterCount(f: Filters): number {
  return (
    f.kind.size +
    f.status.size +
    f.priority.size +
    f.assignee.size +
    f.sprint.size +
    f.project.size +
    f.tag.size +
    f.flags.size
  );
}

function flagged(row: Row, flags: Set<string>): boolean {
  return (
    (flags.has("stale") && row.stale) ||
    (flags.has("superseded") && row.superseded) ||
    (flags.has("never-verified") && row.neverVerified)
  );
}

// matchesFilters reports whether a row survives the active facets: AND across
// facets, OR within each. An empty facet imposes no constraint.
export function matchesFilters(row: Row, f: Filters): boolean {
  return (
    (f.kind.size === 0 || f.kind.has(row.kind)) &&
    (f.status.size === 0 || f.status.has(row.status)) &&
    (f.priority.size === 0 || (row.priority !== null && f.priority.has(String(row.priority)))) &&
    (f.assignee.size === 0 || f.assignee.has(row.assignee)) &&
    (f.sprint.size === 0 || f.sprint.has(row.sprint)) &&
    (f.project.size === 0 || f.project.has(row.project)) &&
    (f.tag.size === 0 || row.tags.some((t) => f.tag.has(t))) &&
    (f.flags.size === 0 || flagged(row, f.flags))
  );
}

interface Option {
  value: string;
  label: string;
  count: number;
}

function tally(values: string[]): Map<string, number> {
  const m = new Map<string, number>();
  for (const v of values) {
    if (v !== "") m.set(v, (m.get(v) ?? 0) + 1);
  }
  return m;
}

function alpha(a: Option, b: Option): number {
  return a.label.localeCompare(b.label);
}

interface Props {
  rows: readonly Row[];
  filters: Filters;
  titles: Map<string, string>;
  onToggle: (facet: FacetKey, value: string) => void;
  onClear: () => void;
}

export function FilterBar({ rows, filters, titles, onToggle, onClear }: Props) {
  const label = (id: string) => {
    const t = titles.get(id);
    return t !== undefined && t !== "" ? t : shortId(id);
  };

  const kindOpts: Option[] = KINDS.map((k) => ({
    value: k,
    label: k,
    count: rows.reduce((n, r) => n + (r.kind === k ? 1 : 0), 0),
  })).filter((o) => o.count > 0);

  const statusOpts: Option[] = [...tally(rows.map((r) => r.status))]
    .map(([value, count]) => ({ value, label: value.replace(/_/g, " "), count }))
    .sort((a, b) => statusRank(a.value) - statusRank(b.value) || alpha(a, b));

  const priorityOpts: Option[] = [
    ...tally(rows.map((r) => (r.priority !== null ? String(r.priority) : ""))),
  ]
    .map(([value, count]) => ({ value, label: `P${value}`, count }))
    .sort((a, b) => Number(a.value) - Number(b.value));

  const assigneeOpts: Option[] = [...tally(rows.map((r) => r.assignee))]
    .map(([value, count]) => ({ value, label: value, count }))
    .sort(alpha);

  const sprintOpts: Option[] = [...tally(rows.map((r) => r.sprint))]
    .map(([value, count]) => ({ value, label: label(value), count }))
    .sort(alpha);

  const projectOpts: Option[] = [...tally(rows.map((r) => r.project))]
    .map(([value, count]) => ({ value, label: label(value), count }))
    .sort(alpha);

  const tagOpts: Option[] = [...tally(rows.flatMap((r) => r.tags))]
    .map(([value, count]) => ({ value, label: value, count }))
    .sort(alpha);

  const flagOpts: Option[] = [
    { value: "stale", label: "stale", count: rows.reduce((n, r) => n + (r.stale ? 1 : 0), 0) },
    {
      value: "superseded",
      label: "superseded",
      count: rows.reduce((n, r) => n + (r.superseded ? 1 : 0), 0),
    },
    {
      value: "never-verified",
      label: "never verified",
      count: rows.reduce((n, r) => n + (r.neverVerified ? 1 : 0), 0),
    },
  ].filter((o) => o.count > 0);

  const groups: { key: FacetKey; label: string; opts: Option[] }[] = [
    { key: "kind", label: "kind", opts: kindOpts },
    { key: "status", label: "status", opts: statusOpts },
    { key: "priority", label: "priority", opts: priorityOpts },
    { key: "assignee", label: "assignee", opts: assigneeOpts },
    { key: "sprint", label: "sprint", opts: sprintOpts },
    { key: "project", label: "project", opts: projectOpts },
    { key: "tag", label: "tag", opts: tagOpts },
    { key: "flags", label: "flags", opts: flagOpts },
  ];

  return (
    <div className="filterbar">
      {groups
        .filter((g) => g.opts.length > 0)
        .map((g) => (
          <div className="facet" key={g.key}>
            <span className="facet-label">{g.label}</span>
            <div className="facet-chips">
              {g.opts.map((o) => {
                const active = filters[g.key].has(o.value);
                return (
                  <button
                    type="button"
                    key={o.value}
                    className={active ? "facet-chip facet-chip-on" : "facet-chip"}
                    aria-pressed={active}
                    onClick={() => onToggle(g.key, o.value)}
                  >
                    {o.label}
                    <span className="facet-count">{o.count}</span>
                  </button>
                );
              })}
            </div>
          </div>
        ))}
      {filterCount(filters) > 0 && (
        <button type="button" className="facet-clear" onClick={onClear}>
          clear filters
        </button>
      )}
    </div>
  );
}
