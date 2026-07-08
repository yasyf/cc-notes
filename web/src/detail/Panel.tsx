// Side panel for the selected entity: fetches /api/entity/{kind}/{id}, renders
// the per-kind summary fields and the change trail as a compact list. Errors are
// a plain message — no retries.

import { useEffect, useState } from "react";
import { fetchEntity, type EntityDetail, type EntitySummary, type TrailChange, type TrailEntry, type TrailValue } from "../api";
import type { Selection } from "../store";

interface Props {
  selection: Selection;
  onClose: () => void;
}

type Load =
  | { state: "loading" }
  | { state: "error"; message: string }
  | { state: "ready"; detail: EntityDetail };

export function Panel({ selection, onClose }: Props) {
  const [load, setLoad] = useState<Load>({ state: "loading" });

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
    <aside className="detail" aria-label="Entity detail">
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
      {load.state === "ready" && <DetailBody detail={load.detail} />}
    </aside>
  );
}

function DetailBody({ detail }: { detail: EntityDetail }) {
  const fields = summaryFields(detail.summary);
  return (
    <div className="detail-body">
      {fields.length > 0 && (
        <dl className="detail-fields">
          {fields.map((f) => (
            <div className="detail-field" key={f.label}>
              <dt>{f.label}</dt>
              <dd>{f.value}</dd>
            </div>
          ))}
        </dl>
      )}
      <h3 className="detail-subhead">Trail</h3>
      <ol className="trail">
        {detail.trail.map((e, i) => (
          <TrailRow key={`${e.sha}-${i}`} entry={e} />
        ))}
      </ol>
    </div>
  );
}

function TrailRow({ entry }: { entry: TrailEntry }) {
  return (
    <li className="trail-row">
      <div className="trail-meta">
        <span className={`trail-kind trail-kind-${entry.kind}`}>{entry.kind}</span>
        <span className="trail-time">{formatTime(entry.time)}</span>
        {entry.kind === "checkpoint" && entry.covers > 0 && (
          <span className="trail-covers">covers {entry.covers}</span>
        )}
      </div>
      {entry.changes.length > 0 && (
        <ul className="trail-changes">
          {entry.changes.map((c, i) => (
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

function ChangeValue({ change }: { change: TrailChange }) {
  if (change.scalar) {
    return (
      <span className="trail-delta">
        <span className="trail-from">{toDisplay(change.from)}</span>
        <span className="trail-arrow" aria-hidden="true">
          →
        </span>
        <span className="trail-to">{toDisplay(change.to)}</span>
      </span>
    );
  }
  return (
    <span className="trail-delta">
      {change.added.length > 0 && (
        <span className="trail-added">+{change.added.map(toDisplay).join(", ")}</span>
      )}
      {change.removed.length > 0 && (
        <span className="trail-removed">−{change.removed.map(toDisplay).join(", ")}</span>
      )}
    </span>
  );
}

interface Field {
  label: string;
  value: string;
}

function summaryFields(s: EntitySummary): Field[] {
  const fields: Field[] = [];
  const add = (label: string, value: string | undefined) => {
    if (value !== undefined && value !== "") fields.push({ label, value });
  };
  const addTime = (label: string, value: number | undefined) => {
    if (value !== undefined && value > 0) fields.push({ label, value: formatTime(value) });
  };
  add("id", s.short);
  add("status", s.status);
  add("branch", s.branch);
  add("assignee", s.assignee);
  addTime("started", s.started_at);
  addTime("closed", s.closed_at);
  add("sprint", s.sprint);
  add("project", s.project);
  addTime("verified", s.verified_at);
  if (s.stale === true) add("stale", "yes");
  if (s.superseded === true) add("superseded", "yes");
  addTime("start", s.start_date);
  addTime("end", s.end_date);
  return fields;
}

// toDisplay renders one typed trail value as a compact string: null and the
// empty string as ∅, a folded sub-object as compact JSON, numbers and bools via
// String. Phase 2 replaces this with per-field renderers.
function toDisplay(v: TrailValue): string {
  if (v === null) return "∅";
  if (typeof v === "object") return JSON.stringify(v);
  if (typeof v === "string") return v === "" ? "∅" : v;
  return String(v);
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
