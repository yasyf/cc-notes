"""Shared helpers, conditions, and record vocabulary for the cc-notes hook pack."""

from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess
from pathlib import Path
from typing import Any

from captain_hook import BaseHookEvent, CommandLine, CustomCondition
from pydantic import BaseModel

NATIVE_TASK_MIRROR_THRESHOLD = 5

# Max durable tasks the session-start floater shows before a "+K more" tail.
SESSION_TASK_CAP = 7
# Per-session fire cap for advisories that aren't once-per-session and don't self-dedup.
NUDGE_MAX_FIRES = 3
# Cap on body/diff/plan text handed to a small-model classifier.
LLM_INPUT_CAP = 6000

RECORD_KINDS = ("note", "doc", "log", "task", "papercut")

# The Claude Code plugin surfaces the cc-notes MCP server's tools under this name prefix.
MCP_TOOL_PREFIX = "mcp__plugin_cc-notes_cc-notes__"

# Shell words the parser accepts as an executable but that bash treats as a keyword
# or builtin (`time`, `exec`, `eval`, …). A line headed by one is not a plain argv:
# what runs is not the word the parser reports, so the approval bails.
SHELL_WORD_EXECUTABLES = frozenset({"time", "command", "builtin", "exec", "eval", "source", "."})


class RecordVerdict(BaseModel):
    """The router's verdict: whether a freshly written file is durable cc-notes content, as which kind.

    ``record`` defaults False so a degenerate or empty model parse fails closed to
    silence. ``kind`` is one of :data:`RECORD_KINDS`; the remaining fields seed the
    suggested ``cc-notes <kind> add`` command and are only meaningful when ``record``
    is true.
    """

    record: bool = False
    kind: str = ""
    title: str = ""
    when: str = ""
    area: str = ""
    reasoning: str = ""


class McpActive(BaseModel):
    """Session-durable flag: a cc-notes MCP tool has fired this session.

    The fast path for :func:`mcp_active` — flipped once by the MCP-tool recorder in
    ``record.py`` and read on every later hook fire, so once the server is known
    active the marker scan is skipped.
    """

    active: bool = False


def is_single_command(cl: CommandLine) -> bool:
    """Report whether the line is one command — no pipe, redirect, or ``&&``/``;`` chain."""
    return len(cl.parts) == 1 and not cl.q.uses_redirect()


def is_plain_argv(cl: CommandLine) -> bool:
    """Report whether the raw line is exactly the primary command's argv.

    The cc-notes approval trusts the parsed executable only when the raw text *is*
    that argv: no env-assignment prefix (what runs is not the parsed word), no
    shell-keyword head (:data:`SHELL_WORD_EXECUTABLES`), and the raw text
    word-splits to exactly the parsed executable + args. Structure the parser
    folded out of the argv (a bare command substitution, a redirect) fails that
    comparison and bails to the dialog.
    """
    if cl.primary.env or cl.primary.executable in SHELL_WORD_EXECUTABLES:
        return False
    try:
        words = shlex.split(cl.raw)
    except ValueError:
        return False
    return words == [cl.primary.executable, *cl.primary.args]


def run_cc_notes(evt: BaseHookEvent, *args: str) -> str | None:
    # Fails closed to None (throw=False) on any subprocess failure so a handler stays
    # silent rather than crashing the hook fire.
    return evt.ctx.call_cli(["cc-notes", *args], timeout=10, throw=False)


def parse_relevant(out: str | None) -> list[dict[str, Any]]:
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []
    return [e for e in parsed if well_shaped_entry(e)]


def entry_kind(entry: dict[str, Any]) -> str:
    kind = entry.get("kind")
    return kind if kind in ("doc", "log") else "note"


def entry_payload(entry: dict[str, Any]) -> dict[str, Any]:
    payload = entry.get(entry_kind(entry))
    return payload if isinstance(payload, dict) else {}


def well_shaped_entry(entry: Any) -> bool:
    if not isinstance(entry, dict):
        return False
    payload = entry.get(entry_kind(entry))
    return isinstance(payload, dict) and isinstance(payload.get("id"), str) and bool(payload["id"])


def parse_tasks(out: str | None) -> list[dict[str, Any]]:
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []
    return [t for t in parsed if isinstance(t, dict)]


