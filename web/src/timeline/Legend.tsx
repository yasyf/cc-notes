// Legend for the swimlane: the event glyphs and task-status swatches actually
// present in the current graph. Identity is never colour-alone — every key pairs
// a mark with its label, and the tooltip/detail panel back it further.

import { Glyph } from "./Glyph";
import type { LayoutResult } from "./layout";
import { EVENT_SPECS, eventSpec, statusSpec } from "./marks";

const STATUS_ORDER = ["in_progress", "done", "cancelled", "open"];

export function Legend({ result }: { result: LayoutResult }) {
  const types = new Set<string>();
  const statuses = new Set<string>();
  for (const lane of result.lanes) {
    for (const m of lane.markers) types.add(m.type);
    for (const s of lane.spans) statuses.add(s.status);
  }
  const eventKeys = Object.keys(EVENT_SPECS).filter((t) => types.has(t));
  const statusKeys = STATUS_ORDER.filter((s) => statuses.has(s));

  if (eventKeys.length === 0 && statusKeys.length === 0) return null;

  return (
    <div className="tl-legend" role="list" aria-label="Timeline legend">
      {statusKeys.map((s) => {
        const spec = statusSpec(s);
        return (
          <span className="tl-legend-key" role="listitem" key={`status-${s}`}>
            <span
              className="tl-legend-bar"
              style={{ background: spec.color }}
              aria-hidden="true"
            />
            {spec.label}
          </span>
        );
      })}
      {eventKeys.map((t) => {
        const spec = eventSpec(t);
        return (
          <span className="tl-legend-key" role="listitem" key={`event-${t}`}>
            <svg width={16} height={16} aria-hidden="true">
              <g transform="translate(8,8)">
                <Glyph spec={spec} size={10} />
              </g>
            </svg>
            {spec.label}
          </span>
        );
      })}
    </div>
  );
}
