import { useCallback, useEffect, useMemo, useState } from "react";
import { fetchGraph, fetchRepo } from "./api";
import { Panel } from "./detail/Panel";
import { connectStream, type Connection } from "./stream";
import {
  StoreProvider,
  useDispatch,
  useStore,
  type Selection,
} from "./store";
import { layout } from "./timeline/layout";
import { Swimlanes } from "./timeline/Swimlanes";

type Tab = "timeline" | "commits";

export default function App() {
  return (
    <StoreProvider>
      <AppShell />
    </StoreProvider>
  );
}

function AppShell() {
  const dispatch = useDispatch();
  const [tab, setTab] = useState<Tab>("timeline");

  const load = useCallback(() => {
    Promise.all([fetchRepo(), fetchGraph()])
      .then(([repo, graph]) => dispatch({ type: "loaded", repo, graph }))
      .catch((err: unknown) => dispatch({ type: "load-error", error: String(err) }));
  }, [dispatch]);

  useEffect(() => {
    load();
    const dispose = connectStream({
      onRefresh: load,
      onConnection: (connection) => dispatch({ type: "connection", connection }),
      onGen: (gen) => dispatch({ type: "gen", gen }),
    });
    return dispose;
  }, [load, dispatch]);

  const select = useCallback(
    (selection: Selection | null) => dispatch({ type: "select", selection }),
    [dispatch],
  );

  return (
    <div className="app">
      <header className="app-header">
        <h1>cc-notes viz</h1>
        <nav className="tabs">
          <button
            type="button"
            className={tab === "timeline" ? "tab tab-active" : "tab"}
            onClick={() => setTab("timeline")}
          >
            Timeline
          </button>
          <button
            type="button"
            className={tab === "commits" ? "tab tab-active" : "tab"}
            onClick={() => setTab("commits")}
          >
            Commits
          </button>
        </nav>
        <RepoBadge />
      </header>

      <main className="app-main">
        {tab === "timeline" ? (
          <TimelinePane onSelect={select} />
        ) : (
          <section className="pane" aria-label="Commits">
            <p className="placeholder">Commit DAG view lands in a later phase.</p>
          </section>
        )}
      </main>
    </div>
  );
}

function TimelinePane({ onSelect }: { onSelect: (sel: Selection | null) => void }) {
  const { graph, selection, error, loading } = useStore();
  const now = useMemo(() => Math.floor(Date.now() / 1000), [graph]);
  const result = useMemo(
    () => (graph !== null ? layout({ graph, now }) : null),
    [graph, now],
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
        <Swimlanes result={result} selection={selection} onSelect={(s) => onSelect(s)} />
        {selection !== null && (
          <Panel selection={selection} onClose={() => onSelect(null)} />
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
        </>
      )}
      <LiveDot connection={connection} />
    </div>
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
