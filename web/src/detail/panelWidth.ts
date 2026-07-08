// Detail-panel width: seeded from localStorage (clamped) or a 28vw default, and
// shared by the panel itself and the Suspense fallback that reserves its column
// while the lazy chunk loads. Kept free of heavy imports so the fallback needn't
// pull the panel's render subtree into the entry chunk.

const WIDTH_KEY = "cc-notes:detail-width";
export const MIN_WIDTH = 320;
export const MAX_WIDTH = 560;

// clampWidth constrains a pixel width to the resizable panel's bounds.
export function clampWidth(px: number): number {
  return Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, px));
}

// defaultWidth is 28vw, clamped — the width used before the user ever drags.
export function defaultWidth(): number {
  const vw = typeof window === "undefined" ? 1200 : window.innerWidth;
  return clampWidth(Math.round(vw * 0.28));
}

// readStoredWidth returns the persisted width (clamped) or the default.
export function readStoredWidth(): number {
  const stored = window.localStorage.getItem(WIDTH_KEY);
  const n = stored !== null ? Number(stored) : NaN;
  return Number.isFinite(n) ? clampWidth(n) : defaultWidth();
}

// persistWidth stores the current width so it survives a reload.
export function persistWidth(width: number): void {
  window.localStorage.setItem(WIDTH_KEY, String(width));
}
