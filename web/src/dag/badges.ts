// Presentational helpers for the DAG rows: the per-column decoration palette,
// sha and time formatting, and the short task-id label. Column colour is
// decoration only — identity in the commit graph comes from a rail's position,
// so hues cycle through the dataviz categorical set (themed in theme.css) rather
// than mapping one-to-one to a stable entity.

const COLUMN_COLORS = [
  "var(--viz-dag-1)",
  "var(--viz-dag-2)",
  "var(--viz-dag-3)",
  "var(--viz-dag-4)",
  "var(--viz-dag-5)",
  "var(--viz-dag-6)",
  "var(--viz-dag-7)",
  "var(--viz-dag-8)",
];

// columnColor returns the decoration colour for a column, cycling the categorical
// set so a deep graph stays coloured without inventing new hues.
export function columnColor(column: number): string {
  return COLUMN_COLORS[((column % COLUMN_COLORS.length) + COLUMN_COLORS.length) % COLUMN_COLORS.length];
}

// shortSha abbreviates a commit sha to its first eight hex characters.
export function shortSha(sha: string): string {
  return sha.slice(0, 8);
}

const MINUTE = 60;
const HOUR = 3600;
const DAY = 86400;
const WEEK = 604800;
const YEAR = 31557600;

// relativeTime renders a unix-second timestamp as a compact age relative to now
// (also unix seconds): "now", "5m", "3h", "2d", "6w", "1y".
export function relativeTime(sec: number, now: number): string {
  const d = Math.max(0, now - sec);
  if (d < MINUTE) return "now";
  if (d < HOUR) return `${Math.floor(d / MINUTE)}m`;
  if (d < DAY) return `${Math.floor(d / HOUR)}h`;
  if (d < WEEK) return `${Math.floor(d / DAY)}d`;
  if (d < YEAR) return `${Math.floor(d / WEEK)}w`;
  return `${Math.floor(d / YEAR)}y`;
}
