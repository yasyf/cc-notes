// Global jump-to-entity search in the header. "/" focuses it from anywhere
// (unless already typing in a field), Escape clears then blurs, arrow keys walk
// the dropdown of top hits, and Enter opens the highlighted entity's panel in the
// current tab. It searches the same index the Browse tab uses.

import { useEffect, useMemo, useRef, useState } from "react";
import { shortId } from "../detail/format";
import { useStore, type Selection } from "../store";
import { buildIndex } from "./index";
import { Highlight, KindBadge } from "./parts";
import { search } from "./search";

const MAX_HITS = 8;

function isEditable(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable;
}

export function HeaderSearch({ onSelect }: { onSelect: (sel: Selection) => void }) {
  const { entities } = useStore();
  const data = entities.data;

  const rowsById = useMemo(
    () => new Map((data !== null ? buildIndex(data) : []).map((r) => [r.id, r])),
    [data],
  );
  const targets = useMemo(() => [...rowsById.values()], [rowsById]);

  const [query, setQuery] = useState("");
  const [focused, setFocused] = useState(false);
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  const hits = useMemo(
    () => (query.trim() !== "" ? search(targets, query).slice(0, MAX_HITS) : []),
    [targets, query],
  );
  const open = focused && hits.length > 0;
  const activeIdx = Math.min(active, hits.length - 1);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "/" && !isEditable(e.target) && document.activeElement !== inputRef.current) {
        e.preventDefault();
        inputRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const pick = (index: number) => {
    const hit = hits[index];
    if (hit === undefined) return;
    const row = rowsById.get(hit.id);
    if (row === undefined) return;
    onSelect({ kind: row.kind, id: row.id, title: row.title });
    setQuery("");
    setActive(0);
    inputRef.current?.blur();
  };

  return (
    <div className="hsearch">
      <input
        ref={inputRef}
        type="search"
        className="hsearch-input"
        placeholder="Jump to entity…  /"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
        }}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setActive((a) => Math.min(a + 1, hits.length - 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive((a) => Math.max(a - 1, 0));
          } else if (e.key === "Enter") {
            e.preventDefault();
            pick(activeIdx);
          } else if (e.key === "Escape") {
            if (query !== "") setQuery("");
            else inputRef.current?.blur();
          }
        }}
      />
      {open && (
        <ul className="hsearch-menu" role="listbox">
          {hits.flatMap((h, i) => {
            const row = rowsById.get(h.id);
            if (row === undefined) return [];
            const title = row.title || shortId(h.id);
            return [
              <li
                key={h.id}
                role="option"
                aria-selected={i === activeIdx}
                className={i === activeIdx ? "hsearch-item hsearch-item-on" : "hsearch-item"}
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(i);
                }}
              >
                <KindBadge kind={row.kind} />
                <span className="hsearch-item-title">
                  <Highlight text={title} spans={h.spans} />
                </span>
              </li>,
            ];
          })}
        </ul>
      )}
    </div>
  );
}
