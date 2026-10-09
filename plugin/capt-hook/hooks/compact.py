
from __future__ import annotations

import re
from collections.abc import Sequence
from itertools import takewhile

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
from captain_hook.cmd import Call
from pydantic import BaseModel, Field

from .common import (
    CC_NOTES_EXECUTABLES,
    COMPACT_DIGEST_BUDGET,
    MCP_TOOL_PREFIX,
    agent_key,
    fit_lines,
    ids_match,
    is_single_command,
    resolve_cli_tool,
    short_id,
    tool_output,
    utf8_len,
)

def cli_calls(evt: BaseHookEvent) -> list[Call]:
    return [call for call in evt.cmd.calls() if call.name in CC_NOTES_EXECUTABLES]


MAX_ENTRIES = 30

_CREATE_VERBS = frozenset({"add", "open"})
_LIST_VERBS = frozenset({"list", "search", "review", "ready", "stale", "backlog", "archived"})
_TOPLEVEL_IGNORED = frozenset({"status", "relevant", "sync", "reconcile", "history", "blame", "search"})

_MINTED_ID_RE = re.compile(r'"id"\s*:\s*"([^"]+)"')
_SHOW_TITLE_RE = re.compile(r"(?m)^title:\s*(.*)$")


class TouchedEntity(BaseModel):
    id: str
    kind: str
    agent: str = "main"
    title: str = ""
    verbs: list[str] = Field(default_factory=list)
    seq: int = 0


class TouchedEntities(BaseModel):
    entries: list[TouchedEntity] = Field(default_factory=list)
    next_seq: int = 0


def _classify(name: str) -> str:
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
    if name == "show":
        return "entity"
    if name == "papercut":
        return "papercut"
    return name.split("_", 1)[0]


def _minted_id(output: str, *, prefer_json: bool) -> str | None:
    if not output:
        return None
    if prefer_json and (m := _MINTED_ID_RE.search(output)):
        return m.group(1)
    stripped = output.strip()
    return stripped.split()[0] if stripped else None


def _cli_command(args: Sequence[str]) -> tuple[str, list[str]] | None:
    if (resolved := resolve_cli_tool(args)) is None:
        return None
    name, depth = resolved
    return name, list(takewhile(lambda arg: not arg.startswith("-"), args[depth:]))


def _mcp_id(raw: dict[str, object]) -> str | None:
    value = raw.get("id")
    return value if isinstance(value, str) and value else None


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
    return positionals[0] if verb == "create" and positionals else ""


class _Touch:

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


def _response_text(evt: BaseHookEvent) -> str:
    response = getattr(evt, "tool_response", None)
    if isinstance(response, dict) and isinstance(response.get("stdout"), str):
        return response["stdout"]
    if isinstance(response, list):
        return "\n".join(block["text"] for block in response if isinstance(block, dict) and isinstance(block.get("text"), str))
    return tool_output(evt)


def _touches(evt: BaseHookEvent) -> list[_Touch]:
    name = evt.tool_name or ""
    output = _response_text(evt)
    if name.startswith(MCP_TOOL_PREFIX):
        touch = _resolve(name[len(MCP_TOOL_PREFIX) :], "mcp", dict(evt.input.raw), [], output, can_mint=True, prefer_json=True)
        return [touch] if touch else []
    single = is_single_command(evt.cmd.line)
    out: list[_Touch] = []
    for call in cli_calls(evt):
        if (parsed := _cli_command(call.args)) is None:
            continue
        tool, positionals = parsed
        can_mint = single and "--checkout" not in call.args
        if touch := _resolve(tool, "cli", {}, positionals, output, can_mint=can_mint, prefer_json="--json" in call.args):
            out.append(touch)
    return out


def _is_read_only(entry: TouchedEntity) -> bool:
    return "create" not in entry.verbs and "edit" not in entry.verbs


def _evict(state: TouchedEntities, agent: str) -> None:
    while len(owned := [e for e in state.entries if e.agent == agent]) > MAX_ENTRIES:
        pool = [e for e in owned if _is_read_only(e)] or owned
        state.entries.remove(min(pool, key=lambda e: e.seq))


def _apply(state: TouchedEntities, touch: _Touch, agent: str) -> None:
    if touch.verb == "remove":
        state.entries = [e for e in state.entries if not ids_match(e.id, touch.id)]
        return
    existing = next((e for e in state.entries if e.agent == agent and ids_match(e.id, touch.id)), None)
    if existing is None:
        state.entries.append(TouchedEntity(id=touch.id, kind=touch.kind, agent=agent, title=touch.title, verbs=[touch.verb], seq=state.next_seq))
    else:
        if len(touch.id) > len(existing.id):
            existing.id = touch.id
        if existing.kind == "entity" and touch.kind != "entity":
            existing.kind = touch.kind
        if touch.title:
            existing.title = touch.title
        if touch.verb not in existing.verbs:
            existing.verbs.append(touch.verb)
        existing.seq = state.next_seq
    state.next_seq += 1
    _evict(state, agent)


class CcNotesEntityCall(CustomCondition):
    def check(self, evt: BaseHookEvent) -> bool:
        return (evt.tool_name or "").startswith(MCP_TOOL_PREFIX) or bool(cli_calls(evt))


@on(
    Event.PostToolUse,
    only_if=[CcNotesEntityCall()],
    tests={
        Input(tool="mcp__plugin_cc-notes_cc-notes__note_add", tool_input={"title": "x"}): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
        Input(command="cc-notes note list"): Allow(),
    },
)
def record_touched_entities(evt: PostToolUseEvent) -> HookResult | None:
    with evt.ctx.s[TouchedEntities].mutate() as state:
        for touch in _touches(evt):
            _apply(state, touch, agent_key(evt))
    return None


class CompactResume(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return isinstance(evt, SessionStartEvent) and evt.source == "compact"


DIGEST_HEADER = "Context was just compacted. cc-notes records you touched this session:"


def _digest_line(entry: TouchedEntity) -> str:
    return f"{entry.kind} {short_id(entry.id)} {entry.title}".rstrip()


def _digest_pointer(hidden: int) -> str:
    more = f"+{hidden} more. " if hidden else ""
    return f"{more}`cc-notes show <id>` re-opens one."


def _digest(entries: list[TouchedEntity]) -> list[str]:
    reserved = utf8_len(DIGEST_HEADER) + utf8_len(_digest_pointer(len(entries))) + 2
    kept = fit_lines([_digest_line(e) for e in entries], COMPACT_DIGEST_BUDGET - reserved)
    return [DIGEST_HEADER, *kept, _digest_pointer(len(entries) - len(kept))]


@on(
    Event.SessionStart,
    only_if=[CompactResume()],
    tests={
        Input(source="startup"): Allow(),
        Input(source="compact"): Allow(),
    },
)
def restore_after_compact(evt: SessionStartEvent) -> HookResult | None:
    agent = agent_key(evt)
    if not (entries := [e for e in evt.ctx.s.load(TouchedEntities).entries if e.agent == agent]):
        return None
    return evt.warn(*_digest(sorted(entries, key=lambda e: e.seq, reverse=True)))
