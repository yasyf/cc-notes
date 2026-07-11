// Commit-DAG tab: assigns every fetched commit a column (pure columns.ts),
// renders rows newest -> oldest with continuous rails, and pages older history
// on demand. The legend reuses the timeline's event-glyph vocabulary; colour on
// the rails is decoration only (identity comes from a rail's position).

import { useEffect, useMemo, useState } from "react";
import { Glyph } from "../timeline/Glyph";
import { EVENT_SPECS, eventSpec } from "../timeline/marks";
import type { CommitPage } from "../api";
import { shortId } from "../detail/format";
import { useDispatch, useStore, type Selection } from "../store";
import { assignColumns } from "./columns";
import { Row } from "./Row";
import { useCommitsLoader } from "./useCommits";

interface Props {
  selection: Selection | null;
  onSelect: (sel: Selection | null) => void;
}

// rowMatches reports whether a commit matches the filter query (case-insensitive)
// across its summary, author, sha, and task ids (full and short form).
function rowMatches(c: CommitPage, needle: string): boolean {
  if (
    c.summary.toLowerCase().includes(needle) ||
    c.author.toLowerCase().includes(needle) ||
    c.sha.toLowerCase().includes(needle)
  ) {
    return true;
  }
  return c.tasks.some(
    (id) =>
      id.toLowerCase().includes(needle) || shortId(id).toLowerCase().includes(needle),
  );
}

export function CommitGraph({ selection, onSelect }: Props) {
  const { commits, focusCommit } = useStore();
  const dispatch = useDispatch();
  const { loadFirst, loadOlder } = useCommitsLoader();
  const [activeSha, setActiveSha] = useState<string | null>(null);
  const [filter, setFilter] = useState("");

  useEffect(() => {
    if (!commits.loaded && !commits.loading) loadFirst();
  }, [commits.loaded, commits.loading, loadFirst]);

  // A focus request from a detail-panel commit chip flashes the matching row,
  // then clears itself so the one-shot request does not re-fire on later renders.
  // The clear waits out the flash animation.
  useEffect(() => {
    if (focusCommit === null) return;
    if (!commits.rows.some((c) => c.sha === focusCommit)) return;
    const t = window.setTimeout(() => dispatch({ type: "focus-commit", sha: null }), 1600);
    return () => window.clearTimeout(t);
  }, [focusCommit, commits.rows, dispatch]);

  const now = useMemo(() => Math.floor(Date.now() / 1000), [commits.rows]);
  const layout = useMemo(
    () => assignColumns(commits.rows.map((c) => ({ sha: c.sha, parents: c.parents }))),
    [commits.rows],
  );

  const needle = filter.trim().toLowerCase();
  const filtering = needle !== "";
  const visibleCount = filtering
    ? commits.rows.reduce((n, c) => n + (rowMatches(c, needle) ? 1 : 0), 0)
    : commits.rows.length;

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
        <div className="dag-filter">
          <input
            type="search"
            className="dag-filter-input"
            placeholder="Filter commits… (summary, author, task)"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            aria-label="Filter commits"
          />
          {filtering && (
            <span className="dag-filter-count">
              {visibleCount} of {commits.rows.length} loaded
            </span>
          )}
        </div>
        <CommitLegend rows={commits.rows} />
      </div>
      <div className="dag-body" role="table" aria-label="Commit graph">
        {filtering && visibleCount === 0 && (
          <p className="dag-endcap">No loaded commits match “{filter.trim()}”.</p>
        )}
        {commits.rows.map((c, i) =>
          filtering && !rowMatches(c, needle) ? null : (
            <Row
              key={c.sha}
              commit={c}
              node={layout.rows[i]}
              totalColumns={layout.totalColumns}
              now={now}
              active={activeSha === c.sha}
              flash={focusCommit === c.sha}
              hideGraph={filtering}
              selectedId={selectedId}
              onSelectRow={setActiveSha}
              onSelectEntity={(sel) => onSelect(sel)}
            />
          ),
        )}
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
