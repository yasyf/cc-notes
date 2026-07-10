// Pure hash-route codec. The URL fragment is the single source of truth for
// which tab is showing and which entity's detail panel is open, so a viz URL is
// shareable and the back button navigates tabs. Malformed fragments fall back to
// the timeline tab and drop an unparseable selection rather than throwing.

import type { Selection, Tab } from "./store";

export interface Route {
  tab: Tab;
  selection: Selection | null;
}

const TABS: readonly Tab[] = ["timeline", "commits", "browse"];
const KINDS: readonly string[] = ["note", "doc", "log", "task", "sprint", "project", "runbook"];

function toTab(path: string): Tab {
  const name = path.replace(/^\//, "");
  return (TABS as readonly string[]).includes(name) ? (name as Tab) : "timeline";
}

// A selection restored from the fragment carries no title — only kind:id round-
// trip — so callers enrich it from loaded data. An empty title is expected here.
function parseSelection(query: string): Selection | null {
  for (const pair of query.split("&")) {
    if (!pair.startsWith("e=")) continue;
    const val = pair.slice(2);
    const colon = val.indexOf(":");
    if (colon <= 0) return null;
    try {
      const kind = decodeURIComponent(val.slice(0, colon));
      const id = decodeURIComponent(val.slice(colon + 1));
      if (!KINDS.includes(kind) || id === "") return null;
      return { kind, id, title: "" };
    } catch {
      return null;
    }
  }
  return null;
}

// parseRoute decodes a location.hash fragment into a Route, tolerating any
// malformed input by falling back to the timeline tab with no selection.
export function parseRoute(hash: string): Route {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  const q = raw.indexOf("?");
  const path = q >= 0 ? raw.slice(0, q) : raw;
  const query = q >= 0 ? raw.slice(q + 1) : "";
  return { tab: toTab(path), selection: parseSelection(query) };
}

// formatRoute encodes a Route back into a location.hash fragment. The selection
// is carried as ?e=<kind>:<id>; its title is intentionally omitted.
export function formatRoute(route: Route): string {
  const base = `#/${route.tab}`;
  if (route.selection === null) return base;
  const { kind, id } = route.selection;
  return `${base}?e=${encodeURIComponent(kind)}:${encodeURIComponent(id)}`;
}
