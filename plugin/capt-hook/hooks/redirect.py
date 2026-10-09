
from __future__ import annotations

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
    CcNotesAvailable,
    NUDGE_MAX_FIRES,
    is_single_command,
    mapped_tool,
)


def param_hint(name: str) -> str:
    if name.startswith("task_criterion_"):
        return "key params: id, criterion/text, script"
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


def redirect_target(evt: PostToolUseFailureEvent) -> str | None:
    if evt.error.split()[:3] != ["Exit", "code", "2"] or not is_single_command(evt.cmd.line):
        return None
    calls = [call for call in evt.cmd.calls() if call.name in CC_NOTES_EXECUTABLES]
    return mapped_tool(calls[0].args) if len(calls) == 1 else None


@on(
    Event.PostToolUseFailure,
    only_if=[Tool("Bash"), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="Read", file="m.py"): Allow(),
        Input(command="git push origin main", error="Exit code 2\n ! [rejected]"): Allow(),
        Input(command="cc-notes note show abc", error="Exit code 1\nnote not found"): Allow(),
        Input(command="cc-notes gc", error="Exit code 2\nunknown flag: --oops"): Allow(),
        Input(command="cc-notes status | cat", error="Exit code 2\nunknown flag"): Allow(),
    },
)
def redirect_failed_cc_notes(evt: PostToolUseFailureEvent) -> HookResult | None:
    name = redirect_target(evt)
    if name is None or not evt.ctx.s.once(name, scope="redirect"):
        return None
    return evt.warn(f"Use the typed MCP tool for this command. Call `{name}` instead of the failing `cc-notes` command ({param_hint(name)}).")
