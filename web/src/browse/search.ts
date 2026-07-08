// Hand-rolled fuzzy scorer over a prebuilt entity index. Ranking, best to
// worst: exact title, title prefix, title word-prefix, title substring,
// substring in another field, then subsequence anywhere. Ties break by earlier
// match position, then shorter title, then id — so the order is deterministic.
// The scorer is pure and allocation-light; uFuzzy drops in behind this same
// signature if the ranking ever needs upgrading.

// Span is a [start, end) range into a title, for highlighting the matched run.
export type Span = [number, number];

// SearchTarget is one indexed entity's searchable projection. titleLower and
// bodyLower are precomputed lowercased text — bodyLower is every searchable
// field except the title, joined by newlines — so scoring never re-lowercases.
export interface SearchTarget {
  id: string;
  kind: string;
  title: string;
  titleLower: string;
  bodyLower: string;
}

// SearchHit is one ranked match: score is the tier (higher is better), spans
// highlight the matched run in the title (empty when the match landed in
// another field or scattered too far to highlight).
export interface SearchHit {
  id: string;
  kind: string;
  score: number;
  spans: Span[];
}

const TITLE_EXACT = 6;
const TITLE_PREFIX = 5;
const TITLE_WORD_PREFIX = 4;
const TITLE_SUBSTRING = 3;
const FIELD_SUBSTRING = 2;
const SUBSEQUENCE = 1;

interface Match {
  tier: number;
  pos: number;
  spans: Span[];
}

function isWordChar(c: string): boolean {
  return (c >= "a" && c <= "z") || (c >= "0" && c <= "9");
}

// wordPrefix returns the index of a mid-string word start whose word begins with
// q, or -1. Index 0 is excluded — a whole-title prefix is a stronger tier.
function wordPrefix(hay: string, q: string): number {
  for (let i = 1; i < hay.length; i++) {
    if (!isWordChar(hay[i - 1]) && isWordChar(hay[i]) && hay.startsWith(q, i)) {
      return i;
    }
  }
  return -1;
}

// subseqSpans greedily matches q as a subsequence of hay, returning the matched
// runs merged into contiguous spans, or null when q is not a subsequence.
function subseqSpans(hay: string, q: string): Span[] | null {
  const spans: Span[] = [];
  let qi = 0;
  for (let i = 0; i < hay.length && qi < q.length; i++) {
    if (hay[i] === q[qi]) {
      const last = spans[spans.length - 1];
      if (last !== undefined && last[1] === i) last[1] = i + 1;
      else spans.push([i, i + 1]);
      qi++;
    }
  }
  return qi === q.length ? spans : null;
}

function rank(t: SearchTarget, q: string): Match | null {
  const title = t.titleLower;
  if (title === q) return { tier: TITLE_EXACT, pos: 0, spans: [[0, t.title.length]] };
  if (title.startsWith(q)) return { tier: TITLE_PREFIX, pos: 0, spans: [[0, q.length]] };

  const wp = wordPrefix(title, q);
  if (wp >= 0) return { tier: TITLE_WORD_PREFIX, pos: wp, spans: [[wp, wp + q.length]] };

  const si = title.indexOf(q);
  if (si >= 0) return { tier: TITLE_SUBSTRING, pos: si, spans: [[si, si + q.length]] };

  const bi = t.bodyLower.indexOf(q);
  if (bi >= 0) return { tier: FIELD_SUBSTRING, pos: bi, spans: [] };

  if (subseqSpans(`${title}\n${t.bodyLower}`, q) !== null) {
    const titleSpans = subseqSpans(title, q);
    return { tier: SUBSEQUENCE, pos: titleSpans?.[0]?.[0] ?? 0, spans: titleSpans ?? [] };
  }
  return null;
}

interface Scored {
  hit: SearchHit;
  pos: number;
  titleLen: number;
}

// search ranks targets against a query, returning matches best-first. An empty
// or whitespace-only query returns no results.
export function search(targets: readonly SearchTarget[], raw: string): SearchHit[] {
  const q = raw.trim().toLowerCase();
  if (q === "") return [];

  const scored: Scored[] = [];
  for (const t of targets) {
    const m = rank(t, q);
    if (m === null) continue;
    scored.push({
      hit: { id: t.id, kind: t.kind, score: m.tier, spans: m.spans },
      pos: m.pos,
      titleLen: t.titleLower.length,
    });
  }

  scored.sort(
    (a, b) =>
      b.hit.score - a.hit.score ||
      a.pos - b.pos ||
      a.titleLen - b.titleLen ||
      (a.hit.id < b.hit.id ? -1 : a.hit.id > b.hit.id ? 1 : 0),
  );
  return scored.map((s) => s.hit);
}
