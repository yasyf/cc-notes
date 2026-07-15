"""Session-start floaters: durable tasks at first prompt, and the missing-binary install nudge."""

from __future__ import annotations

from captain_hook import Event, HookResult, UserPromptSubmitEvent, on

from .common import (
    SESSION_TASK_CAP,
    CcNotesAvailable,
    CcNotesMissing,
    cap_and_render_tasks,
    dedup_tasks,
    mcp_active,
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
    tasks = parse_tasks(run_cc_notes(evt, "task", "list", "--json"))
    if len(tasks) < SESSION_TASK_CAP:
        tasks += parse_tasks(run_cc_notes(evt, "task", "list", "--backlog", "--json"))
    # On an unresolvable detached HEAD the branch read degrades to the backlog set, so the
    # two reads overlap — dedup by id (branch first, first occurrence wins) before capping.
    tasks = dedup_tasks(tasks)
    if not tasks:
        return None
    active = mcp_active(evt)
    if active:
        lede = (
            "Durable cc-notes tasks in play — orient with the status tool "
            "(shared backlog, your branch's tasks, who holds what, notes needing review), "
            "then claim one with the task_claim tool:"
        )
        tail = "orient with the status tool"
    else:
        lede = (
            "Durable cc-notes tasks in play — run `cc-notes status` to orient "
            "(shared backlog, your branch's tasks, who holds what, notes needing review):"
        )
        tail = "run `cc-notes status`"
    return evt.warn(lede, *cap_and_render_tasks(tasks, SESSION_TASK_CAP, tail))


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
)
def announce_cc_notes_available(evt: UserPromptSubmitEvent) -> HookResult | None:
    """Once per session, surface that cc-notes is installed and its durable tooling is available.

    The SessionStart bootstrap (bootstrap.py) does the install/upgrade under async dispatch, whose
    output the harness drops — so the version line the agent reads lands here on the first prompt.
    ``ctx.s.once`` claims the shot only when the line actually emits, so a transient version read that
    comes back empty doesn't burn the announcement.
    """
    version = (run_cc_notes(evt, "version") or "").strip()
    if not version or not evt.ctx.s.once("announce", scope="availability"):
        return None
    if mcp_active(evt):
        return evt.warn(
            f"cc-notes {version} is installed and its MCP server is active — record durable work with the "
            "cc-notes tools (task_add, note_add, doc_add, log_add, papercut; orient with status), each with "
            "a typed schema, rather than shelling out."
        )
    return evt.warn(f"cc-notes {version} is installed; its durable task, note, doc, log, and papercut tooling is available.")


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