def short_id(full: str) -> str:
    return full[:7]


def render_note_lines(entries: list[dict[str, Any]]) -> list[str]:
    dispatch = {"doc": render_doc_line, "log": render_log_line}
    return [dispatch.get(entry_kind(e), render_note_line)(e) for e in entries]


def render_note_line(entry: dict[str, Any]) -> str:
    note = entry.get("note", {})
    reasons = ", ".join(entry.get("reasons", []))
    line = f"{short_id(note.get('id', ''))} {note.get('title', '')}"
    if reasons:
        line += f" ({reasons})"
    if drift := note.get("drift"):
        line += f" [{drift}]"
    return line


def render_doc_line(entry: dict[str, Any]) -> str:
    doc = entry.get("doc", {})
    short = short_id(doc.get("id", ""))
    line = f"{short} {doc.get('title', '')}"
    if when := doc.get("when"):
        line += f" — when: {when}"
    if drift := doc.get("drift"):
        line += f" [{str(drift).lower()}]"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    line += f" — cc-notes doc show {short}"
    return line


def render_log_line(entry: dict[str, Any]) -> str:
    log = entry.get("log", {})
    short = short_id(log.get("id", ""))
    line = f"{short} {log.get('title', '')}"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    line += f" — cc-notes log show {short}"
    return line


