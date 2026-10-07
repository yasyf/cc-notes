
from __future__ import annotations

from captain_hook import Event, HookResult, Input, UserPromptSubmitEvent, Warn, on

from .common import (
    SESSION_ANSWER_CAP,
    SESSION_TASK_CAP,
    CcNotesAvailable,
    CcNotesMissing,
    branch_answers,
    cap_lines,
    current_branch,
    dedup_tasks,
    parse_status,
    remember_answers,
    render_steal_line,
    render_task_line,
    run_cc_notes,
    stale_leases,
    status_tasks,
    unseen_answers,
)


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
    max_fires=1,
)
def float_session_tasks(evt: UserPromptSubmitEvent) -> HookResult | None:
    report = parse_status(run_cc_notes(evt, "status", "--json", "--tasks"))
    stealable = stale_leases(report)
    backlog = sorted(status_tasks(report, "backlog"), key=lambda t: not t.get("ready"))
    tasks = dedup_tasks(stealable + status_tasks(report, "your_branch") + backlog)
    if not tasks:
        return None
    stale_ids = {t["id"] for t in stealable if t.get("id")}
    lines = [render_steal_line(t) if t.get("id") in stale_ids else render_task_line(t) for t in tasks]
    lede = "Durable cc-notes tasks are in play. Run `cc-notes status` to orient, then `cc-notes task claim <id>` to take one:"
    return evt.warn(lede, *cap_lines(lines, SESSION_TASK_CAP, "run `cc-notes status`"))


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
)
def float_session_answers(evt: UserPromptSubmitEvent) -> HookResult | None:
    if not evt.ctx.s.once("first", scope="session-answers"):
        return None
    fresh = unseen_answers(evt, branch_answers(evt))
    if not fresh:
        return None
    lines = remember_answers(evt, fresh[:SESSION_ANSWER_CAP])
    lede = "Durable answers already given on this branch; honor them instead of asking again. `cc-notes answer show <id>` has the full record:"
    if (extra := len(fresh) - SESSION_ANSWER_CAP) > 0:
        lines.append(f"+{extra} more — run `cc-notes answer list --label scope:durable --branch {current_branch(evt)}`")
    return evt.warn(lede, *lines)


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
    max_fires=1,
    tests={
        Input(prompt="keep going"): Warn(pattern="cc-notes task add"),
    },
)
def nudge_cc_notes_installed(evt: UserPromptSubmitEvent) -> HookResult:
    return evt.warn("cc-notes is installed. Record durable work with `cc-notes task add`, `cc-notes note add`, or `cc-notes doc add`.")


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesMissing()],
    max_fires=1,
)
def nudge_cc_notes_missing(evt: UserPromptSubmitEvent) -> HookResult:
    return evt.warn("The `cc-notes` binary is not on PATH, so every cc-notes nudge stays silent. Run `brew install yasyf/tap/cc-notes`.")
