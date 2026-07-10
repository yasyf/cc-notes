import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { fetchEntities, fetchGraph, fetchRepo } from "./api";
import { Browse } from "./browse/Browse";
import { HeaderSearch } from "./browse/HeaderSearch";
import { relativeTime } from "./dag/badges";
import { CommitGraph } from "./dag/CommitGraph";
import { useCommitsLoader } from "./dag/useCommits";
import { nowSec } from "./detail/format";
import { PanelLazy } from "./detail/lazy";
import { formatRoute, parseRoute } from "./route";
import { createSequencer } from "./seq";
import { connectStream, type Connection } from "./stream";
import {
  StoreProvider,
  useDispatch,
  useStore,
  type Selection,
  type Tab,
} from "./store";
import {
  applyFilters,
  NO_FILTERS,
  presentKinds,
  presentTypes,
  type TimelineFilters,
} from "./timeline/filters";
import { layout } from "./timeline/layout";
import { Swimlanes } from "./timeline/Swimlanes";

export default function App() {
  return (
    <StoreProvider>
      <AppShell />
    </StoreProvider>
  );
}

function AppShell() {
  const dispatch = useDispatch();
  const { tab, selection, graph, entities } = useStore();
  const { loadFirst: loadCommits } = useCommitsLoader();

  const tabRef = useRef(tab);
  tabRef.current = tab;

  // A refs event can fire load() while an earlier load() is still in flight; the
  // sequencer tags each request and drops any response superseded by a later one
  // so an older graph never rolls the UI back over fresh data. repo and graph
  // resolve together under one token, so the repo fetch is covered too.
  const loadSeq = useRef(createSequencer());
  const load = useCallback(() => {
    const token = loadSeq.current.next();
    Promise.all([fetchRepo(), fetchGraph()])
      .then(([repo, graph]) => {
        if (loadSeq.current.isLatest(token)) dispatch({ type: "loaded", repo, graph });
      })
      .catch((err: unknown) => {
        if (loadSeq.current.isLatest(token))
          dispatch({ type: "load-error", error: String(err) });
      });
  }, [dispatch]);

  // Entities feed the Browse tab and the global header search, so they load
  // eagerly on mount (search works from any tab) and refetch on refs events while
  // Browse is active. The generation guard mirrors the commits loader: each fetch
  // carries gen+1 and the reducer drops any superseded response.
  const entGenRef = useRef(entities.gen);
  entGenRef.current = entities.gen;
  const loadEntities = useCallback(() => {
    const gen = entGenRef.current + 1;
    dispatch({ type: "entities-load-start", gen });
    fetchEntities()
      .then((data) => dispatch({ type: "entities-loaded", data, gen }))
      .catch((err: unknown) =>
        dispatch({ type: "entities-load-error", error: String(err), gen }),
      );
  }, [dispatch]);

  // A refs event refetches the graph; while the Commits or Browse tab is active
  // it also refetches that tab's data (re-walking from the tip beats splicing).
  const refresh = useCallback(() => {
    load();
    if (tabRef.current === "commits") loadCommits();
    if (tabRef.current === "browse") loadEntities();
  }, [load, loadCommits, loadEntities]);

  useEffect(() => {
    load();
    loadEntities();
    const dispose = connectStream({
      onRefresh: refresh,
      onConnection: (connection) => dispatch({ type: "connection", connection }),
      onGen: (gen) => dispatch({ type: "gen", gen }),
    });
    return dispose;
  }, [load, loadEntities, refresh, dispatch]);

  // Hash → store: apply the fragment on mount and on every back/forward or manual
  // edit. parseRoute never throws, so a garbled fragment falls back to timeline.
  useEffect(() => {
    const apply = () => {
      const route = parseRoute(window.location.hash);
      dispatch({ type: "tab", tab: route.tab });
      dispatch({ type: "select", selection: route.selection });
    };
    apply();
    window.addEventListener("hashchange", apply);
    return () => window.removeEventListener("hashchange", apply);
  }, [dispatch]);

  // Store → hash: mirror tab/selection back into the fragment without a
  // hashchange loop (history.pushState/replaceState fire no event). The first
  // run is skipped so the mount-time read owns the initial URL; a tab change
  // pushes a history entry, a selection-only change replaces it (no spam).
  const firstWriteRef = useRef(true);
  const prevTabRef = useRef<Tab>(tab);
  useEffect(() => {
    const want = formatRoute({ tab, selection });
    if (firstWriteRef.current) {
      firstWriteRef.current = false;
      prevTabRef.current = tab;
      return;
    }
    if (window.location.hash === want) {
      prevTabRef.current = tab;
      return;
    }
    const tabChanged = prevTabRef.current !== tab;
    prevTabRef.current = tab;
    if (tabChanged) window.history.pushState(null, "", want);
    else window.history.replaceState(null, "", want);
  }, [tab, selection]);

  // A selection restored from a shared URL carries no title; fill it from loaded
  // data once available. formatRoute ignores the title, so this never rewrites
  // the URL.
  const titleLookup = useMemo(() => {
    const m = new Map<string, string>();
    for (const e of graph?.entities ?? []) if (e.title !== "") m.set(e.id, e.title);
    const d = entities.data;
    if (d !== null) {
      for (const bucket of [d.notes, d.docs, d.logs, d.tasks, d.sprints, d.projects, d.runbooks]) {
        for (const s of bucket) if (s.title !== "") m.set(s.id, s.title);
      }
    }
    return m;
  }, [graph, entities.data]);
  useEffect(() => {
    if (selection === null || selection.title !== "") return;
    const title = titleLookup.get(selection.id);
    if (title !== undefined && title !== "") {
      dispatch({ type: "select", selection: { ...selection, title } });
    }
  }, [selection, titleLookup, dispatch]);

  const select = useCallback(
    (sel: Selection | null) => dispatch({ type: "select", selection: sel }),
    [dispatch],
  );
  const setTab = useCallback(
    (next: Tab) => dispatch({ type: "tab", tab: next }),
    [dispatch],
  );

  return (
    <div className="app">
      <header className="app-header">
        <h1>cc-notes viz</h1>
        <nav className="tabs">
          <TabButton tab="timeline" active={tab} label="Timeline" onClick={setTab} />
          <TabButton tab="commits" active={tab} label="Commits" onClick={setTab} />
          <TabButton tab="browse" active={tab} label="Browse" onClick={setTab} />
        </nav>
        <HeaderSearch onSelect={select} />
        <RepoBadge />
      </header>

      <main className="app-main">
        {tab === "timeline" && <TimelinePane onSelect={select} />}
        {tab === "commits" && <CommitsPane onSelect={select} />}
        {tab === "browse" && <Browse onSelect={select} />}
      </main>
    </div>
  );
}

