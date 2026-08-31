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

# The Go CLI hard-rejects a title over 256 UTF-8 bytes (exit 2), and run_cc_notes fails
# closed, so an over-long title would silently stop a capture without a clamp.
MAX_TITLE_BYTES = 256

# The generic record_command() path only — record.py special-cases runbook/investigation/plan; sprint/project never route here.
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


# `ccn` is the shorthand symlink; a path-qualified head (`/usr/bin/cc-notes`, `./cc-notes`)
# matches by basename.
CC_NOTES_EXECUTABLES = frozenset({"cc-notes", "ccn"})

# Leading wrapper tokens skipped (with env's VAR=val assignments) to reach the cc-notes
# token; shell-word heads (`command`/`exec`/…) are rejected upstream by is_plain_argv.
WRAPPER_EXECUTABLES = frozenset({"env", "command"})

# The MCP tool names (internal/mcpserver); a mapped argv resolves only when it lands in this set.
CC_NOTES_TOOLS = frozenset(
    {
        # top-level commands that are themselves tools
        "status", "relevant", "sync", "reconcile", "history", "search", "show", "blame",
        "attachment_get", "attachment_path",
        # note
        "note_add", "note_edit", "note_rm", "note_show", "note_list", "note_search",
        "note_review", "note_verify", "note_supersede", "note_expire",
        # doc
        "doc_add", "doc_edit", "doc_rm", "doc_show", "doc_list", "doc_search",
        "doc_review", "doc_verify", "doc_supersede", "doc_expire",
        # log
        "log_add", "log_append", "log_edit", "log_rm", "log_show", "log_list", "log_search",
        "log_entry_list",
        # papercut
        "papercut", "papercut_list", "papercut_show",
        # task
        "task_add", "task_edit", "task_show", "task_list", "task_claim", "task_start",
        "task_done", "task_cancel", "task_comment", "task_dep", "task_undep", "task_ready",
        "task_stale", "task_backlog", "task_archived", "task_renew", "task_validate",
        "task_comment_list", "task_link", "task_unlink",
        # task criterion
        "task_criterion_add", "task_criterion_rm", "task_criterion_list", "task_criterion_met",
        "task_criterion_failed", "task_criterion_pending", "task_criterion_script",
        # sprint
        "sprint_add", "sprint_edit", "sprint_show", "sprint_list", "sprint_activate",
        "sprint_cancel", "sprint_comment", "sprint_complete",
        # project
        "project_add", "project_edit", "project_show", "project_list", "project_activate",
        "project_archive", "project_cancel", "project_comment", "project_complete",
        # runbook
        "runbook_add", "runbook_edit", "runbook_rm", "runbook_show", "runbook_list",
        "runbook_search", "runbook_activate", "runbook_archive", "runbook_comment",
        # runbook step
        "runbook_step_add", "runbook_step_edit", "runbook_step_rm", "runbook_step_move", "runbook_step_list",
        # runbook run
        "runbook_run_start", "runbook_run_list", "runbook_run_show", "runbook_run_done",
        "runbook_run_skip", "runbook_run_fail", "runbook_run_finish",
        # investigation
        "investigation_open", "investigation_list", "investigation_show", "investigation_append",
        "investigation_entry_list",
        "investigation_finding_add", "investigation_finding_edit", "investigation_finding_clear",
        "investigation_finding_confirm", "investigation_finding_rm", "investigation_finding_list",
        "investigation_root_cause", "investigation_fix", "investigation_confirm",
        "investigation_exonerate", "investigation_abandon", "investigation_reopen",
        "investigation_edit", "investigation_search", "investigation_rm",
        "investigation_follow_up", "investigation_supersede",
        # plan
        "plan_add", "plan_edit", "plan_rm", "plan_show", "plan_list", "plan_search",
        "plan_approve", "plan_start", "plan_reopen", "plan_done", "plan_abandon",
        "plan_comment", "plan_supersede",
    }
)

# The deepest CLI command path is three tokens (e.g. task criterion met, runbook run start).
_MAX_DEPTH = 3

# CLI paths whose MCP tool name is not the underscore-join of the argv: an alias verb, or a
# noun-scoped verb mapping to a global tool. Applied after hyphen canonicalization.
_TOOL_PATH_ALIASES: dict[tuple[str, ...], tuple[str, ...]] = {
    ("investigation", "add"): ("investigation", "open"),
    ("investigation", "history"): ("history",),
}


