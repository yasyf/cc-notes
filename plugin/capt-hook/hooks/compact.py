"""Compact-survival capture and restore for cc-notes entities.

A silent PostToolUse tracker records every cc-notes entity a session touches — creates,
edits/transitions, and explicit shows — into session state as it happens. A SessionStart
restorer, firing only on a compaction (`source == "compact"`), reads that state back and
injects a digest into the fresh window: fresh full ``cc-notes show`` output per entity when
few were touched, otherwise one recency-ordered pointer line each. Search/list/status result
ids never count as a touch.
"""

from __future__ import annotations

import os
import re
import shutil

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    HookResult,
    Input,
    PostToolUseEvent,
    SessionStartEvent,
    on,
)
from pydantic import BaseModel, Field

from .common import (
    CC_NOTES_EXECUTABLES,
    MCP_TOOL_PREFIX,
    _MAX_DEPTH,
    _canonical_tokens,
    _strip_wrappers,
    is_single_command,
    mapped_tool,
    mcp_active,
    run_cc_notes,
    short_id,
)

# Entities restored fresh (full `cc-notes show` each) at or below this count; above it, lean pointers.
FULL_SHOW_CAP = 8
# Pointer-mode digest caps the list, then a "+N more" tail steers to `cc-notes status`.
POINTER_CAP = 30
# Session-state ceiling; oldest pure-read entries evict first, then oldest overall.
MAX_ENTRIES = 100

# Verb-token vocabularies the classifier derives create/read/remove/ignored/edit from — never a
# hand-enumerated per-tool list, so a new cc-notes command classifies itself by its name shape.
_CREATE_VERBS = frozenset({"add", "open"})
_LIST_VERBS = frozenset({"list", "search", "review", "ready", "stale", "backlog", "archived"})
_TOPLEVEL_IGNORED = frozenset({"status", "relevant", "sync", "reconcile", "history", "blame", "search"})

_MINTED_ID_RE = re.compile(r'"id"\s*:\s*"([^"]+)"')
_SHOW_TITLE_RE = re.compile(r"(?m)^title:\s*(.*)$")


class TouchedEntity(BaseModel):
    """One cc-notes entity this session touched: its id, kind, best-known title, verbs, and recency seq."""

    id: str
    kind: str
    title: str = ""
    verbs: list[str] = Field(default_factory=list)
    seq: int = 0


class TouchedEntities(BaseModel):
    """Session-durable set of touched entities, plus the monotonically increasing recency counter."""

    entries: list[TouchedEntity] = Field(default_factory=list)
    next_seq: int = 0


def _classify(name: str) -> str:
    """The touch class of an MCP tool name: create, read, remove, ignored, or edit.

    Structural, not enumerated: a two-token ``*_add``/``*_open`` (or bare ``papercut``) creates; a
    trailing ``show`` reads; a two-token ``*_rm`` removes; a list-family verb, a top-level non-entity
    op, or any ``attachment_*`` is ignored; everything else in the tool set mutates an entity (edit).
    Three-token adds/rms (``task_criterion_add``, ``investigation_finding_rm``) fall through to edit —
    they mutate a parent entity rather than birthing or deleting a top-level one.
    """
    tokens = name.split("_")
    last = tokens[-1]
    if name == "papercut" or (last in _CREATE_VERBS and len(tokens) == 2):
        return "create"
    if name == "show" or last == "show":
        return "read"
    if last == "rm" and len(tokens) == 2:
        return "remove"
    if last in _LIST_VERBS or name in _TOPLEVEL_IGNORED or tokens[0] == "attachment":
        return "ignored"
    return "edit"


def _kind(name: str) -> str:
    """The entity kind an MCP tool name touches — its noun prefix, ``entity`` for bare ``show``."""
    if name == "show":
        return "entity"
    if name == "papercut":
        return "papercut"
    return name.split("_", 1)[0]


def _tool_output(evt: BaseHookEvent) -> str:
    return getattr(evt, "tool_response", None) or ""


def _ids_match(a: str, b: str) -> bool:
    # cc-notes ids resolve by unique prefix, so a stored full id and a short prefix (or the reverse)
    # name the same entity.
    return a == b or a.startswith(b) or b.startswith(a)


def _minted_id(output: str, *, prefer_json: bool) -> str | None:
    # A lean CLI line's title may hold a `{"id":"..."}` fragment, so only MCP/`--json` output runs the
    # regex; a lean line takes its first whitespace token.
    if not output:
        return None
    if prefer_json and (m := _MINTED_ID_RE.search(output)):
        return m.group(1)
    stripped = output.strip()
    return stripped.split()[0] if stripped else None


