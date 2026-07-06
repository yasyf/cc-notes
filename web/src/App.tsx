import { useEffect, useState } from "react";
import { fetchGraph, fetchRepo, type Graph, type RepoInfo } from "./api";

type Tab = "timeline" | "commits";

interface Status {
  repo: RepoInfo | null;
  graph: Graph | null;
  error: string | null;
}

export default function App() {
  const [tab, setTab] = useState<Tab>("timeline");
  const [status, setStatus] = useState<Status>({
    repo: null,
    graph: null,
    error: null,
  });

  useEffect(() => {
    let cancelled = false;
    Promise.all([fetchRepo(), fetchGraph()])
      .then(([repo, graph]) => {
        if (!cancelled) setStatus({ repo, graph, error: null });
      })
      .catch((err: unknown) => {
        if (!cancelled)
          setStatus({ repo: null, graph: null, error: String(err) });
      });
    return () => {
      cancelled = true;
    };
  }, []);

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
      </header>

      <main className="app-main">
        {tab === "timeline" ? (
          <section className="pane" aria-label="Timeline">
            <p className="placeholder">Timeline view lands in a later phase.</p>
          </section>
        ) : (
          <section className="pane" aria-label="Commits">
            <p className="placeholder">Commit DAG view lands in a later phase.</p>
          </section>
        )}
      </main>

      <StatusStrip status={status} />
    </div>
  );
}

function StatusStrip({ status }: { status: Status }) {
  if (status.error !== null) {
    return (
      <footer className="status status-error" role="status">
        <span>failed to load: {status.error}</span>
      </footer>
    );
  }
  if (status.repo === null || status.graph === null) {
    return (
      <footer className="status" role="status">
        <span>loading…</span>
      </footer>
    );
  }
  const { repo, graph } = status;
  return (
    <footer className="status" role="status">
      <span className="status-item">
        root <code>{repo.root}</code>
      </span>
      <span className="status-item">
        trunk <code>{repo.trunk}</code>
      </span>
      <span className="status-item">
        head <code>{repo.head || "(detached)"}</code>
      </span>
      <span className="status-sep" aria-hidden="true">
        ·
      </span>
      <span className="status-item">{graph.lanes.length} lanes</span>
      <span className="status-item">{graph.events.length} events</span>
      <span className="status-item">{graph.entities.length} entities</span>
    </footer>
  );
}