def _strip_wrappers(tokens: list[str]) -> list[str]:
    """Drop leading env/command wrapper tokens (and env's VAR=val assignments) to reach the command."""
    i = 0
    while i < len(tokens) and os.path.basename(tokens[i]) in WRAPPER_EXECUTABLES:
        i += 1
        while i < len(tokens) and "=" in tokens[i] and not tokens[i].startswith("-"):
            i += 1
    return tokens[i:]


def _canonical_tokens(tokens: list[str]) -> list[str]:
    """Rewrite an argv token path to its MCP command path: hyphens to underscores (`root-cause` ->
    `root_cause`), then an alias/global substitution on the leading tokens."""
    canon = [tok.replace("-", "_") for tok in tokens]
    for src, dst in _TOOL_PATH_ALIASES.items():
        if tuple(canon[: len(src)]) == src:
            return [*dst, *canon[len(src) :]]
    return canon


def mapped_tool(args: list[str]) -> str | None:
    """The MCP tool name for a cc-notes subcommand argv, by longest-prefix match of its leading tokens."""
    tokens: list[str] = []
    for arg in args:
        if arg.startswith("-"):
            break
        tokens.append(arg)
    tokens = _canonical_tokens(tokens)
    for depth in range(min(len(tokens), _MAX_DEPTH), 0, -1):
        name = "_".join(tokens[:depth])
        if name in CC_NOTES_TOOLS:
            return name
    return None


def run_cc_notes(evt: BaseHookEvent, *args: str) -> str | None:
    # Fails closed to None (throw=False) on any subprocess failure so a handler stays
    # silent rather than crashing the hook fire.
    return evt.ctx.call_cli(["cc-notes", *args], timeout=10, throw=False)


def json_field(out: str | None, key: str) -> str:
    """Read one string field off a ``--json`` object payload, "" when absent or unparseable."""
    if not out or not out.strip():
        return ""
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return ""
    return parsed.get(key, "") if isinstance(parsed, dict) else ""


def tool_output(evt: BaseHookEvent) -> str:
    """The tool's response as searchable text.

    A structured response arrives as a dict, and every caller feeds this to a regex, so a
    non-string is rendered as JSON rather than returned raw.
    """
    response = getattr(evt, "tool_response", None)
    if not response:
        return ""
    return response if isinstance(response, str) else json.dumps(response, default=str)


def clamp_title(title: str, max_bytes: int = MAX_TITLE_BYTES) -> str:
    """Clamp ``title`` to at most ``max_bytes`` UTF-8 bytes on a rune boundary.

    Truncating the encoded bytes then decoding with ``errors="ignore"`` drops a
    partial trailing rune, so the result never exceeds the cap and never splits a
    character.
    """
    encoded = title.encode()
    if len(encoded) <= max_bytes:
        return title
    return encoded[:max_bytes].decode(errors="ignore")


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
    return kind if kind in ("doc", "log", "runbook", "investigation", "plan") else "note"


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


def parse_status(out: str | None) -> dict[str, Any]:
    """Parse `cc-notes status --json` into its mapping, or {} when absent or malformed."""
    if not out or not out.strip():
        return {}
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def status_tasks(report: dict[str, Any], key: str) -> list[dict[str, Any]]:
    """The task rows under one status bucket (``backlog``, ``your_branch``), ill-shaped rows dropped."""
    rows = report.get(key)
    if not isinstance(rows, list):
        return []
    return [t for t in rows if isinstance(t, dict)]


def stale_leases(report: dict[str, Any]) -> list[dict[str, Any]]:
    """The in-progress tasks across every assignee whose lease has expired — stealable work."""
    leases: list[dict[str, Any]] = []
    groups = report.get("in_progress")
    if not isinstance(groups, list):
        return leases
    for group in groups:
        if not isinstance(group, dict):
            continue
        tasks = group.get("tasks")
        if not isinstance(tasks, list):
            continue
        leases += [t for t in tasks if isinstance(t, dict) and t.get("stale")]
    return leases


def short_id(full: str) -> str:
    return full[:7]


def ids_match(a: str, b: str) -> bool:
    """Whether two cc-notes id spellings name the same entity.

    Ids resolve by unique prefix, so a stored full id and a short prefix (or the
    reverse) are the same record.
    """
    return a == b or a.startswith(b) or b.startswith(a)


