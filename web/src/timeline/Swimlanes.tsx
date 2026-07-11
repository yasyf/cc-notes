// SVG swimlane renderer. React owns every node; d3 supplies only math — the
// scaleTime axis, the x-only zoom transform, and the curveBumpX fork/merge
// connectors. A sticky axis sits above a vertically scrolling lane body; zoom
// and pan act on the x axis alone. The toolbar hosts the interactive legend and
// lane-focus filter; lane labels are chips with a collapse toggle and a
// click-to-focus filter.

import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { curveBumpX, line } from "d3-shape";
import { relativeTime } from "../dag/badges";
import { formatDateTime } from "../detail/format";
import { Axis } from "./Axis";
import { Glyph } from "./Glyph";
import { Legend } from "./Legend";
import type { TimelineFilters } from "./filters";
import {
  CONNECTOR_RADIUS,
  GUTTER_WIDTH,
  MARKER_SIZE,
  RAIL_STROKE,
  SPAN_HEIGHT,
  rowMetrics,
} from "./geometry";
import type { Band, Connector, LaneRow, LayoutResult, MarkerItem, SpanItem } from "./layout";
import { eventSpec, mergeMark, statusSpec } from "./marks";
import {
  IDENTITY_ZOOM,
  baseScale,
  clampZoom,
  displayScale,
  panBy,
  ticks as makeTicks,
  zoomAt,
  type ZoomLimits,
  type ZoomState,
} from "./scale";
import { useMeasure } from "./useMeasure";
import type { Selection } from "../store";

interface Props {
  result: LayoutResult;
  selection: Selection | null;
  onSelect: (sel: Selection) => void;
  filters: TimelineFilters;
  presentKinds: ReadonlySet<string>;
  presentTypes: ReadonlySet<string>;
  onToggleKind: (kind: string) => void;
  onToggleType: (type: string) => void;
  onToggleLane: (lane: string) => void;
  onToggleCollapse: (lane: string) => void;
}

interface TipLine {
  label: string;
  value: string;
}

interface Tooltip {
  x: number;
  y: number;
  title: string;
  lines: TipLine[];
}

const curve = line<[number, number]>()
  .x((d) => d[0])
  .y((d) => d[1])
  .curve(curveBumpX);

