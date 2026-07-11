// Pure presentational helpers for the detail panel: byte and date-time
// formatting, attachment file-type classification (icon glyph, modal viewer
// kind, highlight language), the unix-second trail time-field set, short-id
// abbreviation, and scalar-value text. Unit-tested in format.test.ts.

import type { TrailValue } from "../api";

// ViewerKind selects the attachment modal's viewer: an image or PDF streams
// straight from /api/blob, markdown/code/text are fetched and rendered, and
// binary is download-only.
export type ViewerKind = "image" | "pdf" | "markdown" | "code" | "text" | "binary";

// nowSec is the current time in unix seconds, for relative-time rendering.
export function nowSec(): number {
  return Math.floor(Date.now() / 1000);
}

// formatBytes renders a byte count as a compact human-readable string, matching
// the server's blobMissingMessage humanSize (1024-base, one decimal): "48 B",
// "1.5 KB", "3.4 MB".
export function formatBytes(n: number): string {
  const unit = 1024;
  if (n < unit) return `${n} B`;
  let div = unit;
  let exp = 0;
  for (let m = Math.floor(n / unit); m >= unit; m = Math.floor(m / unit)) {
    div *= unit;
    exp++;
  }
  return `${(n / div).toFixed(1)} ${"KMGTPE"[exp]}B`;
}

// formatDateTime renders a unix-second timestamp as a local date-time.
export function formatDateTime(sec: number): string {
  return new Date(sec * 1000).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// shortId abbreviates a sha-like entity id for a chip, keeping short values whole.
export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

const TIME_FIELDS = new Set([
  "verified_at",
  "stale_at",
  "started_at",
  "closed_at",
  "start_date",
  "end_date",
  "heartbeat_at",
]);

// isTimeField reports whether a trail field carries a unix-second timestamp that
// should render as a date rather than a raw number. created_at/updated_at are
// hidden by the trail and so are not listed.
export function isTimeField(field: string): boolean {
  return TIME_FIELDS.has(field);
}

// scalarText renders a scalar trail value as compact text: null and the empty
// string as ∅, booleans and numbers via String, an object as compact JSON.
export function scalarText(v: TrailValue): string {
  if (v === null) return "∅";
  if (typeof v === "string") return v === "" ? "∅" : v;
  if (typeof v === "boolean") return v ? "true" : "false";
  if (typeof v === "number") return String(v);
  return JSON.stringify(v);
}

function basename(name: string): string {
  const slash = Math.max(name.lastIndexOf("/"), name.lastIndexOf("\\"));
  return slash >= 0 ? name.slice(slash + 1) : name;
}

// fileExt returns a file name's lowercased extension without the dot, or "" when
// it has none (a leading-dot name like ".gitignore" counts as having none).
export function fileExt(name: string): string {
  const base = basename(name);
  const dot = base.lastIndexOf(".");
  if (dot <= 0) return "";
  return base.slice(dot + 1).toLowerCase();
}

const IMAGE = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg"]);
const MARKDOWN = new Set(["md", "markdown"]);

// Extensions that highlight through the shared highlight.js registry, mapped to
// the registered language name. Extensions absent here still render as code (see
// CODE_ONLY) but as plain monospace.
const CODE_LANG: Record<string, string> = {
  go: "go",
  ts: "typescript",
  tsx: "typescript",
  mts: "typescript",
  cts: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  py: "python",
  rs: "rust",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  json: "json",
  jsonc: "json",
  yaml: "yaml",
  yml: "yaml",
  toml: "ini",
  ini: "ini",
  sql: "sql",
  diff: "diff",
  patch: "diff",
  css: "css",
  html: "xml",
  htm: "xml",
  xml: "xml",
  dockerfile: "dockerfile",
};

// Code-like extensions with no registered highlighter: rendered as monospace.
const CODE_ONLY = new Set([
  "c", "h", "cc", "cpp", "hpp", "cxx", "cs", "java", "kt", "kts", "swift", "rb",
  "php", "pl", "pm", "lua", "r", "scala", "clj", "ex", "exs", "erl", "hs", "ml",
  "dart", "jl", "nim", "zig", "vue", "svelte", "scss", "sass", "less", "proto",
  "graphql", "gql", "tf", "hcl", "gradle", "groovy", "bat", "cmd", "ps1", "fish",
  "make", "mk", "cmake",
]);

const TEXT_EXT = new Set([
  "txt", "text", "log", "csv", "tsv", "env", "conf", "cfg", "properties",
  "list", "lock", "sum", "mod",
]);

// Extensionless file names classified by their whole (leading-dot-stripped,
// lowercased) base name.
const SPECIAL: Record<string, ViewerKind> = {
  dockerfile: "code",
  makefile: "text",
  gitignore: "text",
  gitattributes: "text",
  editorconfig: "text",
  npmrc: "text",
  license: "text",
  readme: "text",
  changelog: "text",
  notes: "text",
  todo: "text",
};

// viewerKind classifies an attachment file name into the modal viewer to use.
export function viewerKind(name: string): ViewerKind {
  const ext = fileExt(name);
  if (ext !== "") {
    if (IMAGE.has(ext)) return "image";
    if (ext === "pdf") return "pdf";
    if (MARKDOWN.has(ext)) return "markdown";
    if (ext in CODE_LANG || CODE_ONLY.has(ext)) return "code";
    if (TEXT_EXT.has(ext)) return "text";
    return "binary";
  }
  const base = basename(name).toLowerCase().replace(/^\./, "");
  return SPECIAL[base] ?? "binary";
}

// codeLanguage returns the highlight.js language for a code attachment, or null
// when its extension has no registered highlighter.
export function codeLanguage(name: string): string | null {
  const ext = fileExt(name);
  if (ext !== "" && ext in CODE_LANG) return CODE_LANG[ext] ?? null;
  if (basename(name).toLowerCase().replace(/^\./, "") === "dockerfile") return "dockerfile";
  return null;
}

// extIcon returns an emoji glyph for an attachment, keyed by its viewer kind.
export function extIcon(name: string): string {
  switch (viewerKind(name)) {
    case "image":
      return "\u{1F5BC}";
    case "pdf":
      return "\u{1F4D5}";
    case "markdown":
      return "\u{1F4DD}";
    case "code":
      return "\u{1F5A5}";
    case "text":
      return "\u{1F4C4}";
    default:
      return "\u{1F4CE}";
  }
}
