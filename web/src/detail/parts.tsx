// Small presentational atoms shared by the snapshot and trail sections: a
// copy-on-click chip (with commit- and id-labelled variants), plain/label/anchor
// chips, a status badge, a lifecycle timestamp, and the authored block (author
// chip + relative time + markdown body) used for comments and log entries.

import { useState, type ReactNode } from "react";
import type { Anchor, Criterion } from "../api";
import { relativeTime, shortSha } from "../dag/badges";
import { formatDateTime, nowSec, shortId } from "./format";
import { Markdown } from "./Markdown";

export function Chip({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={className ? `chip ${className}` : "chip"}>{children}</span>;
}

export function CopyChip({ text, label, className }: { text: string; label?: string; className?: string }) {
  const [copied, setCopied] = useState(false);
  const classes = ["chip", "chip-copy", className, copied ? "chip-copied" : undefined]
    .filter(Boolean)
    .join(" ");
  return (
    <button
      type="button"
      className={classes}
      title={copied ? "copied" : `copy ${text}`}
      onClick={() => {
        void navigator.clipboard?.writeText(text);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1000);
      }}
    >
      {label ?? text}
    </button>
  );
}

export function CommitChip({ sha }: { sha: string }) {
  return <CopyChip text={sha} label={shortSha(sha)} className="chip-commit" />;
}

export function IdChip({ id }: { id: string }) {
  return <CopyChip text={id} label={shortId(id)} className="chip-id" />;
}

export function AnchorChip({ anchor }: { anchor: Anchor }) {
  return (
    <span className="chip chip-anchor">
      <span className="chip-key">{anchor.kind}</span>
      {anchor.value}
    </span>
  );
}

export function CriterionChip({ criterion }: { criterion: Criterion }) {
  return (
    <span className="chip chip-criterion" title={criterion.script || undefined}>
      <StatusBadge status={criterion.status} />
      <span className="chip-crit-text">{criterion.text}</span>
    </span>
  );
}

function statusTone(s: string): string {
  switch (s) {
    case "met":
    case "passed":
    case "done":
    case "closed":
    case "completed":
      return "good";
    case "failed":
      return "bad";
    case "blocked":
    case "stale":
      return "warn";
    case "in_progress":
    case "active":
    case "started":
    case "open":
      return "accent";
    default:
      return "muted";
  }
}

export function StatusBadge({ status }: { status: string }) {
  return (
    <span className={`badge badge-${statusTone(status)}`}>{status.replace(/_/g, " ")}</span>
  );
}

export function TimeText({ sec }: { sec: number }) {
  if (sec <= 0) return <span className="empty">∅</span>;
  return (
    <span className="time-text" title={new Date(sec * 1000).toISOString()}>
      {formatDateTime(sec)}
    </span>
  );
}

export function AuthoredBlock({
  author,
  ts,
  body,
  sign,
}: {
  author: string;
  ts: number;
  body: string;
  sign?: "+" | "-";
}) {
  const classes = ["authored", sign === "-" ? "authored-removed" : undefined]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={classes}>
      <div className="authored-head">
        {sign !== undefined && (
          <span className="authored-sign" aria-hidden="true">
            {sign === "+" ? "＋" : "−"}
          </span>
        )}
        <span className="chip chip-author">{author}</span>
        <span className="authored-time" title={formatDateTime(ts)}>
          {relativeTime(ts, nowSec())}
        </span>
      </div>
      {body.trim() !== "" && (
        <div className="authored-body">
          <Markdown>{body}</Markdown>
        </div>
      )}
    </div>
  );
}
