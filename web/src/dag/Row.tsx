// One commit row: the SVG graph cell (continuous column rails + curved
// fork/merge transitions + the commit dot) followed by sha, summary, author,
// relative time, and the branch / task / event badges. Rails and edges take a
// per-column decoration colour; the commit dot wears a surface ring so it stays
// legible where a rail passes behind it (dataviz mark spec). Badges are
// keyboard-focusable and open the shared detail panel via the store selection.

import { useEffect, useRef, type KeyboardEvent, type MouseEvent } from "react";
import { curveBumpY, line } from "d3-shape";
import { Glyph } from "../timeline/Glyph";
import { eventSpec } from "../timeline/marks";
import type { CommitPage, Event } from "../api";
import type { Selection } from "../store";
import type { ColumnEdge, CommitLayout } from "./columns";
import { columnColor, relativeTime, shortSha, shortTask } from "./badges";

export const ROW_H = 34;
const COL_W = 16;
const PAD = 9;
const DOT_R = 4.5;
const CENTER = ROW_H / 2;

const link = line<[number, number]>()
  .x((d) => d[0])
  .y((d) => d[1])
  .curve(curveBumpY);

// gutterWidth is the SVG graph-cell width for a graph totalColumns wide.
export function gutterWidth(totalColumns: number): number {
  return PAD * 2 + Math.max(totalColumns, 1) * COL_W;
}

function colX(column: number): number {
  return PAD + column * COL_W + COL_W / 2;
}

interface Props {
  commit: CommitPage;
  node: CommitLayout;
  totalColumns: number;
  now: number;
  active: boolean;
  flash: boolean; // scroll into view and pulse a highlight ring
  hideGraph: boolean; // drop the column gutter (a filtered list has no DAG)
  selectedId: string | null;
  onSelectRow: (sha: string) => void;
  onSelectEntity: (sel: Selection) => void;
}

export function Row({
  commit,
  node,
  totalColumns,
  now,
  active,
  flash,
  hideGraph,
  selectedId,
  onSelectRow,
  onSelectEntity,
}: Props) {
  const rowRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (flash) rowRef.current?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [flash]);
  const cls = ["dag-row", active ? "dag-row-active" : "", flash ? "dag-row-flash" : ""]
    .filter(Boolean)
    .join(" ");
  return (
    <div
      ref={rowRef}
      className={cls}
      role="row"
      tabIndex={0}
      aria-label={`commit ${shortSha(commit.sha)}: ${commit.summary}`}
      onClick={() => onSelectRow(commit.sha)}
      onKeyDown={(e) => activate(e, () => onSelectRow(commit.sha))}
    >
      {!hideGraph && <GraphCell node={node} totalColumns={totalColumns} />}
      <code className="dag-sha">{shortSha(commit.sha)}</code>
      <span className="dag-summary" title={commit.summary}>
        {commit.summary}
      </span>
      <span className="dag-author" title={commit.author}>
        {commit.author}
      </span>
      <span className="dag-time" title={new Date(commit.time * 1000).toLocaleString()}>
        {relativeTime(commit.time, now)}
      </span>
      <span className="dag-badges">
        {commit.branch !== null && (
          <span className="dag-chip dag-chip-branch" title={`branch ${commit.branch}`}>
            {commit.branch}
          </span>
        )}
        {commit.tasks.map((id) => (
          <button
            key={`task-${id}`}
            type="button"
            className={
              selectedId === id ? "dag-chip dag-chip-task dag-chip-on" : "dag-chip dag-chip-task"
            }
            aria-label={`task ${id}`}
            title={`task ${id}`}
            onClick={(e) => openEntity(e, { kind: "task", id, title: id }, onSelectEntity, onSelectRow, commit.sha)}
          >
            {shortTask(id)}
          </button>
        ))}
        {commit.events.map((ev, i) => (
          <EventBadge
            key={`ev-${ev.entity.id}-${ev.type}-${i}`}
            event={ev}
            selected={selectedId === ev.entity.id}
            onSelect={(sel) => {
              onSelectRow(commit.sha);
              onSelectEntity(sel);
            }}
          />
        ))}
      </span>
    </div>
  );
}

function EventBadge({
  event,
  selected,
  onSelect,
}: {
  event: Event;
  selected: boolean;
  onSelect: (sel: Selection) => void;
}) {
  const spec = eventSpec(event.type);
  const sel: Selection = {
    kind: event.entity.kind,
    id: event.entity.id,
    title: event.entity.title,
  };
  return (
    <button
      type="button"
      className={selected ? "dag-event dag-event-on" : "dag-event"}
      aria-label={`${spec.label}: ${event.entity.title}`}
      title={`${spec.label}: ${event.entity.title}`}
      onClick={(e) => {
        e.stopPropagation();
        onSelect(sel);
      }}
    >
      <svg width={14} height={14} aria-hidden="true">
        <g transform="translate(7,7)">
          <Glyph spec={spec} size={9} />
        </g>
      </svg>
    </button>
  );
}

function GraphCell({ node, totalColumns }: { node: CommitLayout; totalColumns: number }) {
  const width = gutterWidth(totalColumns);
  const tops = topColumns(node);
  return (
    <svg
      className="dag-graph"
      width={width}
      height={ROW_H}
      style={{ width, minWidth: width }}
      aria-hidden="true"
    >
      {tops.map((c) => (
        <line
          key={`top-${c}`}
          className="dag-rail"
          x1={colX(c)}
          x2={colX(c)}
          y1={0}
          y2={CENTER}
          stroke={columnColor(c)}
        />
      ))}
      {node.edges.map((e, i) => (
        <EdgePath key={`edge-${i}`} edge={e} />
      ))}
      <circle
        className="dag-dot"
        cx={colX(node.column)}
        cy={CENTER}
        r={DOT_R}
        fill={columnColor(node.column)}
      />
    </svg>
  );
}

function EdgePath({ edge }: { edge: ColumnEdge }) {
  const x0 = colX(edge.fromColumn);
  const x1 = colX(edge.toColumn);
  const color = edge.kind === "fork" || edge.kind === "merge" ? columnColor(Math.max(edge.fromColumn, edge.toColumn)) : columnColor(edge.fromColumn);
  if (edge.kind === "open") {
    return (
      <line
        className="dag-rail dag-rail-open"
        x1={x0}
        x2={x0}
        y1={CENTER}
        y2={ROW_H}
        stroke={color}
      />
    );
  }
  if (edge.fromColumn === edge.toColumn) {
    return (
      <line className="dag-rail" x1={x0} x2={x0} y1={CENTER} y2={ROW_H} stroke={color} />
    );
  }
  const d = link([
    [x0, CENTER],
    [x1, ROW_H],
  ]) ?? "";
  return <path className="dag-rail" d={d} stroke={color} fill="none" />;
}

// topColumns is every column with a rail entering this row from above: the
// source column of each descending edge plus the commit's own column. Rendered
// as the upper half of the rail so it meets the previous row's lower half.
function topColumns(node: CommitLayout): number[] {
  const set = new Set<number>([node.column]);
  for (const e of node.edges) set.add(e.fromColumn);
  return [...set].sort((a, b) => a - b);
}

function openEntity(
  e: MouseEvent,
  sel: Selection,
  onSelectEntity: (sel: Selection) => void,
  onSelectRow: (sha: string) => void,
  sha: string,
) {
  e.stopPropagation();
  onSelectRow(sha);
  onSelectEntity(sel);
}

function activate(e: KeyboardEvent, fn: () => void) {
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    fn();
  }
}
