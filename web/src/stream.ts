// SSE client for GET /api/stream. A single EventSource relays "refs" events;
// each one debounces a refetch (bursts of ref moves collapse into one reload)
// and reports connection health. EventSource reconnects on its own, so a drop is
// surfaced as "disconnected" and clears back to "live" on the next open.

export type Connection = "connecting" | "live" | "disconnected";

const DEBOUNCE_MS = 250;

// RefsPayload mirrors internal/viz.refsEvent — the JSON carried on every
// ref-change event.
export interface RefsPayload {
  gen: number;
  heads: string[];
  entities: string[];
  head: string;
}

export interface StreamHandlers {
  onRefresh: () => void;
  onConnection: (c: Connection) => void;
  onGen?: (gen: number) => void;
}

// connectStream opens the stream and returns a disposer that cancels any pending
// refetch and closes the connection.
export function connectStream(handlers: StreamHandlers): () => void {
  const es = new EventSource("/api/stream");
  let timer: number | undefined;

  const scheduleRefresh = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = window.setTimeout(() => {
      timer = undefined;
      handlers.onRefresh();
    }, DEBOUNCE_MS);
  };

  es.addEventListener("open", () => handlers.onConnection("live"));
  es.addEventListener("error", () => handlers.onConnection("disconnected"));
  es.addEventListener("refs", (ev: MessageEvent<string>) => {
    handlers.onConnection("live");
    const gen = parseGen(ev.data);
    if (gen !== null && handlers.onGen) handlers.onGen(gen);
    scheduleRefresh();
  });

  return () => {
    if (timer !== undefined) clearTimeout(timer);
    es.close();
  };
}

function parseGen(data: string): number | null {
  try {
    const payload = JSON.parse(data) as Partial<RefsPayload>;
    return typeof payload.gen === "number" ? payload.gen : null;
  } catch {
    return null;
  }
}
