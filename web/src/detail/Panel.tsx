// Side panel for the selected entity: fetches /api/entity/{kind}/{id} and
// renders a header (kind, title, copyable short-id, status/branch/assignee
// chips), the full current snapshot per kind, and the change trail. Its width is
// drag-resizable from the left edge and persisted to localStorage. Errors are a
// plain message — no retries.

import { useCallback, useEffect, useState, type PointerEvent } from "react";
import {
  fetchEntity,
  type EntityDetail,
  type EntitySummary,
  type TrailChange,
  type TrailEntry,
} from "../api";
import type { Selection } from "../store";
import { AttachmentProvider } from "./Attachments";
import { formatDateTime } from "./format";
import { clampWidth, persistWidth, readStoredWidth } from "./panelWidth";
import { Chip, CopyChip, StatusBadge } from "./parts";
import { SnapshotView } from "./Snapshot";
import { ChangeValue } from "./values";

interface Props {
  selection: Selection;
  onClose: () => void;
}

type Load =
  | { state: "loading" }
  | { state: "error"; message: string }
  | { state: "ready"; detail: EntityDetail };

// useResizableWidth holds the panel width, seeded from localStorage (or the
// clamp(320px, 28vw, 560px) default) and updated by dragging the left-edge
// handle. The width persists back to localStorage whenever it settles.
function useResizableWidth() {
  const [width, setWidth] = useState<number>(readStoredWidth);

  useEffect(() => {
    persistWidth(width);
  }, [width]);

  const onHandleDown = useCallback(
    (e: PointerEvent) => {
      e.preventDefault();
      const startX = e.clientX;
      const startW = width;
      const onMove = (ev: globalThis.PointerEvent) => {
        setWidth(clampWidth(startW + (startX - ev.clientX)));
      };
      const onUp = () => {
        window.removeEventListener("pointermove", onMove);
        window.removeEventListener("pointerup", onUp);
      };
      window.addEventListener("pointermove", onMove);
      window.addEventListener("pointerup", onUp);
    },
    [width],
  );

  return { width, onHandleDown };
}

export function Panel({ selection, onClose }: Props) {
  const [load, setLoad] = useState<Load>({ state: "loading" });
  const { width, onHandleDown } = useResizableWidth();

  useEffect(() => {
    let cancelled = false;
    setLoad({ state: "loading" });
    fetchEntity(selection.kind, selection.id)
      .then((detail) => {
        if (!cancelled) setLoad({ state: "ready", detail });
      })
      .catch((err: unknown) => {
        if (!cancelled) setLoad({ state: "error", message: String(err) });
      });
    return () => {
      cancelled = true;
    };
  }, [selection.kind, selection.id]);

  return (
    <aside
      className="detail"
      style={{ width, flex: `0 0 ${width}px` }}
      aria-label="Entity detail"
    >
      <div
        className="detail-resize"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize panel"
        onPointerDown={onHandleDown}
      />
      <header className="detail-header">
        <div className="detail-heading">
          <span className="detail-kind">{selection.kind}</span>
          <h2 className="detail-title">{selection.title}</h2>
        </div>
        <button type="button" className="detail-close" aria-label="Close detail" onClick={onClose}>
          ×
        </button>
      </header>
      {load.state === "loading" && <p className="detail-msg">Loading…</p>}
      {load.state === "error" && <p className="detail-msg detail-error">{load.message}</p>}
      {load.state === "ready" && (
        <AttachmentProvider>
          <DetailBody detail={load.detail} />
        </AttachmentProvider>
      )}
    </aside>
  );
}

function DetailBody({ detail }: { detail: EntityDetail }) {
  const { summary, snapshot, trail } = detail;
  return (
    <div className="detail-body">
      <HeaderChips summary={summary} />
      <SnapshotView kind={summary.kind} snapshot={snapshot} />
      <section className="snap-section">
        <h4 className="snap-head">Trail</h4>
        <ol className="trail">
          {trail.map((e, i) => (
            <TrailRow key={`${e.sha}-${i}`} entry={e} />
          ))}
        </ol>
      </section>
    </div>
  );
}

function HeaderChips({ summary }: { summary: EntitySummary }) {
  return (
    <div className="detail-chips">
      <CopyChip text={summary.id} label={summary.short} className="chip-id" />
      {summary.status !== undefined && summary.status !== "" && (
        <StatusBadge status={summary.status} />
      )}
      {summary.branch !== undefined && summary.branch !== "" && (
        <Chip className="chip-branch">{summary.branch}</Chip>
      )}
      {summary.assignee !== undefined && summary.assignee !== "" && (
        <Chip className="chip-assignee">{summary.assignee}</Chip>
      )}
    </div>
  );
}

function TrailRow({ entry }: { entry: TrailEntry }) {
  return (
    <li className="trail-row">
      <div className="trail-meta">
        <span className={`trail-kind trail-kind-${entry.kind}`}>{entry.kind}</span>
        <span className="trail-time" title={new Date(entry.time * 1000).toISOString()}>
          {formatDateTime(entry.time)}
        </span>
        {entry.author !== "" && <span className="trail-author">{entry.author}</span>}
        {entry.session !== undefined && entry.session !== "" && (
          <span className="trail-session" title={entry.session}>
            {entry.session.slice(0, 8)}
          </span>
        )}
        {entry.kind === "checkpoint" && entry.covers > 0 && (
          <span className="trail-covers">covers {entry.covers}</span>
        )}
      </div>
      {entry.changes.length > 0 && (
        <ul className="trail-changes">
          {entry.changes.map((c: TrailChange, i) => (
            <li className="trail-change" key={`${c.field}-${i}`}>
              <span className="trail-field">{c.field}</span>
              <ChangeValue change={c} />
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}