def render_note_lines(entries: list[dict[str, Any]]) -> list[str]:
    dispatch = {
        "doc": render_doc_line,
        "log": render_log_line,
        "runbook": render_runbook_line,
        "investigation": render_investigation_line,
        "plan": render_plan_line,
    }
    return [dispatch.get(entry_kind(e), render_note_line)(e) for e in entries]


def drift_suffix(payload: dict[str, Any]) -> str:
    """The reconciliation context behind a drift verdict: why it was retired, and the
    commit it was last checked against — the diff base for what changed under it."""
    parts = []
    if reason := payload.get("stale_reason"):
        parts.append(f"reason: {reason}")
    if commit := payload.get("verified_commit"):
        parts.append(f"diff against {short_id(commit)}")
    return f" ({'; '.join(parts)})" if parts else ""


def render_note_line(entry: dict[str, Any]) -> str:
    note = entry.get("note", {})
    reasons = ", ".join(entry.get("reasons", []))
    line = f"{short_id(note.get('id', ''))} {note.get('title', '')}"
    if reasons:
        line += f" ({reasons})"
    if drift := note.get("drift"):
        line += f" [{drift}]{drift_suffix(note)}"
    return line


def render_doc_line(entry: dict[str, Any]) -> str:
    doc = entry.get("doc", {})
    short = short_id(doc.get("id", ""))
    line = f"{short} {doc.get('title', '')}"
    if when := doc.get("when"):
        line += f" — when: {when}"
    if drift := doc.get("drift"):
        line += f" [{str(drift).lower()}]{drift_suffix(doc)}"
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


def render_runbook_line(entry: dict[str, Any]) -> str:
    runbook = entry.get("runbook", {})
    short = short_id(runbook.get("id", ""))
    line = f"{short} {runbook.get('title', '')}"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    line += f" — cc-notes runbook show {short}"
    return line


def render_investigation_line(entry: dict[str, Any]) -> str:
    investigation = entry.get("investigation", {})
    short = short_id(investigation.get("id", ""))
    line = f"{short} {investigation.get('title', '')}"
    if status := investigation.get("status"):
        line += f" [{status}]"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    line += f" — cc-notes investigation show {short}"
    return line


def render_plan_line(entry: dict[str, Any]) -> str:
    plan = entry.get("plan", {})
    short = short_id(plan.get("id", ""))
    line = f"{short} {plan.get('title', '')}"
    if status := plan.get("status"):
        line += f" [{status}]"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    line += f" — cc-notes plan show {short}"
    return line


def filter_drifted(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [e for e in entries if entry_payload(e).get("drift")]


def render_task_line(task: dict[str, Any]) -> str:
    line = f"{short_id(task.get('id', ''))} {task.get('status', '')} {task.get('title', '')}"
    if assignee := task.get("assignee"):
        line += f" @{assignee}"
    return line


def render_steal_line(task: dict[str, Any], *, mcp: bool) -> str:
    """Render one expired lease as a summary line ending in the reclaim call."""
    short = short_id(task.get("id", ""))
    reclaim = f"task_claim tool with id={short}, steal=true" if mcp else f"cc-notes task claim {short} --steal"
    return f"{render_task_line(task)} — lease expired, {reclaim}"


def dedup_tasks(tasks: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Drop tasks whose id already appeared, keeping the first occurrence in order.

    The session floater concatenates the status report's buckets, and a task can sit in
    more than one — an expired lease on the current branch is both a stale lease and a
    your-branch row. First occurrence wins, so the steal-hinted rendering survives. Tasks
    carrying no id are never collapsed.
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


def cap_lines(lines: list[str], cap: int, more_tail: str) -> list[str]:
    # more_tail follows the caller's branch (MCP tool vs CLI wording) so the "+N more"
    # overflow line steers to the same surface as the lede, not always `cc-notes status`.
    if not lines:
        return []
    capped = lines[:cap]
    if (extra := len(lines) - cap) > 0:
        capped.append(f"+{extra} more — {more_tail}")
    return capped


def cap_and_render_tasks(tasks: list[dict[str, Any]], cap: int, more_tail: str) -> list[str]:
    return cap_lines([render_task_line(t) for t in tasks], cap, more_tail)


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
            return ['call the papercut tool: body="<one-paragraph complaint>".']
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
