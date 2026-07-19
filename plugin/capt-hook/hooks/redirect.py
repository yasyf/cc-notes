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
    CC_NOTES_EXECUTABLES,
    CC_NOTES_TOOLS,
    CcNotesAvailable,
    NUDGE_MAX_FIRES,
    _strip_wrappers,
    is_plain_argv,
    is_single_command,
    mapped_tool,
    mcp_active,
)

_EXIT_RE = re.compile(r"\s*Exit code (\d+)")


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
