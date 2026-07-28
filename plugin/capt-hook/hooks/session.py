"""Session-start floaters: durable tasks at first prompt, and the missing-binary install nudge."""

from __future__ import annotations

from captain_hook import Event, HookResult, UserPromptSubmitEvent, on

from .common import (
    SESSION_TASK_CAP,
    CcNotesAvailable,
    CcNotesMissing,
    cap_lines,
    dedup_tasks,
    mcp_active,
    parse_status,
    render_steal_line,
    render_task_line,
    run_cc_notes,
    stale_leases,
    status_tasks,
)


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
    max_fires=1,
)
def float_session_tasks(evt: UserPromptSubmitEvent) -> HookResult | None:
    """Float this session's durable tasks once, at the first prompt.

    One `status --json` carries every bucket the floater needs — the current branch's
    tasks, the shared backlog with each row's ready-to-claim verdict, and the in-progress
    leases — so session start costs one fold, not one per bucket.
    """
    report = parse_status(run_cc_notes(evt, "status", "--json"))
    active = mcp_active(evt)
    # An expired lease is the most actionable row on the board: work nobody is driving.
    # It leads, and its id wins the dedup so the steal hint survives.
    stealable = stale_leases(report)
    # sorted() is stable, so ready backlog rows lead in priority order, blocked ones trail.
    backlog = sorted(status_tasks(report, "backlog"), key=lambda t: not t.get("ready"))
    tasks = dedup_tasks(stealable + status_tasks(report, "your_branch") + backlog)
    if not tasks:
        return None
    stale_ids = {t["id"] for t in stealable if t.get("id")}
    lines = [
        render_steal_line(t, mcp=active) if t.get("id") in stale_ids else render_task_line(t)
        for t in tasks
    ]
    if active:
        lede = (
            "Durable cc-notes tasks in play — orient with the status tool "
            "(backlog by readiness, your branch's tasks, expired leases you can steal), "
            "then claim one with the task_claim tool:"
        )
        tail = "orient with the status tool"
    else:
        lede = (
            "Durable cc-notes tasks in play — run `cc-notes status` to orient "
            "(backlog by readiness, your branch's tasks, expired leases you can steal):"
        )
        tail = "run `cc-notes status`"
    return evt.warn(lede, *cap_lines(lines, SESSION_TASK_CAP, tail))


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
            "cc-notes tools (task_add, note_add, doc_add, log_add, papercut, runbook_add, investigation_open; "
            "orient with status), each with a typed schema, rather than shelling out. On macOS, a human "
            "must run `cc-notes package install` before repository provisioning."
        )
    return evt.warn(
        f"cc-notes {version} is installed; its durable task, note, doc, log, papercut, runbook, "
        "and investigation tooling is available. On macOS, a human must run `cc-notes package install` "
        "before repository provisioning."
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
