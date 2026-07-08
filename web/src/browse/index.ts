// Builds the flat entity index that powers the Browse tab and the header's
// global search: one uniform Row per live entity, folded from the six kind-typed
// snapshots. Every column, facet, and search field is precomputed here so the
// table, kanban, and scorer stay presentational. Pure — no fetch, no DOM.

import type {
  DocSnapshot,
  LogSnapshot,
  NoteSnapshot,
  ProjectSnapshot,
  SprintSnapshot,
  StateResponse,
  TaskSnapshot,
} from "../api";
import type { SearchTarget } from "./search";

export type EntityKind = "note" | "doc" | "log" | "task" | "sprint" | "project";

// KINDS is the fixed display order of the kind facet and kind badges.
export const KINDS: readonly EntityKind[] = ["task", "note", "doc", "log", "sprint", "project"];

// TASK_STATUSES is the kanban column order and the task status lifecycle.
export const TASK_STATUSES = ["open", "in_progress", "done", "cancelled"] as const;

// Row is one entity projected into every field the Browse views consume. It
// satisfies SearchTarget, so the scorer ranks Rows directly.
export interface Row extends SearchTarget {
  kind: EntityKind;
  status: string; // task/sprint/project lifecycle; "" for notes/docs/logs
  priority: number | null; // tasks only (0 = P0, most urgent)
  assignee: string;
  branch: string;
  sprint: string; // task's sprint id
  project: string; // task's or sprint's project id
  tags: string[]; // tags (notes/docs/logs) or labels (tasks/sprints/projects)
  updated: number;
  verifiedAt: number; // notes/docs; 0 otherwise
  verifiable: boolean; // note or doc — supports verification
  stale: boolean;
  superseded: boolean;
  neverVerified: boolean; // verifiable && never verified
  criteriaMet: number; // tasks
  criteriaTotal: number; // tasks
}

function haystack(parts: string[]): string {
  return parts
    .filter((p) => p !== "")
    .join("\n")
    .toLowerCase();
}

function noteDocRow(kind: "note" | "doc", s: NoteSnapshot | DocSnapshot): Row {
  const when = kind === "doc" ? (s as DocSnapshot).when : "";
  const verifiedAt = s.verified_at;
  return {
    kind,
    id: s.id,
    title: s.title,
    titleLower: s.title.toLowerCase(),
    bodyLower: haystack([s.body, when, ...s.tags, ...s.anchors.map((a) => a.value), s.id]),
    status: "",
    priority: null,
    assignee: "",
    branch: "",
    sprint: "",
    project: "",
    tags: s.tags,
    updated: s.updated_at,
    verifiedAt,
    verifiable: true,
    stale: s.stale_at > 0,
    superseded: s.superseded_by.length > 0,
    neverVerified: verifiedAt === 0,
    criteriaMet: 0,
    criteriaTotal: 0,
  };
}

function logRow(s: LogSnapshot): Row {
  return {
    kind: "log",
    id: s.id,
    title: s.title,
    titleLower: s.title.toLowerCase(),
    bodyLower: haystack([...s.entries.map((e) => e.text), ...s.tags, s.id]),
    status: "",
    priority: null,
    assignee: "",
    branch: "",
    sprint: "",
    project: "",
    tags: s.tags,
    updated: s.updated_at,
    verifiedAt: 0,
    verifiable: false,
    stale: false,
    superseded: false,
    neverVerified: false,
    criteriaMet: 0,
    criteriaTotal: 0,
  };
}

function taskRow(s: TaskSnapshot): Row {
  return {
    kind: "task",
    id: s.id,
    title: s.title,
    titleLower: s.title.toLowerCase(),
    bodyLower: haystack([
      s.description,
      ...s.labels,
      ...s.criteria.map((c) => c.text),
      ...s.comments.map((c) => c.body),
      s.assignee,
      s.branch,
      s.id,
    ]),
    status: s.status,
    priority: s.priority,
    assignee: s.assignee,
    branch: s.branch,
    sprint: s.sprint,
    project: s.project,
    tags: s.labels,
    updated: s.updated_at,
    verifiedAt: 0,
    verifiable: false,
    stale: false,
    superseded: false,
    neverVerified: false,
    criteriaMet: s.criteria.filter((c) => c.status === "met").length,
    criteriaTotal: s.criteria.length,
  };
}

function sprintRow(s: SprintSnapshot): Row {
  return {
    kind: "sprint",
    id: s.id,
    title: s.title,
    titleLower: s.title.toLowerCase(),
    bodyLower: haystack([s.description, ...s.labels, ...s.comments.map((c) => c.body), s.id]),
    status: s.status,
    priority: null,
    assignee: "",
    branch: "",
    sprint: "",
    project: s.project,
    tags: s.labels,
    updated: s.updated_at,
    verifiedAt: 0,
    verifiable: false,
    stale: false,
    superseded: false,
    neverVerified: false,
    criteriaMet: 0,
    criteriaTotal: 0,
  };
}

function projectRow(s: ProjectSnapshot): Row {
  return {
    kind: "project",
    id: s.id,
    title: s.title,
    titleLower: s.title.toLowerCase(),
    bodyLower: haystack([s.description, ...s.labels, ...s.comments.map((c) => c.body), s.id]),
    status: s.status,
    priority: null,
    assignee: "",
    branch: "",
    sprint: "",
    project: "",
    tags: s.labels,
    updated: s.updated_at,
    verifiedAt: 0,
    verifiable: false,
    stale: false,
    superseded: false,
    neverVerified: false,
    criteriaMet: 0,
    criteriaTotal: 0,
  };
}

// buildIndex folds a StateResponse into the flat Row index, in kind order.
export function buildIndex(state: StateResponse): Row[] {
  return [
    ...state.tasks.map(taskRow),
    ...state.notes.map((n) => noteDocRow("note", n)),
    ...state.docs.map((d) => noteDocRow("doc", d)),
    ...state.logs.map(logRow),
    ...state.sprints.map(sprintRow),
    ...state.projects.map(projectRow),
  ];
}

// titleMap indexes rows by id for resolving a sprint/project reference (or a
// deep-linked selection) to its human title.
export function titleMap(rows: readonly Row[]): Map<string, string> {
  return new Map(rows.map((r) => [r.id, r.title]));
}

const STATUS_RANK: Record<string, number> = {
  open: 0,
  planned: 0,
  in_progress: 1,
  active: 1,
  done: 3,
  completed: 3,
  archived: 3,
  cancelled: 4,
};

// statusRank orders a status along the lifecycle (actionable first) for the
// table's default sort. Status-less kinds land in the middle band.
export function statusRank(status: string): number {
  return STATUS_RANK[status] ?? 2;
}

// compareDefault is the table's default order: status group, then priority
// (P0 first), then most-recently-updated. It keeps actionable tasks at the top.
export function compareDefault(a: Row, b: Row): number {
  return (
    statusRank(a.status) - statusRank(b.status) ||
    (a.priority ?? 99) - (b.priority ?? 99) ||
    b.updated - a.updated ||
    (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
  );
}

// priorityLabel renders a task priority as P0..P3.
export function priorityLabel(p: number): string {
  return `P${p}`;
}
