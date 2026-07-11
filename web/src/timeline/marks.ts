// Mark vocabulary for the swimlane: which glyph and colour role each event type
// and task status wears. Following the dataviz method, identity rides SHAPE
// first (every event type is a distinct glyph) with a restrained set of semantic
// colour families plus the reserved status palette — colour never carries
// meaning alone, and the legend + tooltip always back it. Colours resolve to CSS
// custom properties themed for light/dark in theme.css.

export type GlyphShape =
  | "circle"
  | "ring"
  | "square"
  | "diamond"
  | "triangle-up"
  | "triangle-down"
  | "chevron"
  | "tick"
  | "check"
  | "slash"
  | "dot";

export interface MarkSpec {
  shape: GlyphShape;
  color: string; // CSS var reference
  label: string;
  hollow: boolean; // stroke-only glyph
}

// Semantic colour roles (themed CSS custom properties).
const S1 = "var(--viz-s1)"; // blue — creation
const S2 = "var(--viz-s2)"; // aqua — content append
const S3 = "var(--viz-s3)"; // yellow — status change
const S4 = "var(--viz-s4)"; // magenta — runbook run
const S5 = "var(--viz-s5)"; // violet — branch move
const S6 = "var(--viz-s6)"; // orange — commit link
const GOOD = "var(--viz-good)";
const WARN = "var(--viz-warning)";
const SERIOUS = "var(--viz-serious)";
const MUTED = "var(--viz-muted-mark)";

// EVENT_SPECS maps every classified event type to its glyph. Task-lifecycle
// events (claimed/closed/…) surface on spans rather than as markers, but a spec
// exists for each so any type renders consistently and the legend can name it.
export const EVENT_SPECS: Record<string, MarkSpec> = {
  created: { shape: "circle", color: S1, label: "Created", hollow: false },
  claimed: { shape: "triangle-up", color: S2, label: "Claimed", hollow: false },
  reclaimed: { shape: "triangle-down", color: S2, label: "Reclaimed", hollow: false },
  status: { shape: "diamond", color: S3, label: "Status change", hollow: false },
  closed: { shape: "square", color: MUTED, label: "Closed", hollow: false },
  branch_moved: { shape: "chevron", color: S5, label: "Branch moved", hollow: false },
  commit_linked: { shape: "tick", color: S6, label: "Commit linked", hollow: false },
  verified: { shape: "check", color: GOOD, label: "Verified", hollow: false },
  stale: { shape: "diamond", color: WARN, label: "Stale", hollow: true },
  superseded: { shape: "slash", color: SERIOUS, label: "Superseded", hollow: false },
  entry: { shape: "square", color: S2, label: "Log entry", hollow: false },
  run_started: { shape: "triangle-up", color: S4, label: "Run started", hollow: false },
  run_finished: { shape: "square", color: S4, label: "Run finished", hollow: false },
  comment: { shape: "dot", color: MUTED, label: "Comment", hollow: false },
  edited: { shape: "ring", color: MUTED, label: "Edited", hollow: true },
};

const FALLBACK_SPEC: MarkSpec = {
  shape: "circle",
  color: MUTED,
  label: "Event",
  hollow: true,
};

// eventSpec returns the mark spec for an event type, falling back to a neutral
// ring for any unclassified type.
export function eventSpec(type: string): MarkSpec {
  return EVENT_SPECS[type] ?? { ...FALLBACK_SPEC, label: type };
}

export interface StatusSpec {
  color: string;
  label: string;
}

// TASK_STATUS colours a task span by its final status. Done wears the reserved
// good token; cancelled recedes to muted; active work stays accent blue.
export const TASK_STATUS: Record<string, StatusSpec> = {
  done: { color: GOOD, label: "Done" },
  cancelled: { color: MUTED, label: "Cancelled" },
  in_progress: { color: S1, label: "In progress" },
  open: { color: S1, label: "Open" },
};

export function statusSpec(status: string): StatusSpec {
  return TASK_STATUS[status] ?? { color: S1, label: status || "In progress" };
}

// MERGE_MARK_DEFAULT is the plain merge diamond, also the fallback for any
// unclassified merge kind.
const MERGE_MARK_DEFAULT: MarkSpec = { shape: "diamond", color: S5, label: "Merge", hollow: false };

// MERGE_MARKS glyphs a lane's merge point, keyed by merge.kind so a real merge,
// a fast-forward, and an inferred (task-rumor) merge each read as a distinct
// shape rather than colour alone.
export const MERGE_MARKS: Record<string, MarkSpec> = {
  merge: MERGE_MARK_DEFAULT,
  "fast-forward": { shape: "chevron", color: S2, label: "Fast-forward merge", hollow: false },
  inferred: { shape: "ring", color: MUTED, label: "Inferred merge", hollow: true },
};

// mergeMark returns the glyph for a merge kind, defaulting to the plain merge
// diamond for any unclassified kind.
export function mergeMark(kind: string): MarkSpec {
  return MERGE_MARKS[kind] ?? MERGE_MARK_DEFAULT;
}

export type GlyphGeom =
  | { kind: "circle"; r: number }
  | { kind: "rect"; x: number; y: number; w: number; h: number; rx: number }
  | { kind: "polygon"; points: string }
  | { kind: "path"; d: string };

// glyphGeom returns the centred geometry for a glyph at the given pixel size,
// drawn around the origin so the caller positions it with a translate.
export function glyphGeom(shape: GlyphShape, size: number): GlyphGeom {
  const h = size / 2;
  switch (shape) {
    case "circle":
    case "ring":
      return { kind: "circle", r: h };
    case "dot":
      return { kind: "circle", r: size / 3 };
    case "square":
      return { kind: "rect", x: -h, y: -h, w: size, h: size, rx: 1.5 };
    case "diamond":
      return { kind: "polygon", points: `0,${-h * 1.2} ${h * 1.2},0 0,${h * 1.2} ${-h * 1.2},0` };
    case "triangle-up":
      return { kind: "polygon", points: `0,${-h * 1.15} ${h},${h * 0.9} ${-h},${h * 0.9}` };
    case "triangle-down":
      return { kind: "polygon", points: `0,${h * 1.15} ${h},${-h * 0.9} ${-h},${-h * 0.9}` };
    case "chevron":
      return { kind: "path", d: `M ${-h},${-h * 0.6} L 0,${h * 0.4} L ${h},${-h * 0.6}` };
    case "tick":
      return { kind: "rect", x: -1, y: -h * 1.2, w: 2, h: size * 1.2, rx: 1 };
    case "check":
      return { kind: "path", d: `M ${-h},0 L ${-h * 0.2},${h * 0.7} L ${h},${-h * 0.8}` };
    case "slash":
      return { kind: "path", d: `M ${-h},${h} L ${h},${-h}` };
  }
}

// STROKE_SHAPES render as strokes rather than fills regardless of the hollow
// flag — a check or slash has no interior to fill.
export const STROKE_SHAPES = new Set<GlyphShape>(["check", "slash", "chevron"]);
