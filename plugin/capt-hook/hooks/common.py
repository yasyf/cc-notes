
from __future__ import annotations

import json
import os
import shutil
import subprocess
from collections.abc import Sequence
from itertools import takewhile
from pathlib import Path
from typing import Any

from captain_hook import BaseHookEvent, CommandLine, CustomCondition
from captain_hook.state import SeenKeys
from pydantic import BaseModel, Field

NATIVE_TASK_MIRROR_THRESHOLD = 5

SESSION_TASK_CAP = 7
SESSION_ANSWER_CAP = 8
ANSWER_CANDIDATE_LIMIT = 12
ANSWERS_SCOPE = "answers"
ANSWER_METADATA_PREFIXES = ("Question: ", "Options: ", "Notes: ")
NUDGE_MAX_FIRES = 3
LLM_INPUT_CAP = 6000
COMPACT_RESTORE_BUDGET = 7500
COMPACT_DIGEST_BUDGET = 3000
COMPACT_ANSWER_BUDGET = COMPACT_RESTORE_BUDGET - COMPACT_DIGEST_BUDGET - 2

MAX_TITLE_BYTES = 256

RECORD_KINDS = ("note", "doc", "log", "task", "papercut")

MCP_TOOL_PREFIX = "mcp__plugin_cc-notes_cc-notes__"


class RecordVerdict(BaseModel):
    """The router's verdict: whether a freshly written file is durable cc-notes content, and of which kind (one of RECORD_KINDS)."""

    record: bool = False
    kind: str = ""


class SessionAnswers(BaseModel):
    lines: dict[str, str] = Field(default_factory=dict)


def is_single_command(cl: CommandLine) -> bool:
    return len(cl.parts) == 1 and not cl.q.uses_redirect()


CC_NOTES_EXECUTABLES = frozenset({"cc-notes", "ccn"})


CC_NOTES_TOOLS = frozenset(
    {
        "status", "relevant", "sync", "reconcile", "history", "search", "show", "blame",
        "attachment_get", "attachment_path",
        "note_add", "note_edit", "note_rm", "note_show", "note_list", "note_search",
        "note_review", "note_verify", "note_supersede", "note_expire",
        "doc_add", "doc_edit", "doc_rm", "doc_show", "doc_list", "doc_search",
        "doc_review", "doc_verify", "doc_supersede", "doc_expire",
        "answer_add", "answer_edit", "answer_rm", "answer_show", "answer_list", "answer_search",
        "answer_review", "answer_verify", "answer_supersede", "answer_expire",
        "log_add", "log_append", "log_edit", "log_rm", "log_show", "log_list", "log_search",
        "log_entry_list",
        "papercut", "papercut_list", "papercut_show",
        "task_add", "task_edit", "task_show", "task_list", "task_claim", "task_start",
        "task_done", "task_cancel", "task_comment", "task_dep", "task_undep", "task_ready",
        "task_stale", "task_backlog", "task_archived", "task_renew", "task_validate",
        "task_comment_list", "task_link", "task_unlink",
        "task_criterion_add", "task_criterion_rm", "task_criterion_list", "task_criterion_met",
        "task_criterion_failed", "task_criterion_pending", "task_criterion_script",
        "sprint_add", "sprint_edit", "sprint_show", "sprint_list", "sprint_activate",
        "sprint_cancel", "sprint_comment", "sprint_complete",
        "project_add", "project_edit", "project_show", "project_list", "project_activate",
        "project_archive", "project_cancel", "project_comment", "project_complete",
        "runbook_add", "runbook_edit", "runbook_rm", "runbook_show", "runbook_list",
        "runbook_search", "runbook_activate", "runbook_archive", "runbook_comment",
        "runbook_step_add", "runbook_step_edit", "runbook_step_rm", "runbook_step_move", "runbook_step_list",
        "runbook_run_start", "runbook_run_list", "runbook_run_show", "runbook_run_done",
        "runbook_run_skip", "runbook_run_fail", "runbook_run_finish",
        "ledger_add", "ledger_edit", "ledger_rm", "ledger_show", "ledger_list",
        "ledger_search", "ledger_activate", "ledger_archive", "ledger_comment", "ledger_sync",
        "ledger_row_set", "ledger_row_rm", "ledger_row_list",
        "investigation_open", "investigation_list", "investigation_show", "investigation_append",
        "investigation_entry_list",
        "investigation_finding_add", "investigation_finding_edit", "investigation_finding_clear",
        "investigation_finding_confirm", "investigation_finding_rm", "investigation_finding_list",
        "investigation_root_cause", "investigation_fix", "investigation_confirm",
        "investigation_exonerate", "investigation_abandon", "investigation_reopen",
        "investigation_edit", "investigation_search", "investigation_rm",
        "investigation_follow_up", "investigation_supersede",
        "plan_add", "plan_edit", "plan_rm", "plan_show", "plan_list", "plan_search",
        "plan_approve", "plan_start", "plan_reopen", "plan_done", "plan_abandon",
        "plan_comment", "plan_supersede",
    }
)