def _cli_command(rest: list[str]) -> tuple[str, list[str]] | None:
    """The MCP tool a cc-notes CLI argv (sans the ``cc-notes`` head) maps to, plus its leading positionals.

    Positionals run from the command path to the first flag, so a value-flag's argument
    (``log append -m "x" abc``) never leaks in as the id.
    """
    name = mapped_tool(rest)
    if name is None:
        return None
    tokens: list[str] = []
    for arg in rest:
        if arg.startswith("-"):
            break
        tokens.append(arg)
    # Depth in RAW argv tokens: canonicalize each prefix so a length-changing alias
    # (`investigation history` -> `history`) can't misalign the positionals.
    depth = next(
        (d for d in range(min(len(tokens), _MAX_DEPTH), 0, -1) if "_".join(_canonical_tokens(tokens[:d])) == name),
        len(tokens),
    )
    positionals: list[str] = []
    for arg in rest[depth:]:
        if arg.startswith("-"):
            break
        positionals.append(arg)
    return name, positionals


def _mcp_id(raw: dict[str, object]) -> str | None:
    # Most tools carry the id under `id`; task_criterion_* and task_validate carry it under `task`.
    for key in ("id", "task"):
        value = raw.get(key)
        if isinstance(value, str) and value:
            return value
    return None


def _resolve_id(
    verb: str, surface: str, raw: dict[str, object], positionals: list[str], output: str, *, can_mint: bool, prefer_json: bool
) -> str | None:
    if verb == "create":
        return _minted_id(output, prefer_json=prefer_json) if can_mint else None
    if surface == "mcp":
        return _mcp_id(raw)
    return positionals[0] if positionals else None


def _resolve_title(verb: str, surface: str, raw: dict[str, object], positionals: list[str], output: str) -> str:
    if verb == "read":
        m = _SHOW_TITLE_RE.search(output)
        return m.group(1).strip() if m else ""
    if surface == "mcp":
        title = raw.get("title")
        return title if isinstance(title, str) else ""
    # A CLI create's first positional is its title; a CLI edit/remove's first positional is the id, so
    # they contribute no title and lean on a create/show touch (or a prior title) via the merge below.
    return positionals[0] if verb == "create" and positionals else ""


class _Touch:
    """A single resolved touch: the class, entity id, kind, and best title from this one tool call."""

    __slots__ = ("verb", "id", "kind", "title")

    def __init__(self, verb: str, ident: str, kind: str, title: str) -> None:
        self.verb = verb
        self.id = ident
        self.kind = kind
        self.title = title


def _resolve(
    tool: str, surface: str, raw: dict[str, object], positionals: list[str], output: str, *, can_mint: bool, prefer_json: bool
) -> _Touch | None:
    verb = _classify(tool)
    if verb == "ignored":
        return None
    ident = _resolve_id(verb, surface, raw, positionals, output, can_mint=can_mint, prefer_json=prefer_json)
    if not ident:
        return None
    return _Touch(verb, ident, _kind(tool), _resolve_title(verb, surface, raw, positionals, output))


def _touches(evt: BaseHookEvent) -> list[_Touch]:
    name = evt.tool_name or ""
    output = _tool_output(evt)
    if name.startswith(MCP_TOOL_PREFIX):
        touch = _resolve(name[len(MCP_TOOL_PREFIX) :], "mcp", dict(evt._tool_input), [], output, can_mint=True, prefer_json=True)
        return [touch] if touch else []
    line = evt.command_line
    if line is None:
        return []
    single = is_single_command(line)
    out: list[_Touch] = []
    for cmd in line.commands:
        argv = _strip_wrappers([cmd.executable, *cmd.args])
        if not argv or os.path.basename(argv[0]) not in CC_NOTES_EXECUTABLES:
            continue
        if (parsed := _cli_command(argv[1:])) is None:
            continue
        tool, positionals = parsed
        # A create id is minted from the shared tool_response, so only a single-command line can attribute
        # it; `--checkout` merely writes a template and prints its PATH (the entity is born at `--apply`).
        can_mint = single and "--checkout" not in argv
        if touch := _resolve(tool, "cli", {}, positionals, output, can_mint=can_mint, prefer_json="--json" in argv):
            out.append(touch)
    return out


def _is_read_only(entry: TouchedEntity) -> bool:
    return "create" not in entry.verbs and "edit" not in entry.verbs


def _evict(state: TouchedEntities) -> None:
    while len(state.entries) > MAX_ENTRIES:
        pool = [e for e in state.entries if _is_read_only(e)] or state.entries
        state.entries.remove(min(pool, key=lambda e: e.seq))