function TabButton({
  tab,
  active,
  label,
  onClick,
}: {
  tab: Tab;
  active: Tab;
  label: string;
  onClick: (t: Tab) => void;
}) {
  return (
    <button
      type="button"
      className={active === tab ? "tab tab-active" : "tab"}
      onClick={() => onClick(tab)}
    >
      {label}
    </button>
  );
}

// toggleMember returns a copy of set with value flipped in or out.
function toggleMember(set: ReadonlySet<string>, value: string): Set<string> {
  const next = new Set(set);
  if (next.has(value)) next.delete(value);
  else next.add(value);
  return next;
}

function TimelinePane({ onSelect }: { onSelect: (sel: Selection | null) => void }) {
  const { graph, selection, error, loading } = useStore();
  // Legend/lane filters and lane-collapse toggles are local to the timeline pane:
  // filters apply before layout so packing tightens, collapse feeds into layout.
  const [filters, setFilters] = useState<TimelineFilters>(NO_FILTERS);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());

  const now = useMemo(() => Math.floor(Date.now() / 1000), [graph]);
  const filtered = useMemo(
    () => (graph !== null ? applyFilters(graph, filters) : null),
    [graph, filters],
  );
  const result = useMemo(
    () => (filtered !== null ? layout({ graph: filtered, now, collapsed }) : null),
    [filtered, now, collapsed],
  );
  const kinds = useMemo(() => (graph !== null ? presentKinds(graph) : new Set<string>()), [graph]);
  const types = useMemo(() => (graph !== null ? presentTypes(graph) : new Set<string>()), [graph]);

  const onToggleKind = useCallback(
    (k: string) => setFilters((f) => ({ ...f, hiddenKinds: toggleMember(f.hiddenKinds, k) })),
    [],
  );
  const onToggleType = useCallback(
    (t: string) => setFilters((f) => ({ ...f, hiddenTypes: toggleMember(f.hiddenTypes, t) })),
    [],
  );
  const onToggleLane = useCallback(
    (l: string) => setFilters((f) => ({ ...f, lane: f.lane === l ? null : l })),
    [],
  );
  const onToggleCollapse = useCallback(
    (l: string) => setCollapsed((prev) => toggleMember(prev, l)),
    [],
  );

  if (error !== null && graph === null) {
    return (
      <section className="pane pane-msg" aria-label="Timeline">
        <p className="placeholder detail-error">failed to load: {error}</p>
      </section>
    );
  }
  if (result === null || loading) {
    return (
      <section className="pane pane-msg" aria-label="Timeline">
        <p className="placeholder">Loading timeline…</p>
      </section>
    );
  }
  if (result.lanes.length === 0) {
    return (
      <section className="pane pane-msg" aria-label="Timeline">
        <p className="placeholder">No branches to show yet.</p>
      </section>
    );
  }

  return (
    <section className="pane pane-timeline" aria-label="Timeline">
      <div className="timeline-grid">
        <Swimlanes
          result={result}
          selection={selection}
          onSelect={(s) => onSelect(s)}
          filters={filters}
          presentKinds={kinds}
          presentTypes={types}
          onToggleKind={onToggleKind}
          onToggleType={onToggleType}
          onToggleLane={onToggleLane}
          onToggleCollapse={onToggleCollapse}
        />
        {selection !== null && (
          <PanelLazy selection={selection} onClose={() => onSelect(null)} />
        )}
      </div>
    </section>
  );
}

