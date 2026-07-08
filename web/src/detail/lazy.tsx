// Code-split entry point for the detail panel. The panel's render subtree pulls
// in react-markdown, highlight.js, and the syntax grammars; deferring it behind
// React.lazy keeps that weight out of the entry chunk until an entity is first
// selected. Every call site already gates on a non-null selection, so the import
// fires exactly when the panel is needed.

import { lazy, Suspense } from "react";
import type { Selection } from "../store";
import { readStoredWidth } from "./panelWidth";

const Panel = lazy(() => import("./Panel").then((m) => ({ default: m.Panel })));

// PanelFallback reserves the detail column's width while the lazy chunk loads so
// the layout doesn't flash-collapse when an entity is first selected.
function PanelFallback() {
  const width = readStoredWidth();
  return (
    <aside
      className="detail"
      style={{ width, flex: `0 0 ${width}px` }}
      aria-label="Entity detail"
      aria-busy="true"
    />
  );
}

// PanelLazy is a drop-in for Panel that loads it on demand behind a
// width-reserving Suspense fallback.
export function PanelLazy(props: { selection: Selection; onClose: () => void }) {
  return (
    <Suspense fallback={<PanelFallback />}>
      <Panel {...props} />
    </Suspense>
  );
}