_MAX_DEPTH = 3

_TOOL_PATH_ALIASES: dict[tuple[str, ...], tuple[str, ...]] = {
    ("investigation", "add"): ("investigation", "open"),
    ("investigation", "history"): ("history",),
}


def _tool_name(tokens: list[str]) -> str:
    canon = [tok.replace("-", "_") for tok in tokens]
    for src, dst in _TOOL_PATH_ALIASES.items():
        if tuple(canon[: len(src)]) == src:
            canon = [*dst, *canon[len(src) :]]
            break
    return "_".join(canon)


def resolve_cli_tool(args: Sequence[str]) -> tuple[str, int] | None:
    tokens = list(takewhile(lambda arg: not arg.startswith("-"), args))
    return next(
        (
            (name, depth)
            for depth in range(min(len(tokens), _MAX_DEPTH), 0, -1)
            if (name := _tool_name(tokens[:depth])) in CC_NOTES_TOOLS
        ),
        None,
    )


def mapped_tool(args: Sequence[str]) -> str | None:
    resolved = resolve_cli_tool(args)
    return resolved[0] if resolved else None


def run_cc_notes(evt: BaseHookEvent, *args: str) -> str | None:
    return evt.ctx.call_cli(["cc-notes", *args], timeout=10, throw=False)


def json_field(out: str | None, key: str) -> str:
    if not out or not out.strip():
        return ""
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return ""
    return parsed.get(key, "") if isinstance(parsed, dict) else ""


def tool_output(evt: BaseHookEvent) -> str:
    response = getattr(evt, "tool_response", None)
    if not response:
        return ""
    return response if isinstance(response, str) else json.dumps(response, default=str)


def clamp_title(title: str, max_bytes: int = MAX_TITLE_BYTES) -> str:
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
    return kind if kind in ("doc", "log", "runbook", "investigation", "plan", "answer") else "note"


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
    if not out or not out.strip():
        return {}
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def status_tasks(report: dict[str, Any], key: str) -> list[dict[str, Any]]:
    rows = report.get(key)
    if not isinstance(rows, list):
        return []
    return [t for t in rows if isinstance(t, dict)]


def stale_leases(report: dict[str, Any]) -> list[dict[str, Any]]:
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


def clip(text: str, limit: int) -> str:
    return text if len(text) <= limit else text[: limit - 1] + "…"


def utf8_len(text: str) -> int:
    return len(text.encode())


def ids_match(a: str, b: str) -> bool:
    return a == b or a.startswith(b) or b.startswith(a)


def render_note_lines(entries: list[dict[str, Any]]) -> list[str]:
    dispatch = {
        "doc": render_doc_line,
        "log": render_log_line,
        "runbook": render_runbook_line,
        "investigation": render_investigation_line,
        "plan": render_plan_line,
        "answer": render_answer_line,
    }
    return [dispatch.get(entry_kind(e), render_note_line)(e) for e in entries]


def drift_suffix(payload: dict[str, Any]) -> str:
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


def answer_question(answer: dict[str, Any]) -> str:
    for line in answer.get("body", "").split("\n")[1:]:
        if line.startswith("Question: "):
            return line.removeprefix("Question: ")
    return answer.get("title", "")


def answer_text(answer: dict[str, Any]) -> str:
    chosen: list[str] = []
    for line in answer.get("body", "").split("\n"):
        if line.startswith(ANSWER_METADATA_PREFIXES):
            break
        chosen.append(line)
    return " / ".join(chosen)


def answer_line(answer: dict[str, Any]) -> str:
    return f"{short_id(answer.get('id', ''))} {answer_question(answer)} → {answer_text(answer)}"


