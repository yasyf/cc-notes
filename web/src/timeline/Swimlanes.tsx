// SVG swimlane renderer. React owns every node; d3 supplies only math — the
// scaleTime axis, the x-only zoom transform, and the curveBumpX fork/merge
// connectors. A sticky axis sits above a vertically scrolling lane body; zoom
// and pan act on the x axis alone.

import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { curveBumpX, line } from "d3-shape";
import { Axis } from "./Axis";
import { Glyph } from "./Glyph";
import { Legend } from "./Legend";
import {
  CONNECTOR_RADIUS,
  MARKER_SIZE,
  RAIL_STROKE,
  SPAN_HEIGHT,
  rowMetrics,
} from "./geometry";
import type { Band, Connector, LaneRow, LayoutResult, MarkerItem, SpanItem } from "./layout";
import { LANE_STATUS, eventSpec, statusSpec } from "./marks";
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

export function Swimlanes({ result, selection, onSelect }: Props) {
  const [bodyRef, measured] = useMeasure<HTMLDivElement>();
  const chartWidth = Math.max(measured, 1);
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
        const px = e.clientX - rect.left;
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

  return (
    <div className="tl-root">
      <div className="tl-toolbar">
        <Legend result={result} />
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
        <Axis ticks={tickList} width={chartWidth} />
      </div>
      <div className="tl-body" ref={bodyRef}>
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
          <g className="tl-spans">
            {result.lanes.flatMap((lane) =>
              lane.spans.map((s) => (
                <SpanBar
                  key={`span-${lane.name}-${s.ref.id}`}
                  span={s}
                  lane={lane}
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
          <g className="tl-labels">
            {result.lanes.map((lane) => (
              <LaneLabel key={`label-${lane.name}`} lane={lane} x={Math.max(2, sx(lane.start))} y={metrics.labelY(lane.row)} />
            ))}
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

function SpanBar({
  span,
  lane,
  sx,
  y,
  selected,
  onSelect,
  onTip,
  onLeave,
}: {
  span: SpanItem;
  lane: LaneRow;
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
    { label: "started", value: formatTime(span.start) },
    { label: span.open ? "now" : "closed", value: formatTime(span.end) },
    { label: "lane", value: lane.name },
  ];
  if (span.orphanBranch !== undefined) {
    lines.push({ label: "branch", value: `${span.orphanBranch} (unknown)` });
  }
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
  cx,
  cy,
  selected,
  onSelect,
  onTip,
  onLeave,
}: {
  marker: MarkerItem;
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
    { label: "time", value: formatTime(marker.time) },
  ];
  if (marker.orphanBranch !== undefined) {
    lines.push({ label: "branch", value: `${marker.orphanBranch} (unknown)` });
  }
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

function LaneLabel({ lane, x, y }: { lane: LaneRow; x: number; y: number }) {
  const chip = LANE_STATUS[lane.status] ?? lane.status;
  const chipW = chip.length * 5.4 + 10;
  return (
    <g transform={`translate(${x},${y})`}>
      <rect x={0} y={-9} width={chipW} height={12} rx={3} className={`tl-chip tl-chip-${lane.status}`} />
      <text x={4} y={0} className="tl-chip-text">
        {chip}
      </text>
      <text x={chipW + 6} y={0} className="tl-lane-name">
        {lane.name}
      </text>
    </g>
  );
}

function railClass(lane: LaneRow): string {
  const parts = ["tl-rail", `tl-rail-${lane.status}`];
  if (lane.inferred) parts.push("tl-rail-inferred");
  return parts.join(" ");
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

function formatTime(sec: number): string {
  return new Date(sec * 1000).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