def filter_drifted(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [e for e in entries if entry_payload(e).get("drift")]


def render_task_line(task: dict[str, Any]) -> str:
    line = f"{short_id(task.get('id', ''))} {task.get('status', '')} {task.get('title', '')}"
    if assignee := task.get("assignee"):
        line += f" @{assignee}"
    return line


def dedup_tasks(tasks: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Drop tasks whose id already appeared, keeping the first occurrence in order.

    The session floater concatenates the current-branch list with the shared backlog;
    on an unresolvable detached HEAD the branch read itself degrades to the backlog set,
    so the two `task list` reads can return the same task. Tasks carrying no id are never
    collapsed.
    """
    seen: set[str] = set()
    out: list[dict[str, Any]] = []
    for task in tasks:
        tid = task.get("id")
        if tid:
            if tid in seen:
                continue
            seen.add(tid)
        out.append(task)
    return out


def cap_and_render_tasks(tasks: list[dict[str, Any]], cap: int, more_tail: str) -> list[str]:
    # more_tail follows the caller's branch (MCP tool vs CLI wording) so the "+N more"
    # overflow line steers to the same surface as the lede, not always `cc-notes status`.
    if not tasks:
        return []
    lines = [render_task_line(t) for t in tasks[:cap]]
    if (extra := len(tasks) - cap) > 0:
        lines.append(f"+{extra} more — {more_tail}")
    return lines


def in_cc_pool_memory(path: Path) -> bool:
    # The mirror owns the cc-pool memory tree, so the advisory record-router excludes it.
    # Deliberately broader than MemoryWrite: the whole tree is the mirror's domain.
    return ".cc-pool" in path.parts and path.parent.name == "memory"


def record_command(kind: str, title: str, when: str, area: str, *, mcp: bool = False) -> list[str]:
    # A log takes no body at creation — `log add` opens the journal and `log append`
    # grows it — so it renders as two lines; the others are a single `add`. With the MCP
    # server active, the whole surface is tool calls: the body param carries the content,
    # so there is no checkout buffer or stdin.
    if mcp:
        dir_arg = f', dirs=["{area}"]' if area and area != "." else ""
        if kind == "doc":
            return [
                f'call the doc_add tool: title="{title}", when="{when}"{dir_arg}, and the FULL markdown guidance as the body param (no scratch file — the body lives in the record; use the attach param for artifact files).'
            ]
        if kind == "log":
            return [
                f'call the log_add tool (title="{title}"{dir_arg}) to open the journal, then the log_append tool once per entry.'
            ]
        if kind == "task":
            return [
                f'call the task_add tool: title="{title}", criteria=["<how to verify it is done>"] (backlog=true if any agent should be able to claim it; no_validation_criteria=true only when acceptance genuinely cannot be stated).'
            ]
        if kind == "papercut":
            return ['call the papercut tool: text="<one-paragraph complaint>".']
        return [f'call the note_add tool: title="{title}"{dir_arg}, with the fact as the body param.']
    dir_flag = f" --dir {area}" if area and area != "." else ""
    if kind == "doc":
        return [
            f'p=$(cc-notes doc add "{title}" --checkout --when "{when}"{dir_flag})   # a prefilled buffer to write the body into',
            'cc-notes doc add --apply "$p"   # after writing the full body into $p, below the frontmatter',
            f'# short body? cc-notes doc add "{title}" --when "{when}"{dir_flag} --body - reads it from stdin',
        ]
    if kind == "log":
        return [
            f'cc-notes log add "{title}"{dir_flag}',
            "cc-notes log append <id>   # then add the chronology one entry at a time",
        ]
    if kind == "task":
        return [
            f'cc-notes task add "{title}" --criterion "<how to verify it is done>"   # --backlog if shared; --no-validation-criteria only when acceptance cannot be stated'
        ]
    if kind == "papercut":
        return ['cc-notes papercut "<one-paragraph complaint>"']
    return [f'cc-notes note add "{title}"{dir_flag} --body -']


def mcp_active(evt: BaseHookEvent) -> bool:
    """Whether the cc-notes MCP server is serving this repo — for nudge WORDING only.

    Best-effort and a pure function of the event's real session/marker state. A wrong
    answer only mis-words a teaching hint and never changes whether a handler fires, so
    this is called inside handler bodies, never in a condition. True when a cc-notes MCP
    tool call flipped the session flag this session, or when a live liveness marker sits
    under the repo's git common dir; outside a git repo, False.
    """
    return _mcp_session_flag(evt) or _mcp_marker_live(evt)


def _mcp_session_flag(evt: BaseHookEvent) -> bool:
    try:
        return evt.ctx.s.load(McpActive).active
    except Exception:
        return False


def _mcp_marker_live(evt: BaseHookEvent) -> bool:
    try:
        common_dir = evt.ctx.git("rev-parse", "--path-format=absolute", "--git-common-dir")
    except (subprocess.SubprocessError, OSError):
        return False  # git hung or errored — the best-effort probe degrades to inactive
    if not common_dir or not common_dir.strip():
        return False
    mcp_dir = Path(common_dir.strip()) / "cc-notes" / "mcp"
    try:
        markers = list(mcp_dir.glob("*.json"))
    except OSError:
        return False
    return any(_marker_pid_alive(m) for m in markers)


def _marker_pid_alive(marker: Path) -> bool:
    try:
        pid = json.loads(marker.read_text(encoding="utf-8")).get("pid")
    except (OSError, ValueError, AttributeError, RecursionError):
        return False  # a foreign/corrupt marker skips this one, never aborting the sibling scan
    if not isinstance(pid, int) or pid <= 0:
        return False
    try:
        os.kill(pid, 0)  # signal 0 probes liveness only — it never signals the process
    except ProcessLookupError:
        return False
    except PermissionError:
        return True  # a live process we do not own (EPERM)
    except (OSError, OverflowError):
        return False  # OverflowError: a foreign marker's out-of-range pid — never crash the probe
    return True


class CcNotesMcpToolCall(CustomCondition):
    """Matches a PostToolUse for any cc-notes MCP server tool, by name prefix."""

    def check(self, evt: BaseHookEvent) -> bool:
        return bool(evt.tool_name) and evt.tool_name.startswith(MCP_TOOL_PREFIX)


class CcNotesAvailable(CustomCondition):
    """Matches whenever the ``cc-notes`` binary resolves on PATH."""

    def check(self, evt: BaseHookEvent) -> bool:
        return shutil.which("cc-notes") is not None


class CcNotesMissing(CustomCondition):
    """Matches whenever the ``cc-notes`` binary does NOT resolve on PATH."""

    def check(self, evt: BaseHookEvent) -> bool:
        return shutil.which("cc-notes") is None


class ManyNativeTasks(CustomCondition):
    """Matches when the session is carrying enough open native tasks to look durable."""

    def check(self, evt: BaseHookEvent) -> bool:
        return len(evt.tasks.open) >= NATIVE_TASK_MIRROR_THRESHOLD
