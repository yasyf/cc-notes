"""capt-hook enforcement hooks for repos that have adopted cc-notes.

These are advisory NUDGES, never gates: cc-notes *complements* Claude's native
task tracking, so the hooks only ever warn — they never block a tool call.

Every nudge is gated behind :class:`CcNotesAdopted`, which keeps them completely
silent unless the repo actually uses cc-notes (binary on PATH *and*
``refs/cc-notes/*`` present). Installed into a repo that doesn't use cc-notes,
this file is inert.

The teaching goal is the native-vs-durable distinction:

  * native ``TaskCreate``/``TaskUpdate`` — ephemeral, this-session-only, private
    scratchpad for decomposing the current task into in-session steps;
  * ``cc-notes task`` — durable, git-synced, GLOBAL work. The id is global; a
    task's branch is a mutable attribute (the shared backlog is the unassigned
    queue, ``Branch == ""``, visible to every agent), and ``task start`` claims
    it under a lease while moving it onto your current branch;
  * ``cc-notes note`` — durable, git-synced, repo-global decisions & facts, born
    verified against HEAD with first-class drift/verify/supersede.
"""

from __future__ import annotations

import json
import shutil
import subprocess
from typing import Any

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    Input,
    PostToolUseEvent,
    Tool,
    UserPromptSubmitEvent,
    Warn,
    nudge,
    on,
    session_state,
)
from captain_hook.types import Command
from pydantic import BaseModel

NATIVE_TASK_MIRROR_THRESHOLD = 5

# SESSION_TASK_CAP bounds how many durable tasks the session-start floater shows
# before collapsing the rest into a "+K more" tail pointing at `cc-notes status`.
SESSION_TASK_CAP = 7

GIT_MERGE_PULL = r"^git\s+(?:-\S+\s+)*(?:merge|pull)\b"
GIT_COMMIT = r"^git\s+(?:-\S+\s+)*commit\b"
CC_NOTES_CLAIM = r"^cc-notes\s+task\s+(?:claim|start)\b"


def run_cc_notes(evt: BaseHookEvent, *args: str) -> str | None:
    """Run ``cc-notes`` with ``args`` in the project dir, returning stdout or None.

    Every way the subprocess can fail — a missing binary, a present-but-not-executable
    binary, a non-zero exit, a timeout — falls closed to ``None`` so a handler stays
    silent rather than crashing the hook fire. ``OSError`` covers ``FileNotFoundError``
    and ``PermissionError``; ``SubprocessError`` covers ``CalledProcessError`` and
    ``TimeoutExpired``.
    """
    try:
        return evt.ctx.call_cli(["cc-notes", *args], timeout=10)
    except (OSError, subprocess.SubprocessError):
        return None


def parse_relevant(out: str | None) -> list[dict[str, Any]]:
    """Parse ``cc-notes relevant --json`` stdout into its list of ranked entries.

    Returns an empty list for ``None``, blank output, malformed JSON, or any
    shape that is not a JSON array — the callers treat "nothing parsed" and
    "nothing relevant" identically. Entries that are not a dict carrying a
    ``note`` dict with a non-empty string ``id`` are dropped, so the downstream
    render/dedup/persist helpers can index ``entry["note"]["id"]`` without
    guarding every ill-shaped element.
    """
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []
    return [e for e in parsed if _well_shaped_entry(e)]


def _well_shaped_entry(entry: Any) -> bool:
    """Report whether ``entry`` is a relevance entry with an indexable ``note.id``."""
    return (
        isinstance(entry, dict)
        and isinstance(note := entry.get("note"), dict)
        and isinstance(note.get("id"), str)
        and bool(note["id"])
    )


def parse_tasks(out: str | None) -> list[dict[str, Any]]:
    """Parse ``cc-notes task list --json`` stdout into its flat list of task DTOs.

    Returns an empty list for ``None``, blank output, malformed JSON, or any
    non-array shape. Non-dict array elements are dropped so ``render_task_line``
    can call ``.get`` on every survivor.
    """
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []
    return [t for t in parsed if isinstance(t, dict)]


def short_id(full: str) -> str:
    """Return the 7-char short id cc-notes renders, the first 7 hex chars of an id."""
    return full[:7]


def render_note_lines(entries: list[dict[str, Any]]) -> list[str]:
    """Render relevance entries as ``<short> <title> (reasons) [DRIFT]`` lines.

    The drift suffix is appended only when the note's ``drift`` verdict is
    non-null; a fresh note (null/absent drift) carries no suffix.
    """
    lines: list[str] = []
    for entry in entries:
        note = entry.get("note", {})
        reasons = ", ".join(entry.get("reasons", []))
        line = f"{short_id(note.get('id', ''))} {note.get('title', '')}"
        if reasons:
            line += f" ({reasons})"
        if drift := note.get("drift"):
            line += f" [{drift}]"
        lines.append(line)
    return lines


