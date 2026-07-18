"""Redirect a failed cc-notes Bash usage error (exit 2) to the typed MCP tool that maps to it.

A Bash command that exits nonzero dispatches as ``PostToolUseFailure``: the envelope carries
the combined output in ``evt.error``, headed by an ``Exit code N`` line (a success dispatches
as ``PostToolUse`` with a ``tool_response`` instead, so this never fires on it). cc-notes maps
every usage error — unknown flag, unknown command, wrong arity, mutually-exclusive flags — to
exit 2, so on such a failure this names the MCP tool the argv maps to, MCP-active sessions only.
"""

from __future__ import annotations

import os
import re

from captain_hook import (
    Allow,
    Event,
    HookResult,
    Input,
    PostToolUseFailureEvent,
    Tool,
    on,
)

from .common import (
    CcNotesAvailable,
    NUDGE_MAX_FIRES,
    is_plain_argv,
    is_single_command,
    mcp_active,
)

# `ccn` is the shorthand symlink; a path-qualified head (`/usr/bin/cc-notes`, `./cc-notes`)
# matches by basename.
CC_NOTES_EXECUTABLES = frozenset({"cc-notes", "ccn"})

# Leading wrapper tokens skipped (with env's VAR=val assignments) to reach the cc-notes
# token; shell-word heads (`command`/`exec`/…) are rejected upstream by is_plain_argv.
WRAPPER_EXECUTABLES = frozenset({"env", "command"})

_EXIT_RE = re.compile(r"\s*Exit code (\d+)")

# The MCP tool names (internal/mcpserver); a mapped argv redirects only when it lands in this set.
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
        # papercut
        "papercut", "papercut_list",
        # task
        "task_add", "task_edit", "task_show", "task_list", "task_claim", "task_start",
        "task_done", "task_cancel", "task_comment", "task_dep", "task_undep", "task_ready",
        "task_stale", "task_backlog", "task_archived", "task_renew", "task_validate",
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
        "investigation_finding_add", "investigation_finding_edit", "investigation_finding_clear",
        "investigation_finding_confirm", "investigation_finding_rm", "investigation_finding_list",
        "investigation_root_cause", "investigation_fix", "investigation_confirm",
        "investigation_exonerate", "investigation_abandon", "investigation_reopen",
        "investigation_edit", "investigation_search", "investigation_rm",
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


def _canonical_tokens(tokens: list[str]) -> list[str]:
    """Rewrite an argv token path to its MCP command path: hyphens to underscores (`root-cause` ->
    `root_cause`), then an alias/global substitution on the leading tokens."""
    canon = [tok.replace("-", "_") for tok in tokens]
    for src, dst in _TOOL_PATH_ALIASES.items():
        if tuple(canon[: len(src)]) == src:
            return [*dst, *canon[len(src) :]]
    return canon


def param_hint(name: str) -> str:
    """The key-params clause for a tool family — param names verified against internal/mcpserver/tools_*.go."""
    if name.startswith("task_criterion_"):
        return "key params: task, crit/text, script"
    if name.startswith("runbook_step_"):
        return "key params: id, text, command, placement (first/last/before/after)"
    if name.startswith("runbook_run_"):
        return "key params: id, step, note"
    if name.endswith("_comment"):
        return "key params: id, body"
    if name == "investigation_finding_add":
        return "key params: id, text"
    if name == "investigation_finding_edit":
        return "key params: id, finding, text"
    if name.endswith("_add"):
        return "key params: title, body, anchors (commits/paths/dirs/branches), labels"
    if name.endswith("_edit"):
        return "key params: id plus the fields to change (title, body, anchors, labels)"
    return "named params in place of the CLI flags"


def _exit_code(error: str) -> int | None:
    """The numeric exit code Claude Code heads a Bash failure envelope's error text with, or None."""
    m = _EXIT_RE.match(error)
    return int(m.group(1)) if m else None


def _strip_wrappers(tokens: list[str]) -> list[str]:
    """Drop leading env/command wrapper tokens (and env's VAR=val assignments) to reach the command."""
    i = 0
    while i < len(tokens) and os.path.basename(tokens[i]) in WRAPPER_EXECUTABLES:
        i += 1
        while i < len(tokens) and "=" in tokens[i] and not tokens[i].startswith("-"):
            i += 1
    return tokens[i:]


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


def redirect_target(evt: PostToolUseFailureEvent) -> str | None:
    """The MCP tool a failed cc-notes Bash call maps to, or None when it should stay silent."""
    if _exit_code(evt.error) != 2:
        return None
    cl = evt.command_line
    if cl is None or not is_single_command(cl):
        return None
    if cl.primary is None or not is_plain_argv(cl):
        return None
    argv = _strip_wrappers([cl.primary.executable, *cl.primary.args])
    if not argv or os.path.basename(argv[0]) not in CC_NOTES_EXECUTABLES:
        return None
    return mapped_tool(argv[1:])


@on(
    Event.PostToolUseFailure,
    only_if=[Tool("Bash"), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="Read", file="m.py"): Allow(),  # not a Bash tool — the gate misses
        Input(command="git push origin main", error="Exit code 2\n ! [rejected]"): Allow(),  # not cc-notes
        Input(command="cc-notes note show abc", error="Exit code 1\nnote not found"): Allow(),  # exit 1 is a runtime error, not usage
        Input(command="cc-notes gc", error="Exit code 2\nunknown flag: --oops"): Allow(),  # operator cmd -> no tool
        Input(command='cc-notes note add "oops', error="Exit code 2\nunknown flag"): Allow(),  # unterminated quote -> not plain argv
        # The exit-2 firing path is mcp_active-gated (and self-adoption makes a live MCP marker
        # nondeterministic here), so the dispatch-level FIRE proof lives in tests/test_cc_notes.py.
    },
)
def redirect_failed_cc_notes(evt: PostToolUseFailureEvent) -> HookResult | None:
    """On a cc-notes Bash usage error (exit 2), name the typed MCP tool it maps to — MCP-active sessions only."""
    name = redirect_target(evt)
    if name is None:
        return None
    if not mcp_active(evt):
        return None
    if not evt.ctx.s.once(name, scope="redirect"):
        return None
    return evt.warn(
        f"this cc-notes call failed with a usage error — prefer the MCP tool `{name}` "
        f"(typed schema; {param_hint(name)})."
    )
