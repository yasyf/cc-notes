// Browse tab: the current total state of every entity as a filterable, sortable
// table (default) or a task kanban. Filters and the live search box both narrow
// the same index and feed both views; a row/card click opens the shared detail
// Panel. Filters, sort, view mode, and query are view state — only the selection
// is routed. View mode persists to localStorage.

import { useCallback, useEffect, useMemo, useState } from "react";
import { Panel } from "../detail/Panel";
import { useStore, type Selection } from "../store";
import { EntityTable, nextSort, sortRows, type ColKey, type SortState } from "./EntityTable";
import { emptyFilters, FilterBar, matchesFilters, type FacetKey, type Filters } from "./FilterBar";
import { buildIndex, titleMap, type Row } from "./index";
import { Kanban } from "./Kanban";
import { search, type Span } from "./search";

type View = "table" | "kanban";
const VIEW_KEY = "cc-notes:browse-view";

function initialView(): View {
  return window.localStorage.getItem(VIEW_KEY) === "kanban" ? "kanban" : "table";
}

export function Browse({ onSelect }: { onSelect: (sel: Selection | null) => void }) {
  const { entities, selection } = useStore();
  const data = entities.data;

  const index = useMemo(() => (data !== null ? buildIndex(data) : []), [data]);
  const titles = useMemo(() => titleMap(index), [index]);

  const [filters, setFilters] = useState<Filters>(emptyFilters);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SortState>({ col: null, dir: "asc" });
  const [view, setView] = useState<View>(initialView);

  useEffect(() => {
    window.localStorage.setItem(VIEW_KEY, view);
  }, [view]);

  const filtered = useMemo(
    () => index.filter((r) => matchesFilters(r, filters)),
    [index, filters],
  );

  const bySearch = query.trim() !== "";
  const { ordered, spans } = useMemo((): { ordered: Row[]; spans: Map<string, Span[]> } => {
    if (!bySearch) return { ordered: sortRows(filtered, sort, titles), spans: new Map() };
    const byId = new Map(filtered.map((r) => [r.id, r]));
    const hits = search(filtered, query);
    return {
      ordered: hits.flatMap((h) => {
        const row = byId.get(h.id);
        return row !== undefined ? [row] : [];
      }),
      spans: new Map(hits.map((h) => [h.id, h.spans])),
    };
  }, [bySearch, filtered, query, sort, titles]);

  const taskRows = useMemo(() => ordered.filter((r) => r.kind === "task"), [ordered]);
  const otherRows = useMemo(() => ordered.filter((r) => r.kind !== "task"), [ordered]);

  const toggleFacet = useCallback((facet: FacetKey, value: string) => {
    setFilters((prev) => {
      const set = new Set(prev[facet]);
      if (set.has(value)) set.delete(value);
      else set.add(value);
      return { ...prev, [facet]: set };
    });
  }, []);

  const onSort = useCallback((col: ColKey) => setSort((prev) => nextSort(prev, col)), []);
  const select = useCallback((sel: Selection) => onSelect(sel), [onSelect]);

  if (data === null) {
    return (
      <section className="pane pane-msg" aria-label="Browse">
        <p className="placeholder">
          {entities.error !== null ? `failed to load: ${entities.error}` : "Loading entities…"}
        </p>
      </section>
    );
  }

  return (
    <section className="pane pane-timeline" aria-label="Browse">
      <div className="timeline-grid">
        <div className="browse-root">
          <div className="browse-toolbar">
            <div className="browse-search">
              <input
                type="search"
                className="browse-search-input"
                placeholder="Search entities…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") setQuery("");
                }}
              />
            </div>
            <div className="browse-view" role="group" aria-label="View mode">
              <button
                type="button"
                className={view === "table" ? "browse-view-btn browse-view-on" : "browse-view-btn"}
                aria-pressed={view === "table"}
                onClick={() => setView("table")}
              >
                Table
              </button>
              <button
                type="button"
                className={view === "kanban" ? "browse-view-btn browse-view-on" : "browse-view-btn"}
                aria-pressed={view === "kanban"}
                onClick={() => setView("kanban")}
              >
                Kanban
              </button>
            </div>
            <span className="browse-count">
              {ordered.length} of {index.length}
              {bySearch && <span className="browse-hint"> · by relevance</span>}
            </span>
          </div>

          <FilterBar
            rows={index}
            filters={filters}
            titles={titles}
            onToggle={toggleFacet}
            onClear={() => setFilters(emptyFilters())}
          />

          <div className="browse-body">
            {view === "table" ? (
              <EntityTable
                rows={ordered}
                titles={titles}
                sort={sort}
                bySearch={bySearch}
                spans={spans}
                selection={selection}
                onSort={onSort}
                onSelect={select}
              />
            ) : (
              <>
                <Kanban tasks={taskRows} titles={titles} selection={selection} onSelect={select} />
                {otherRows.length > 0 && (
                  <div className="browse-other">
                    <h3 className="browse-other-head">Other entities</h3>
                    <EntityTable
                      rows={otherRows}
                      titles={titles}
                      sort={sort}
                      bySearch={bySearch}
                      spans={spans}
                      selection={selection}
                      onSort={onSort}
                      onSelect={select}
                    />
                  </div>
                )}
              </>
            )}
          </div>
        </div>
        {selection !== null && <Panel selection={selection} onClose={() => onSelect(null)} />}
      </div>
    </section>
  );
}
