// Interactive legend for the swimlane: entity-kind and event-type toggles plus a
// static task-status colour key. Clicking a kind or event chip filters that
// family out of the timeline (applied before layout, so packing tightens);
// filtered-out chips render dimmed and stay clickable to re-enable. Identity is
// never colour-alone — every chip pairs a mark or badge with its label.

import { Glyph } from "./Glyph";
import { DISPLAY_KINDS, kindBadgeClass } from "../kinds";
import type { TimelineFilters } from "./filters";
import type { LayoutResult } from "./layout";
import { EVENT_SPECS, eventSpec, statusSpec } from "./marks";

const STATUS_ORDER = ["in_progress", "done", "cancelled", "open"];

interface Props {
  result: LayoutResult;
  presentKinds: ReadonlySet<string>;
  presentTypes: ReadonlySet<string>;
  filters: TimelineFilters;
  onToggleKind: (kind: string) => void;
  onToggleType: (type: string) => void;
}

export function Legend({
  result,
  presentKinds,
  presentTypes,
  filters,
  onToggleKind,
  onToggleType,
}: Props) {
  const kinds = DISPLAY_KINDS.filter((k) => presentKinds.has(k));
  const types = Object.keys(EVENT_SPECS).filter((t) => presentTypes.has(t));

  const statuses = new Set<string>();
  for (const lane of result.lanes) for (const s of lane.spans) statuses.add(s.status);
  const statusKeys = STATUS_ORDER.filter((s) => statuses.has(s));

  if (kinds.length === 0 && types.length === 0) return null;

  return (
    <div className="tl-legend" aria-label="Timeline filters">
      {kinds.length > 0 && (
        <div className="tl-legend-group" role="group" aria-label="Filter by kind">
          {kinds.map((k) => {
            const off = filters.hiddenKinds.has(k);
            return (
              <button
                type="button"
                key={`kind-${k}`}
                className={off ? "tl-legend-chip tl-legend-off" : "tl-legend-chip"}
                aria-pressed={!off}
                onClick={() => onToggleKind(k)}
              >
                <span className={kindBadgeClass(k)}>{k}</span>
              </button>
            );
          })}
        </div>
      )}
      {types.length > 0 && (
        <div className="tl-legend-group" role="group" aria-label="Filter by event">
          {types.map((t) => {
            const spec = eventSpec(t);
            const off = filters.hiddenTypes.has(t);
            return (
              <button
                type="button"
                key={`event-${t}`}
                className={off ? "tl-legend-chip tl-legend-off" : "tl-legend-chip"}
                aria-pressed={!off}
                onClick={() => onToggleType(t)}
              >
                <svg width={16} height={16} aria-hidden="true">
                  <g transform="translate(8,8)">
                    <Glyph spec={spec} size={10} />
                  </g>
                </svg>
                {spec.label}
              </button>
            );
          })}
        </div>
      )}
      {statusKeys.length > 0 && (
        <div className="tl-legend-group tl-legend-static" aria-label="Task status colours">
          {statusKeys.map((s) => {
            const spec = statusSpec(s);
            return (
              <span className="tl-legend-key" key={`status-${s}`}>
                <span
                  className="tl-legend-bar"
                  style={{ background: spec.color }}
                  aria-hidden="true"
                />
                {spec.label}
              </span>
            );
          })}
        </div>
      )}
    </div>
  );
}