def dedup_against_ids(entries: list[dict[str, Any]], seen: list[str]) -> list[dict[str, Any]]:
    """Drop relevance entries whose note id is already in ``seen``, preserving order."""
    seen_set = set(seen)
    return [e for e in entries if e.get("note", {}).get("id") not in seen_set]


def filter_drifted(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Keep only relevance entries whose note has a non-null ``drift`` verdict."""
    return [e for e in entries if e.get("note", {}).get("drift")]


def render_task_line(task: dict[str, Any]) -> str:
    """Render one task DTO as ``<short> <status> <title>`` plus ``@assignee`` when set."""
    line = f"{short_id(task.get('id', ''))} {task.get('status', '')} {task.get('title', '')}"
    if assignee := task.get("assignee"):
        line += f" @{assignee}"
    return line


def cap_and_render_tasks(tasks: list[dict[str, Any]], cap: int) -> list[str]:
    """Render up to ``cap`` task lines, with a ``+K more`` tail when truncated.

    Returns an empty list for no tasks. When ``len(tasks) > cap`` the first
    ``cap`` are rendered and a final ``+K more — run `cc-notes status``` line
    accounts for the remainder.
    """
    if not tasks:
        return []
    lines = [render_task_line(t) for t in tasks[:cap]]
    if (extra := len(tasks) - cap) > 0:
        lines.append(f"+{extra} more — run `cc-notes status`")
    return lines


class CcNotesAdopted(CustomCondition):
    """Matches only in repos that actually use cc-notes.

    Both signals are required so the nudges stay silent in repos that merely have
    the hook installed but never ran ``cc-notes init``:

      1. the ``cc-notes`` binary resolves on PATH, and
      2. the repository carries at least one ``refs/cc-notes/*`` ref.

    The ref probe shells out via ``evt.ctx.git`` (cwd = the project dir), which
    returns ``None`` outside a git work tree, so the condition fails closed.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        if shutil.which("cc-notes") is None:
            return False
        refs = evt.ctx.git("for-each-ref", "--count=1", "refs/cc-notes/")
        return bool(refs and refs.strip())


class ManyNativeTasks(CustomCondition):
    """Matches when the session is carrying enough open native tasks to look durable.

    A large, growing native task list is the drift signal: some of those items are
    almost certainly cross-session or cross-agent work that belongs in cc-notes.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        return len(evt.tasks.open) >= NATIVE_TASK_MIRROR_THRESHOLD


@session_state
class FloatedNotes(BaseModel):
    """Per-session record of note ids already floated as Read-time context."""

    ids: list[str] = []


@session_state
class StaleChecked(BaseModel):
    """Per-session record of note ids already surfaced as edit-time staleness prompts."""

    ids: list[str] = []


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAdopted()],
    max_fires=1,
)
def float_session_tasks(evt: UserPromptSubmitEvent) -> Any:
    """Float this session's durable tasks once, at the first prompt.

    Lists the current branch's open/in-progress tasks, topping up from the
    shared backlog while room remains under :data:`SESSION_TASK_CAP`, and
    renders the combined top ``SESSION_TASK_CAP`` as orientation pointing at
    ``cc-notes status``. Silent when cc-notes is absent or there are no tasks.
    """
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
    Event.PostToolUse,
    only_if=[Tool("Read"), CcNotesAdopted()],
    tests={
        # Gate keeps the floater silent without cc-notes; non-Read tools never match.
        Input(tool="Read", file="internal/store/store.go"): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def float_note_context(evt: PostToolUseEvent) -> Any:
    """Float notes relevant to a freshly read file, once per note per session.

    Runs ``cc-notes relevant <path> --json``, drops ids already floated this
    session, and on anything fresh persists the union and warns with each note's
    title, reasons, and drift flag. Silent when nothing remains or the command
    empties.
    """
    if not evt.file:
        return None
    entries = parse_relevant(run_cc_notes(evt, "relevant", str(evt.file), "--json"))
    floated = evt.ctx.session.load(FloatedNotes)
    fresh = dedup_against_ids(entries, floated.ids)
    if not fresh:
        return None
    floated.ids = floated.ids + [e["note"]["id"] for e in fresh]
    evt.ctx.session[FloatedNotes].set(floated)
    return evt.warn(
        f"Notes relevant to {evt.file} (durable, git-synced cc-notes context):",
        *render_note_lines(fresh),
    )


@on(
    Event.PostToolUse,
    only_if=[Tool("Edit|Write|MultiEdit"), CcNotesAdopted()],
    tests={
        # Gate keeps the check silent without cc-notes; reads never match.
        Input(tool="Edit", file="internal/store/store.go"): Allow(),
        Input(tool="Write", file="internal/store/store.go"): Allow(),
        Input(tool="MultiEdit", file="internal/store/store.go"): Allow(),
        Input(tool="Read", file="m.py"): Allow(),
    },
)
def check_note_staleness(evt: PostToolUseEvent) -> Any:
    """Prompt reconciliation when an edit touches a path anchored by a drifted note.

    Runs ``cc-notes relevant <path> --attached --worktree --json``, keeps only
    notes whose drift verdict is non-null, drops ids already surfaced this
    session, and on anything new persists the union and warns naming the file
    and the verify/edit/supersede/expire next steps. Silent otherwise.
    """
    if not evt.file:
        return None
    entries = parse_relevant(run_cc_notes(evt, "relevant", str(evt.file), "--attached", "--worktree", "--json"))
    drifted = filter_drifted(entries)
    checked = evt.ctx.session.load(StaleChecked)
    fresh = dedup_against_ids(drifted, checked.ids)
    if not fresh:
        return None
    checked.ids = checked.ids + [e["note"]["id"] for e in fresh]
    evt.ctx.session[StaleChecked].set(checked)
    return evt.warn(
        f"You edited {evt.file}, which a note flags as needing attention. Reconcile each: "
        "`cc-notes note verify <id>` to re-confirm it against HEAD, "
        "`cc-notes note edit <id>` to revise it, "
        "`cc-notes note supersede <old> --by <new>` to replace it, or "
        "`cc-notes note expire <id>` to flag it out-of-date if it's no longer accurate.",
        *render_note_lines(fresh),
    )


nudge(
    "Plan approved. Native TaskCreate/TaskUpdate is your private, this-session "
    "scratchpad. Durable shared work goes in `cc-notes task add --backlog` (the "
    "global queue every agent can see and claim) — or plain `cc-notes task add` "
    "for work specific to your current branch. Capture decisions and durable "
    "facts as `cc-notes note add`.",
    only_if=[Tool("ExitPlanMode"), CcNotesAdopted()],
    events=Event.PostToolUse,
    tests={
        Input(tool="ExitPlanMode"): Warn(pattern="cc-notes task add"),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)


nudge(
    "Commit landed. Add a `cc-task: <id>` trailer to link it to its task "
    "(queryable with `git log --grep` and `cc-notes blame <sha>`). Capture any "
    "durable decision behind it as `cc-notes note add \"...\" --tag design` (born "
    "verified against HEAD), then `cc-notes sync` to share your refs.",
    only_if=[Command(GIT_COMMIT), CcNotesAdopted()],
    events=Event.PostToolUse,
    tests={
        Input(command="git commit -m 'add retry ceiling'"): Warn(pattern="cc-task:"),
        Input(command="git commit --amend"): Warn(pattern="cc-notes sync"),
        Input(command="git status"): Allow(),
    },
)


nudge(
    "A merged branch's still-open tasks stay on that branch until you carry them "
    "over. Run `cc-notes reconcile --into <target>` to set them onto the target, "
    "then `cc-notes sync` to converge with the remote. Both are idempotent. "
    "(jj merges never fire git hooks — reconcile is the explicit step.)",
    only_if=[Command(GIT_MERGE_PULL), CcNotesAdopted()],
    events=Event.PostToolUse,
    max_fires=3,
    tests={
        Input(command="git merge feature"): Warn(pattern="reconcile"),
        Input(command="git pull origin main"): Warn(pattern="reconcile"),
        Input(command="git pull"): Warn(pattern="reconcile"),
        Input(command="git status"): Allow(),
        Input(command="git log --no-merges"): Allow(),
    },
)


nudge(
    "You hold a lease now. Run `cc-notes sync` so other agents see the claim, "
    "`cc-notes task renew <id>` on long silent stretches, and `cc-notes task done "
    "<id>` when finished. A crashed hold whose lease expired is reclaimable with "
    "`cc-notes task claim <id> --steal`.",
    only_if=[Command(CC_NOTES_CLAIM), CcNotesAdopted()],
    events=Event.PostToolUse,
    max_fires=2,
    tests={
        Input(command="cc-notes task start d82c087"): Warn(pattern="renew"),
        Input(command="cc-notes task claim 08118da --steal"): Warn(pattern="renew"),
        Input(command="cc-notes task list"): Allow(),
    },
)


nudge(
    "Your native task list is getting large. Native tasks vanish at session end "
    "and are private to this agent — mirror any that are durable or cross-agent "
    "into `cc-notes task add` (`--backlog` if it's shared work anyone can claim). "
    "Keep the purely in-session steps as native todos.",
    only_if=[Tool("TaskCreate"), ManyNativeTasks(), CcNotesAdopted()],
    events=Event.PostToolUse,
    max_fires=2,
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
