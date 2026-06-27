"""Commit/claim/merge workflow nudges with auto-sync and auto-reconcile side-effects."""

from __future__ import annotations

import subprocess

from captain_hook import (
    Allow,
    Event,
    HookResult,
    Input,
    PostToolUseEvent,
    Prompt,
    Tool,
    Warn,
    nudge,
    on,
)
from captain_hook.types import Command

from .common import (
    CcNotesAvailable,
    ManyNativeTasks,
    NATIVE_TASK_MIRROR_THRESHOLD,
    NUDGE_MAX_FIRES,
    RecordVerdict,
    record_command,
    run_cc_notes,
)

GIT_MERGE_PULL = r"^git\s+(?:-\S+\s+)*(?:merge|pull)\b"
GIT_COMMIT = r"^git\s+(?:-\S+\s+)*commit\b"
CC_NOTES_CLAIM = r"^cc-notes\s+task\s+(?:claim|start)\b"


def should_autosync(evt: PostToolUseEvent) -> bool:
    # At most one sync per turn even when commit+claim co-occur. A scoped once-key,
    # isolated from the record router's shared fired_this_turn slot and the per-sha/per-plan once scopes.
    return evt.ctx.s.once(str(len(evt.ctx.t) - len(evt.ctx.turn)), scope="autosync")


def do_sync(evt: PostToolUseEvent) -> str | None:
    # call_cli(throw=False) discards stderr, so throw and inspect it: a no-remote/offline repo is
    # benign (silent); a real push rejection must surface or the agent believes its refs shipped.
    try:
        evt.ctx.call_cli(["cc-notes", "sync"], timeout=15)
    except subprocess.CalledProcessError as e:
        return None if "remote not configured" in (e.stderr or "") else "cc-notes sync failed — run `cc-notes sync` to retry."
    except (OSError, subprocess.SubprocessError):
        return None  # timeout / missing-or-unexecutable binary: silent (FileNotFoundError is an OSError, not SubprocessError)
    return "Synced cc-notes refs."


def auto_sync(evt: PostToolUseEvent) -> str | None:
    return do_sync(evt) if should_autosync(evt) else None


def auto_reconcile(evt: PostToolUseEvent) -> str | None:
    # Reconcile is local + idempotent (run_cc_notes is fail-closed); a detached HEAD or reconcile
    # error is silent. The push after is unconditional — a merge must share its carried tasks — and
    # do_sync's own line rides along (its success confirm or fail-closed retry warn), so a failed
    # push is never swallowed as "synced".
    branch = (evt.ctx.git("rev-parse", "--abbrev-ref", "HEAD") or "").strip()
    if not branch or branch == "HEAD" or run_cc_notes(evt, "reconcile", "--into", branch) is None:
        return None
    should_autosync(evt)  # claim the turn token so a same-turn commit/claim won't double-sync
    reconciled = f"Reconciled merged tasks onto {branch}."
    return f"{reconciled} {synced}" if (synced := do_sync(evt)) else reconciled


COMMIT_DECISION_SYSTEM = (
    "An agent just landed a git commit. Decide whether the change embodies a durable DECISION worth "
    "capturing as a cc-notes record — a design choice, a tradeoff, a non-obvious rationale a future "
    "agent would want explained — as opposed to routine, self-explanatory work (a rename, a "
    "dependency bump, a formatting pass, a mechanical fix) that the diff and message already cover.\n"
    "\n"
    "Set record=false for routine or self-explanatory commits — that is most commits. Only a commit "
    "that encodes a decision worth preserving records. When record=true choose the kind: note for a "
    "single durable fact or decision (one verifiable claim), doc for longer living rationale a future "
    "agent should read before touching this area. Return title (short), when (for a doc, the 'read "
    "this when…' trigger; empty for a note), area (the repo directory, or '.'), reasoning (one line)."
)


def commit_decision(evt: PostToolUseEvent) -> list[str]:
    try:
        diff = evt.ctx.diff(commit="HEAD")
    except Exception:
        return []
    if not diff:
        return []
    prompt = (
        Prompt()
        .system(COMMIT_DECISION_SYSTEM)
        .context("commit", diff)
        .ask("Does this commit encode a durable decision worth a cc-notes note or doc?")
    )
    try:
        verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    except Exception:
        # Deliberate fail-closed exception: a classifier error drops only the suggestion.
        return []
    if not verdict.record or verdict.kind not in ("note", "doc"):
        return []
    title = verdict.title or "the decision behind this commit"
    return [
        f"This commit encodes a durable {verdict.kind} ({verdict.reasoning}) — capture it:",
        *record_command(verdict.kind, title, verdict.when, verdict.area),
    ]


@on(
    Event.PostToolUse,
    only_if=[Command(GIT_COMMIT), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="git status"): Allow(),
    },
)
def nudge_commit_record(evt: PostToolUseEvent) -> HookResult | None:
    """After a commit, remind to link to its task, route any durable decision, and sync the new refs."""
    # Per-HEAD-sha dedup BEFORE any side-effect: each commit is judged once, an amend
    # (new sha) gets a fresh look, a sha-less git failure still fires the reminder.
    try:
        sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    except Exception:
        sha = ""
    if sha and not evt.ctx.s.once(sha, scope="commit"):
        return None
    return evt.warn(
        "Commit landed. Link it to its task with a `cc-task: <id>` trailer (queryable via "
        "`git log --grep`, `cc-notes blame <sha>`, and `cc-notes history <id>`).",
        *commit_decision(evt),
        *([line] if (line := auto_sync(evt)) else []),
    )


@on(
    Event.PostToolUse,
    only_if=[Command(GIT_MERGE_PULL), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={Input(command="git status"): Allow(), Input(command="git log --no-merges"): Allow()},
)
def reconcile_after_merge(evt: PostToolUseEvent) -> HookResult | None:
    """After a merge/pull, carry the merged branch's open tasks onto this branch and sync."""
    return evt.warn(line) if (line := auto_reconcile(evt)) else None


@on(
    Event.PostToolUse,
    only_if=[Command(CC_NOTES_CLAIM), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={Input(command="cc-notes task list"): Allow()},
)
def nudge_claim(evt: PostToolUseEvent) -> HookResult | None:
    """After claiming/starting a task, teach lease upkeep and sync the new claim."""
    return evt.warn(
        "You hold a lease now. `cc-notes task renew <id>` on long silent stretches, "
        "`cc-notes task done <id>` when finished. A crashed hold whose lease expired is "
        "reclaimable with `cc-notes task claim <id> --steal`.",
        *([line] if (line := auto_sync(evt)) else []),
    )


nudge(
    "Your native task list is getting large. Native tasks vanish at session end "
    "and are private to this agent — mirror any that are durable or cross-agent "
    "into `cc-notes task add` (`--backlog` if it's shared work anyone can claim). "
    "Keep the purely in-session steps as native todos.",
    only_if=[Tool("TaskCreate"), ManyNativeTasks(), CcNotesAvailable()],
    events=Event.PostToolUse,
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(
            tool="TaskCreate",
            tasks=[{"id": str(i), "subject": f"t{i}", "status": "pending"} for i in range(NATIVE_TASK_MIRROR_THRESHOLD)],
        ): Warn(pattern="cc-notes task add"),
        Input(
            tool="TaskCreate",
            tasks=[{"id": "1", "subject": "t1", "status": "in_progress"}],
        ): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
