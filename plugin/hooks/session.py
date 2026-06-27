"""Session-start floaters: durable tasks at first prompt, and the missing-binary install nudge."""

from __future__ import annotations

from captain_hook import Event, HookResult, UserPromptSubmitEvent, on

from .common import (
    SESSION_TASK_CAP,
    CcNotesAvailable,
    CcNotesMissing,
    cap_and_render_tasks,
    parse_tasks,
    run_cc_notes,
)


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
    max_fires=1,
)
def float_session_tasks(evt: UserPromptSubmitEvent) -> HookResult | None:
    """Float this session's durable tasks once, at the first prompt."""
    branch_tasks = parse_tasks(run_cc_notes(evt, "task", "list", "--json"))
    tasks = list(branch_tasks)
    if len(tasks) < SESSION_TASK_CAP:
        tasks.extend(parse_tasks(run_cc_notes(evt, "task", "list", "--backlog", "--json")))
    lines = cap_and_render_tasks(tasks, SESSION_TASK_CAP)
    if not lines:
        return None
    return evt.warn(
        "Durable cc-notes tasks in play — run `cc-notes status` to orient "
        "(shared backlog, your branch's tasks, who holds what, notes needing review):",
        *lines,
    )


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesMissing()],
    max_fires=1,
)
def prompt_install_cc_notes(evt: UserPromptSubmitEvent) -> HookResult | None:
    """Once per session, surface that the cc-notes binary is missing and how to install it."""
    return evt.warn(
        "cc-notes hooks are enabled in this repo but the `cc-notes` binary isn't on "
        "PATH, so every cc-notes nudge stays silent (the plugin's auto-install didn't "
        "land one). Install it to enable them:",
        "brew install yasyf/tap/cc-notes",
        "# or: curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh",
    )
