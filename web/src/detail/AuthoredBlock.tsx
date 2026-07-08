// The authored block (author chip + relative time + markdown body) used for
// comments and log entries. Split out of parts.tsx so its markdown/highlight
// dependency stays in the lazy-loaded panel chunk rather than the entry bundle:
// parts.tsx's atoms are imported by entry-chunk browse views, this is not.

import { relativeTime } from "../dag/badges";
import { formatDateTime, nowSec } from "./format";
import { Markdown } from "./Markdown";

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