def render_answer_line(entry: dict[str, Any]) -> str:
    answer = entry.get("answer", {})
    line = answer_line(answer)
    if drift := answer.get("drift"):
        line += f" [{drift}]{drift_suffix(answer)}"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    return line


def parse_answers(out: str | None) -> list[dict[str, Any]]:
    return [a for a in parse_tasks(out) if isinstance(a.get("id"), str) and a["id"]]


def durable_answers(evt: BaseHookEvent) -> list[dict[str, Any]]:
    out = run_cc_notes(evt, "answer", "list", "--json", "--label", "scope:durable", "--limit", str(ANSWER_CANDIDATE_LIMIT))
    return [a for a in parse_answers(out) if not a.get("stale_at")]


def unseen_answers(evt: BaseHookEvent, answers: list[dict[str, Any]]) -> list[dict[str, Any]]:
    seen = set(evt.ctx.s.load(SeenKeys).seen.get(ANSWERS_SCOPE, []))
    return [a for a in answers if a["id"] not in seen]


def remember_answers(evt: BaseHookEvent, answers: list[dict[str, Any]]) -> list[str]:
    return remember_answer_lines(evt, {a["id"]: answer_line(a) for a in answers})


def remember_answer_lines(evt: BaseHookEvent, lines: dict[str, str]) -> list[str]:
    fresh = {aid: lines[aid] for aid in evt.ctx.s.unseen(list(lines), scope=ANSWERS_SCOPE)}
    with evt.ctx.s[SessionAnswers].mutate() as state:
        state.lines.update(fresh)
    return list(fresh.values())


def filter_drifted(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [e for e in entries if entry_payload(e).get("drift")]


def render_task_line(task: dict[str, Any]) -> str:
    line = f"{short_id(task.get('id', ''))} {task.get('status', '')} {task.get('title', '')}"
    if assignee := task.get("assignee"):
        line += f" @{assignee}"
    return line


def dedup_tasks(tasks: list[dict[str, Any]]) -> list[dict[str, Any]]:
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
    if not lines:
        return []
    capped = lines[:cap]
    if (extra := len(lines) - cap) > 0:
        capped.append(f"+{extra} more — {more_tail}")
    return capped


def repo_root(path: str) -> str | None:
    resolved = Path(path).expanduser().resolve()
    return next((str(d) for d in (resolved, *resolved.parents) if (d / ".git").exists()), None)


def git_relative(target: str, root: str) -> str | None:
    parent = os.path.realpath(os.path.dirname(target))
    if not os.path.isdir(parent):
        return None
    parts: list[str] = []
    here = parent
    while not os.path.samefile(here, root):
        up = os.path.dirname(here)
        if up == here:
            return None
        parts.append(_entry_name(up, os.path.basename(here)))
        here = up
    return "/".join([*reversed(parts), _entry_name(parent, os.path.basename(target))])


def _entry_name(directory: str, name: str) -> str:
    try:
        want = os.lstat(os.path.join(directory, name))
        entries = os.listdir(directory)
    except OSError:
        return name
    if name in entries:
        return name
    for entry in entries:
        found = os.lstat(os.path.join(directory, entry))
        if (found.st_dev, found.st_ino) == (want.st_dev, want.st_ino):
            return entry
    return name


def in_cc_pool_memory(path: Path) -> bool:
    return ".cc-pool" in path.parts and path.parent.name == "memory"


def record_command(kind: str) -> str:
    return "cc-notes papercut" if kind == "papercut" else f"cc-notes {kind} add"


def render_steal_line(task: dict[str, Any]) -> str:
    reclaim = f"cc-notes task claim {short_id(task.get('id', ''))} --steal"
    return f"{render_task_line(task)} — lease expired, {reclaim}"


class AnswerFileSurfacing(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        try:
            value = evt.ctx.git("config", "--type=bool", "--get", "cc-notes.answers.fileSurfacing")
        except (OSError, subprocess.SubprocessError):
            return False
        return (value or "").strip() == "true"


class CcNotesAvailable(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return shutil.which("cc-notes") is not None


class CcNotesMissing(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return shutil.which("cc-notes") is None


class ManyNativeTasks(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return len(evt.tasks.open) >= NATIVE_TASK_MIRROR_THRESHOLD