def _apply(state: TouchedEntities, touch: _Touch) -> None:
    existing = next((e for e in state.entries if _ids_match(e.id, touch.id)), None)
    if touch.verb == "remove":
        if existing is not None:
            state.entries.remove(existing)
        return
    if existing is None:
        state.entries.append(TouchedEntity(id=touch.id, kind=touch.kind, title=touch.title, verbs=[touch.verb], seq=state.next_seq))
    else:
        if len(touch.id) > len(existing.id):
            existing.id = touch.id
        if existing.kind == "entity" and touch.kind != "entity":
            existing.kind = touch.kind  # a bare-`show` placeholder upgrades to a per-kind touch's concrete kind
        if touch.title:
            existing.title = touch.title
        if touch.verb not in existing.verbs:
            existing.verbs.append(touch.verb)
        existing.seq = state.next_seq
    state.next_seq += 1
    _evict(state)


class CcNotesEntityCall(CustomCondition):
    """Matches a cc-notes entity call — an MCP cc-notes tool, or a ``cc-notes``/``ccn`` leg of any Bash command line."""

    def check(self, evt: BaseHookEvent) -> bool:
        name = evt.tool_name or ""
        if name.startswith(MCP_TOOL_PREFIX):
            return True
        line = evt.command_line
        if line is None:
            return False
        return any(
            (argv := _strip_wrappers([cmd.executable, *cmd.args])) and os.path.basename(argv[0]) in CC_NOTES_EXECUTABLES
            for cmd in line.commands
        )


@on(
    Event.PostToolUse,
    only_if=[CcNotesEntityCall()],
    tests={
        Input(tool="mcp__plugin_cc-notes_cc-notes__note_add", tool_input={"title": "x"}): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),  # not a cc-notes call — the condition misses
        Input(command="cc-notes note list"): Allow(),  # a list read records nothing
    },
)
def record_touched_entities(evt: PostToolUseEvent) -> HookResult | None:
    """Record every cc-notes entity a tool call touched into session state — silent, never blocks."""
    try:
        with evt.ctx.s[TouchedEntities].mutate() as state:
            for touch in _touches(evt):
                _apply(state, touch)
    except Exception:
        # A store or parse error must never disturb the tool call (record_mcp_active precedent).
        pass
    return None


class CompactResume(CustomCondition):
    """Matches a SessionStart fired by a compaction — the only source whose digest is injected."""

    def check(self, evt: BaseHookEvent) -> bool:
        return isinstance(evt, SessionStartEvent) and evt.source == "compact"


def _load(evt: BaseHookEvent) -> TouchedEntities:
    try:
        return evt.ctx.s.load(TouchedEntities)
    except Exception:
        return TouchedEntities()


def _touch_label(verbs: list[str]) -> str:
    if "create" in verbs:
        return "created"
    if "edit" in verbs:
        return "edited"
    return "read"


def _pointer_line(entry: TouchedEntity) -> str:
    title = f" {entry.title}" if entry.title else ""
    return f"{entry.kind} {short_id(entry.id)}{title} ({_touch_label(entry.verbs)})"


def _closing_hint(evt: BaseHookEvent) -> str:
    if mcp_active(evt):
        return "Re-open any of these with the note_show/task_show/… tools, or the show tool."
    return "Re-open any of these with `cc-notes show <id>`."


def _digest(evt: BaseHookEvent, entries: list[TouchedEntity]) -> list[str]:
    parts = ["Context was just compacted. Durable cc-notes records this session touched:"]
    if len(entries) <= FULL_SHOW_CAP and shutil.which("cc-notes") is not None:
        for entry in entries:
            body = run_cc_notes(evt, "show", entry.id)
            if body and body.strip():
                parts.append(f"[{entry.kind} {short_id(entry.id)} · {_touch_label(entry.verbs)}]\n{body.strip()}")
            else:
                parts.append(_pointer_line(entry))
    else:
        parts.extend(_pointer_line(entry) for entry in entries[:POINTER_CAP])
        if (extra := len(entries) - POINTER_CAP) > 0:
            parts.append(f"+{extra} more — cc-notes status to orient")
    parts.append(_closing_hint(evt))
    return parts


@on(
    Event.SessionStart,
    only_if=[CompactResume()],
    tests={
        Input(source="startup"): Allow(),  # only a compaction injects
        Input(source="compact"): Allow(),  # null store -> empty state -> silent
    },
)
def restore_after_compact(evt: SessionStartEvent) -> HookResult | None:
    """After a compaction, inject a digest of the cc-notes entities this session touched."""
    state = _load(evt)
    if not state.entries:
        return None
    entries = sorted(state.entries, key=lambda e: e.seq, reverse=True)
    return evt.warn(*_digest(evt, entries))