function CommitsPane({ onSelect }: { onSelect: (sel: Selection | null) => void }) {
  const { selection } = useStore();
  return (
    <section className="pane pane-timeline" aria-label="Commits">
      <div className="timeline-grid">
        <CommitGraph selection={selection} onSelect={onSelect} />
        {selection !== null && (
          <PanelLazy selection={selection} onClose={() => onSelect(null)} />
        )}
      </div>
    </section>
  );
}

function RepoBadge() {
  const { repo, connection } = useStore();
  return (
    <div className="repo-badge">
      {repo !== null && (
        <>
          <span className="repo-item">
            trunk <code>{repo.trunk}</code>
          </span>
          <span className="repo-item">
            head <code>{repo.head || "(detached)"}</code>
          </span>
          <GeneratedAt iso={repo.generated_at} />
          {repo.truncated && (
            <span className="repo-trunc" title="History was truncated to the view window">
              truncated
            </span>
          )}
        </>
      )}
      <LiveDot connection={connection} />
    </div>
  );
}

function GeneratedAt({ iso }: { iso: string }) {
  const sec = Math.floor(new Date(iso).getTime() / 1000);
  if (!Number.isFinite(sec)) return null;
  const rel = relativeTime(sec, nowSec());
  return (
    <span className="repo-item" title={iso}>
      {rel === "now" ? "generated just now" : `generated ${rel} ago`}
    </span>
  );
}

function LiveDot({ connection }: { connection: Connection }) {
  const label =
    connection === "live"
      ? "live"
      : connection === "disconnected"
        ? "disconnected"
        : "connecting";
  return (
    <span className={`live live-${connection}`} role="status" aria-label={`stream ${label}`}>
      <span className="live-dot" aria-hidden="true" />
      {label}
    </span>
  );
}
