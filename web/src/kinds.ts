// Single source of truth for the seven entity kinds: wire order, display
// order, marker kinds, and the shared kind-badge CSS class.

export type EntityKind = "note" | "doc" | "log" | "task" | "sprint" | "project" | "runbook";

// WIRE_KINDS is the codec order entity kinds arrive in on the wire.
export const WIRE_KINDS: readonly EntityKind[] = [
  "note",
  "doc",
  "log",
  "task",
  "sprint",
  "project",
  "runbook",
];

// DISPLAY_KINDS is the fixed order the kind facet, badges, and legend use.
export const DISPLAY_KINDS: readonly EntityKind[] = [
  "task",
  "note",
  "doc",
  "log",
  "runbook",
  "sprint",
  "project",
];

// MARKER_KINDS is the kinds that render as point markers (tasks are spans,
// sprint/project are bands).
export const MARKER_KINDS: ReadonlySet<string> = new Set([
  "note",
  "doc",
  "log",
  "sprint",
  "runbook",
]);

// isEntityKind narrows a raw string (e.g. a decoded route fragment) to EntityKind.
export function isEntityKind(kind: string): kind is EntityKind {
  return (WIRE_KINDS as readonly string[]).includes(kind);
}

// kindBadgeClass returns the CSS classes for a kind badge or chip.
export function kindBadgeClass(kind: EntityKind): string {
  return `kind-badge kind-${kind}`;
}