export function Swimlanes({
  result,
  selection,
  onSelect,
  filters,
  presentKinds,
  presentTypes,
  onToggleKind,
  onToggleType,
  onToggleLane,
  onToggleCollapse,
}: Props) {
  const [bodyRef, measured] = useMeasure<HTMLDivElement>();
  // The lane-label gutter occupies a fixed left column of the scrolling body, so
  // the chart's scale math, ticks, and svg width all run against the remaining
  // width and start at the gutter's right edge.
  const chartWidth = Math.max(measured - GUTTER_WIDTH, 1);
  const [zoom, setZoom] = useState<ZoomState>(IDENTITY_ZOOM);
  const [tip, setTip] = useState<Tooltip | null>(null);

  const limits: ZoomLimits = { minK: 1, maxK: 200, width: chartWidth };
  const view = clampZoom(zoom, limits);

  const domainKey = `${result.domain[0]}:${result.domain[1]}`;
  useEffect(() => {
    setZoom(IDENTITY_ZOOM);
  }, [domainKey]);

  const base = useMemo(
    () => baseScale(result.domain, chartWidth),
    [result.domain, chartWidth],
  );
  const scale = useMemo(
    () => displayScale(base, view.k, view.x),
    [base, view.k, view.x],
  );
  const metrics = useMemo(() => rowMetrics(result), [result]);
  const laneRows = useMemo(() => groupLaneRows(result.lanes), [result.lanes]);
  const tickCount = Math.max(2, Math.round(chartWidth / 120));
  const tickList = makeTicks(scale, tickCount);
  const sx = (t: number) => scale(new Date(t * 1000));

  const zoomRef = useRef(view);
  zoomRef.current = view;
  const limitsRef = useRef(limits);
  limitsRef.current = limits;

  useEffect(() => {
    const el = bodyRef.current;
    if (el === null) return;
    const onWheel = (e: WheelEvent) => {
      const rect = el.getBoundingClientRect();
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        const px = e.clientX - rect.left - GUTTER_WIDTH;
        setZoom(
          zoomAt(zoomRef.current, px, Math.exp(-e.deltaY * 0.0015), limitsRef.current),
        );
      } else if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) {
        e.preventDefault();
        setZoom(panBy(zoomRef.current, -e.deltaX, limitsRef.current));
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [bodyRef]);

  const onPointerDown = (e: ReactPointerEvent<SVGSVGElement>) => {
    if (e.button !== 0) return;
    let last = e.clientX;
    const move = (ev: PointerEvent) => {
      const dx = ev.clientX - last;
      last = ev.clientX;
      setZoom(panBy(zoomRef.current, dx, limitsRef.current));
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  const zoomStep = (factor: number) =>
    setZoom(zoomAt(zoomRef.current, chartWidth / 2, factor, limitsRef.current));

  const showTip = (e: ReactPointerEvent, title: string, lines: TipLine[]) => {
    const host = bodyRef.current;
    if (host === null) return;
    const rect = host.getBoundingClientRect();
    setTip({
      x: e.clientX - rect.left + host.scrollLeft,
      y: e.clientY - rect.top + host.scrollTop,
      title,
      lines,
    });
  };

  const selected = (id: string) => selection !== null && selection.id === id;
  const now = result.now;

  return (
    <div className="tl-root">
      <div className="tl-toolbar">
        <Legend
          result={result}
          presentKinds={presentKinds}
          presentTypes={presentTypes}
          filters={filters}
          onToggleKind={onToggleKind}
          onToggleType={onToggleType}
        />
        {filters.lane !== null && (
          <button
            type="button"
            className="tl-lane-focus"
            title="Clear lane focus"
            onClick={() => onToggleLane(filters.lane as string)}
          >
            focus: <span className="tl-lane-focus-name">{filters.lane}</span> ✕
          </button>
        )}
        <div className="tl-zoom" role="group" aria-label="Zoom">
          <button type="button" aria-label="Zoom out" onClick={() => zoomStep(1 / 1.4)}>
            −
          </button>
          <button type="button" aria-label="Reset zoom" onClick={() => setZoom(IDENTITY_ZOOM)}>
            Reset
          </button>
          <button type="button" aria-label="Zoom in" onClick={() => zoomStep(1.4)}>
            +
          </button>
        </div>
      </div>
      <div className="tl-axis-wrap">
        <div className="tl-axis-corner" style={{ width: GUTTER_WIDTH }} />
        <Axis ticks={tickList} width={chartWidth} />
      </div>
      <div className="tl-body" ref={bodyRef}>
        <div className="tl-gutter" style={{ width: GUTTER_WIDTH, height: metrics.totalHeight }}>
          {laneRows.map((r) => (
            <div key={`lane-row-${r.row}`} className="tl-lane-row" style={{ top: metrics.labelY(r.row) - 11 }}>
              {r.lanes.map((lane) => (
                <LaneChip
                  key={`label-${lane.name}`}
                  lane={lane}
                  focused={filters.lane === lane.name}
                  onToggleLane={onToggleLane}
                  onToggleCollapse={onToggleCollapse}
                />
              ))}
            </div>
          ))}
        </div>
        <svg
          className="tl-svg"
          width={chartWidth}
          height={metrics.totalHeight}
          onPointerDown={onPointerDown}
          role="img"
          aria-label="Branch and entity timeline"
        >
          <g className="tl-bands">
            {result.bands.map((b) => (
              <BandRect key={`${b.kind}-${b.ref.id}`} band={b} sx={sx} height={metrics.totalHeight} />
            ))}
          </g>
          <g className="tl-grid-layer">
            {tickList.map((t) => (
              <line key={t.value.getTime()} x1={t.x} x2={t.x} y1={0} y2={metrics.totalHeight} className="tl-grid" />
            ))}
          </g>
          <g className="tl-connectors">
            {result.connectors.map((c, i) => (
              <path key={i} d={connectorPath(c, metrics.railY, sx)} className={c.dashed ? "tl-conn tl-conn-dashed" : "tl-conn"} />
            ))}
          </g>
          <g className="tl-rails">
            {result.lanes.map((lane) => (
              <line
                key={`rail-${lane.name}`}
                x1={Math.min(sx(lane.start), chartWidth)}
                x2={sx(lane.end)}
                y1={metrics.railY(lane.row)}
                y2={metrics.railY(lane.row)}
                strokeWidth={RAIL_STROKE}
                className={railClass(lane)}
              />
            ))}
          </g>
          <g className="tl-merges">
            {result.connectors
              .filter((c) => c.kind !== "fork")
              .map((c, i) => (
                <MergeGlyph key={`merge-${i}`} c={c} cx={sx(c.time)} cy={metrics.railY(c.parentRow)} />
              ))}
          </g>
          <g className="tl-spans">
            {result.lanes.flatMap((lane) =>
              lane.spans.map((s) => (
                <SpanBar
                  key={`span-${lane.name}-${s.ref.id}`}
                  span={s}
                  lane={lane}
                  now={now}
                  sx={sx}
                  y={metrics.itemY(lane.row, s.subRow)}
                  selected={selected(s.ref.id)}
                  onSelect={onSelect}
                  onTip={showTip}
                  onLeave={() => setTip(null)}
                />
              )),
            )}
          </g>
          <g className="tl-markers">
            {result.lanes.flatMap((lane) =>
              lane.markers.map((m, i) => (
                <MarkerGlyph
                  key={`mk-${lane.name}-${m.ref.id}-${m.type}-${i}`}
                  marker={m}
                  lane={lane}
                  now={now}
                  cx={sx(m.time)}
                  cy={metrics.itemY(lane.row, m.subRow)}
                  selected={selected(m.ref.id)}
                  onSelect={onSelect}
                  onTip={showTip}
                  onLeave={() => setTip(null)}
                />
              )),
            )}
          </g>
        </svg>
        {tip !== null && (
          <div className="tl-tooltip" style={{ left: tip.x + 12, top: tip.y + 12 }} role="tooltip">
            <div className="tl-tooltip-title">{tip.title}</div>
            {tip.lines.map((l, i) => (
              <div className="tl-tooltip-line" key={i}>
                <span className="tl-tooltip-label">{l.label}</span>
                <span className="tl-tooltip-value">{l.value}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function BandRect({ band, sx, height }: { band: Band; sx: (t: number) => number; height: number }) {
  const x = sx(band.start);
  const w = Math.max(1, sx(band.end) - x);
  return (
    <g className={`tl-band tl-band-${band.kind}`}>
      <rect x={x} y={0} width={w} height={height}>
        <title>{`${band.kind}: ${band.ref.title}`}</title>
      </rect>
      <text x={x + 4} y={12} className="tl-band-label">
        {band.ref.title}
      </text>
    </g>
  );
}

function MergeGlyph({ c, cx, cy }: { c: Connector; cx: number; cy: number }) {
  const spec = mergeMark(c.kind);
  return (
    <g className="tl-merge" transform={`translate(${cx},${cy})`} aria-hidden="true">
      <Glyph spec={spec} size={9} />
      <title>{spec.label}</title>
    </g>
  );
}

function SpanBar({
  span,
  lane,
  now,
  sx,
  y,
  selected,
  onSelect,
  onTip,
  onLeave,
}: {
  span: SpanItem;
  lane: LaneRow;
  now: number;
  sx: (t: number) => number;
  y: number;
  selected: boolean;
  onSelect: (sel: Selection) => void;
  onTip: (e: ReactPointerEvent, title: string, lines: TipLine[]) => void;
  onLeave: () => void;
}) {
  const spec = statusSpec(span.status);
  const x = sx(span.start);
  const w = Math.max(3, sx(span.end) - x);
  const sel: Selection = { kind: span.ref.kind, id: span.ref.id, title: span.ref.title };
  const label = `task ${span.ref.title}, ${spec.label}`;
  const lines: TipLine[] = [
    { label: "status", value: spec.label + (span.open ? " (open)" : "") },
  ];
  if (span.assignee !== undefined) lines.push({ label: "assignee", value: span.assignee });
  lines.push({ label: "branch", value: branchOf(span.orphanBranch, lane) });
  lines.push({ label: "started", value: whenText(span.start, now) });
  lines.push({ label: span.open ? "now" : "closed", value: whenText(span.end, now) });
  lines.push({ label: "duration", value: formatDuration(span.end - span.start) });
  return (
    <rect
      x={x}
      y={y - SPAN_HEIGHT / 2}
      width={w}
      height={SPAN_HEIGHT}
      rx={4}
      className={selected ? "tl-span tl-selected" : "tl-span"}
      style={{ fill: spec.color }}
      role="button"
      tabIndex={0}
      aria-label={label}
      onPointerDown={(e) => e.stopPropagation()}
      onClick={() => onSelect(sel)}
      onKeyDown={(e) => activate(e, () => onSelect(sel))}
      onPointerEnter={(e) => onTip(e, span.ref.title, lines)}
      onPointerLeave={onLeave}
    />
  );
}

function MarkerGlyph({
  marker,
  lane,
  now,
  cx,
  cy,
  selected,
  onSelect,
  onTip,
  onLeave,
}: {
  marker: MarkerItem;
  lane: LaneRow;
  now: number;
  cx: number;
  cy: number;
  selected: boolean;
  onSelect: (sel: Selection) => void;
  onTip: (e: ReactPointerEvent, title: string, lines: TipLine[]) => void;
  onLeave: () => void;
}) {
  const spec = eventSpec(marker.type);
  const sel: Selection = { kind: marker.ref.kind, id: marker.ref.id, title: marker.ref.title };
  const lines: TipLine[] = [
    { label: "event", value: spec.label },
    { label: "kind", value: marker.ref.kind },
    { label: "branch", value: branchOf(marker.orphanBranch, lane) },
    { label: "time", value: whenText(marker.time, now) },
  ];
  return (
    <g
      transform={`translate(${cx},${cy})`}
      className={selected ? "tl-marker tl-selected" : "tl-marker"}
      role="button"
      tabIndex={0}
      aria-label={`${spec.label}: ${marker.ref.title}`}
      onPointerDown={(e) => e.stopPropagation()}
      onClick={() => onSelect(sel)}
      onKeyDown={(e) => activate(e, () => onSelect(sel))}
      onPointerEnter={(e) => onTip(e, marker.ref.title, lines)}
      onPointerLeave={onLeave}
    >
      <Glyph spec={spec} size={MARKER_SIZE} />
      <circle r={9} fill="transparent" className="tl-hit" />
    </g>
  );
}

// groupLaneRows buckets lanes by their packed row so the gutter renders one
// label strip per row. Layout packs non-overlapping sibling lanes onto a shared
// row; each row's chips sit inline, ordered by lane start (then name), so a
// packed row lays its lanes side by side instead of stacking labels on one top.
function groupLaneRows(lanes: LaneRow[]): { row: number; lanes: LaneRow[] }[] {
  const byRow = new Map<number, LaneRow[]>();
  for (const lane of lanes) {
    const arr = byRow.get(lane.row);
    if (arr) arr.push(lane);
    else byRow.set(lane.row, [lane]);
  }
  const rows = [...byRow.entries()].map(([row, ls]) => ({
    row,
    lanes: ls.sort(
      (a, b) => a.start - b.start || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0),
    ),
  }));
  rows.sort((a, b) => a.row - b.row);
  return rows;
}

// LaneChip renders one lane's cell within a gutter row: a collapse chevron and a
// focus chip (status dot, name, commit count, "older than view" tag). Several
// cells share a row when layout packs their lanes together; each cell shrinks so
// the chip name ellipsizes gracefully when a row hosts more than one.
function LaneChip({
  lane,
  focused,
  onToggleLane,
  onToggleCollapse,
}: {
  lane: LaneRow;
  focused: boolean;
  onToggleLane: (lane: string) => void;
  onToggleCollapse: (lane: string) => void;
}) {
  const chipClass = ["tl-lane-chip", focused ? "tl-lane-chip-on" : ""]
    .filter(Boolean)
    .join(" ");
  const title = `${lane.name} — ${lane.laneClass}${lane.commits > 0 ? `, ${lane.commits} commit${lane.commits === 1 ? "" : "s"}` : ""}`;
  return (
    <div className="tl-lane-cell">
      <button
        type="button"
        className="tl-lane-chevron"
        aria-label={lane.collapsed ? `expand lane ${lane.name}` : `collapse lane ${lane.name}`}
        aria-expanded={!lane.collapsed}
        onClick={() => onToggleCollapse(lane.name)}
      >
        <span
          className={lane.collapsed ? "tl-chevron tl-chevron-collapsed" : "tl-chevron"}
          aria-hidden="true"
        />
      </button>
      <button
        type="button"
        className={chipClass}
        title={title}
        aria-label={`focus lane ${lane.name}`}
        aria-pressed={focused}
        onClick={() => onToggleLane(lane.name)}
      >
        <span className={`tl-lane-dot tl-lane-dot-${lane.laneClass}`} aria-hidden="true" />
        <span className="tl-lane-name">{lane.name}</span>
        {lane.commits > 0 && <span className="tl-lane-count">{lane.commits}</span>}
        {lane.autoCollapsed && <span className="tl-lane-tag">older than view</span>}
      </button>
    </div>
  );
}

function branchOf(orphanBranch: string | undefined, lane: LaneRow): string {
  if (orphanBranch !== undefined) return `${orphanBranch} (unknown)`;
  return lane.name;
}

function railClass(lane: LaneRow): string {
  return `tl-rail tl-rail-${lane.laneClass}`;
}

function connectorPath(
  c: Connector,
  railY: (row: number) => number,
  sx: (t: number) => number,
): string {
  const py = railY(c.parentRow);
  const cy = railY(c.childRow);
  const x = sx(c.time);
  const pts: [number, number][] =
    c.kind === "fork"
      ? [
          [x, py],
          [x + CONNECTOR_RADIUS, cy],
        ]
      : [
          [x - CONNECTOR_RADIUS, cy],
          [x, py],
        ];
  return curve(pts) ?? "";
}

function activate(e: KeyboardEvent, fn: () => void) {
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    fn();
  }
}

// whenText renders an absolute local time with a compact relative age, e.g.
// "Jan 3, 2026, 02:15 PM (2d ago)".
function whenText(sec: number, now: number): string {
  const rel = relativeTime(sec, now);
  return `${formatDateTime(sec)} (${rel === "now" ? "just now" : `${rel} ago`})`;
}

// formatDuration renders a span length in the two coarsest non-zero units.
function formatDuration(sec: number): string {
  if (sec <= 0) return "0m";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return h > 0 ? `${d}d ${h}h` : `${d}d`;
  if (h > 0) return m > 0 ? `${h}h ${m}m` : `${h}h`;
  return `${m}m`;
}
