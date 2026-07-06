// Commit-DAG tab: assigns every fetched commit a column (pure columns.ts),
// renders rows newest -> oldest with continuous rails, and pages older history
// on demand. The legend reuses the timeline's event-glyph vocabulary; colour on
// the rails is decoration only (identity comes from a rail's position).

import { useEffect, useMemo, useState } from "react";
import { Glyph } from "../timeline/Glyph";
import { EVENT_SPECS, eventSpec } from "../timeline/marks";
import type { CommitPage } from "../api";
import { useStore, type Selection } from "../store";
import { assignColumns } from "./columns";
import { Row } from "./Row";
import { useCommitsLoader } from "./useCommits";

interface Props {
  selection: Selection | null;
  onSelect: (sel: Selection | null) => void;
}

export function CommitGraph({ selection, onSelect }: Props) {
  const { commits } = useStore();
  const { loadFirst, loadOlder } = useCommitsLoader();
  const [activeSha, setActiveSha] = useState<string | null>(null);

  useEffect(() => {
    if (!commits.loaded && !commits.loading) loadFirst();
  }, [commits.loaded, commits.loading, loadFirst]);

  const now = useMemo(() => Math.floor(Date.now() / 1000), [commits.rows]);
  const layout = useMemo(
    () => assignColumns(commits.rows.map((c) => ({ sha: c.sha, parents: c.parents }))),
    [commits.rows],
  );

  if (commits.error !== null && commits.rows.length === 0) {
    return (
      <section className="pane pane-msg" aria-label="Commits">
        <p className="placeholder detail-error">failed to load commits: {commits.error}</p>
      </section>
    );
  }
  if (!commits.loaded && commits.loading) {
    return (
      <section className="pane pane-msg" aria-label="Commits">
        <p className="placeholder">Loading commits…</p>
      </section>
    );
  }
  if (commits.rows.length === 0) {
    return (
      <section className="pane pane-msg" aria-label="Commits">
        <p className="placeholder">No commits in this window.</p>
      </section>
    );
  }

  const selectedId = selection?.id ?? null;

  return (
    <div className="dag-root">
      <div className="dag-toolbar">
        <CommitLegend rows={commits.rows} />
      </div>
      <div className="dag-body" role="table" aria-label="Commit graph">
        {commits.rows.map((c, i) => (
          <Row
            key={c.sha}
            commit={c}
            node={layout.rows[i]}
            totalColumns={layout.totalColumns}
            now={now}
            active={activeSha === c.sha}
            selectedId={selectedId}
            onSelectRow={setActiveSha}
            onSelectEntity={(sel) => onSelect(sel)}
          />
        ))}
        {commits.nextBefore !== null && (
          <div className="dag-more">
            <button
              type="button"
              className="dag-more-btn"
              disabled={commits.loading}
              onClick={() => loadOlder(commits.nextBefore as string)}
            >
              {commits.loading ? "Loading…" : "Load older"}
            </button>
          </div>
        )}
        {commits.nextBefore === null && commits.truncated && (
          <p className="dag-endcap">
            History horizon reached — older commits beyond the graph window are not shown.
          </p>
        )}
      </div>
    </div>
  );
}

// CommitLegend lists the event glyphs actually present in the loaded pages, in
// the canonical EVENT_SPECS order, so identity is never colour-alone.
function CommitLegend({ rows }: { rows: CommitPage[] }) {
  const present = new Set<string>();
  for (const c of rows) {
    for (const ev of c.events) present.add(ev.type);
  }
  const keys = Object.keys(EVENT_SPECS).filter((t) => present.has(t));
  if (keys.length === 0) {
    return <span className="dag-legend-empty">No entity events on these commits.</span>;
  }
  return (
    <div className="tl-legend" role="list" aria-label="Commit events legend">
      {keys.map((t) => {
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
