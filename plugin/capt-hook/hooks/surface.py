
from __future__ import annotations

from pathlib import Path
from typing import Any

from captain_hook import (
    Allow,
    Event,
    HookResult,
    Input,
    PostToolUseEvent,
    Tool,
    on,
)
from spawnllm import Binary, BinaryAnswer

from .common import (
    AnswerFileSurfacing,
    CcNotesAvailable,
    entry_kind,
    entry_payload,
    filter_drifted,
    flat_clip,
    git_relative,
    parse_relevant,
    remember_answers,
    render_note_lines,
    repo_root,
    run_cc_notes,
)
from .deferred import defer

RELEVANT_LIMIT = 10

UNRELATED_FLOOR = 0.7
RECORD_LINE_CAP = 500


def unrelated_question(line: str) -> Binary:
    return Binary(
        f'A cc-notes record anchored to this file reads: "{flat_clip(line, RECORD_LINE_CAP)}". Is it plainly unrelated to the file?',
        yes="Its title and match reasons make it plainly unrelated to this file.",
        no="It is related to this file, or might be.",
    )


def plainly_unrelated(answer: object) -> bool:
    match answer:
        case BinaryAnswer(p_yes=p_yes):
            return p_yes >= UNRELATED_FLOOR
    return False


def repo_path(evt: PostToolUseEvent) -> str | None:
    if not evt.file:
        return None
    path = Path(evt.file.path)
    if not path.is_absolute():
        return path.as_posix()
    if (project := evt.ctx.repo_root) is None or (root := repo_root(str(project))) is None:
        return None
    return git_relative(str(path), root)


def unseen_entries(evt: PostToolUseEvent, entries: list[dict[str, Any]], *, scope: str) -> list[dict[str, Any]]:
    fresh = set(evt.ctx.s.unseen([entry_payload(e)["id"] for e in entries], scope=scope))
    return [e for e in entries if entry_payload(e)["id"] in fresh]


def file_surfaced(evt: PostToolUseEvent, out: str | None) -> list[dict[str, Any]]:
    entries = parse_relevant(out)
    if any(entry_kind(e) == "answer" for e in entries):
        surfacing = AnswerFileSurfacing().check(evt)
        entries = [e for e in entries if entry_kind(e) != "answer" or (surfacing and not entry_payload(e).get("stale_at"))]
    return entries[:RELEVANT_LIMIT]


def remember_surfaced_answers(evt: PostToolUseEvent, entries: list[dict[str, Any]]) -> None:
    if answers := [entry_payload(e) for e in entries if entry_kind(e) == "answer"]:
        remember_answers(evt, answers)


def surface_filter(evt: PostToolUseEvent, fresh: list[dict[str, Any]], *, path: str, touched: str) -> list[dict[str, Any]]:
    if len(fresh) <= 1:
        return fresh
    asked = {entry_payload(e)["id"]: unrelated_question(line) for e, line in zip(fresh, render_note_lines(fresh), strict=True)}
    if (decision := evt.decide(f"The agent just {touched} the file {path}.", asked)) is None:
        return fresh
    return [e for e in fresh if not plainly_unrelated(decision.answers[entry_payload(e)["id"]])]


def recall_note_context(evt: PostToolUseEvent) -> HookResult | None:
    if not (path := repo_path(evt)):
        return None
    entries = file_surfaced(evt, run_cc_notes(evt, "relevant", path, "--limit", "0", "--json"))
    fresh = unseen_entries(evt, entries, scope="floated")
    if not fresh:
        return None
    remember_surfaced_answers(evt, fresh)
    return evt.warn(
        f"Durable cc-notes records anchored to {evt.file}; read them before relying on it:",
        *render_note_lines(fresh),
    )


@on(
    Event.PostToolUse,
    only_if=[Tool("Read"), CcNotesAvailable()],
    async_=True,
    tests={
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def float_note_context(evt: PostToolUseEvent) -> None:
    defer(evt, recall_note_context(evt))


def recall_stale_notes(evt: PostToolUseEvent) -> HookResult | None:
    if not (path := repo_path(evt)):
        return None
    entries = file_surfaced(evt, run_cc_notes(evt, "relevant", path, "--attached", "--worktree", "--limit", "0", "--json"))
    drifted = filter_drifted(entries)
    fresh = unseen_entries(evt, drifted, scope="stale")
    if not fresh:
        return None
    picked = surface_filter(evt, fresh, path=path, touched="edited")
    if not picked:
        return None
    remember_surfaced_answers(evt, picked)
    guidance = (
        f"You edited {evt.file}; the cc-notes records below look out of date. "
        "Reconcile each with `cc-notes <kind> verify <id>`, or `edit <id>` it if it changed."
    )
    return evt.warn(guidance, *render_note_lines(picked))


@on(
    Event.PostToolUse,
    only_if=[Tool("Edit|Write|MultiEdit"), CcNotesAvailable()],
    async_=True,
    tests={
        Input(tool="Read", file="m.py"): Allow(),
    },
)
def check_note_staleness(evt: PostToolUseEvent) -> None:
    defer(evt, recall_stale_notes(evt))
