// Presentational atoms for the Browse views and header search: a kind badge, a
// priority badge, and a title highlighter that marks a scorer's match spans.

import type { ReactNode } from "react";
import { priorityLabel, type EntityKind } from "./index";
import type { Span } from "./search";

export function KindBadge({ kind }: { kind: EntityKind }) {
  return <span className={`kind-badge kind-${kind}`}>{kind}</span>;
}

export function PriorityBadge({ priority }: { priority: number }) {
  return <span className={`prio prio-${priority}`}>{priorityLabel(priority)}</span>;
}

// Highlight renders text with <mark> around each span. Spans are [start, end)
// ranges into text, in order; a match elsewhere passes an empty spans array and
// the text renders plain.
export function Highlight({ text, spans }: { text: string; spans: Span[] }) {
  if (spans.length === 0) return <>{text}</>;
  const out: ReactNode[] = [];
  let cursor = 0;
  spans.forEach(([start, end], i) => {
    if (start > cursor) out.push(text.slice(cursor, start));
    out.push(
      <mark key={i} className="hl">
        {text.slice(start, end)}
      </mark>,
    );
    cursor = end;
  });
  if (cursor < text.length) out.push(text.slice(cursor));
  return <>{out}</>;
}
