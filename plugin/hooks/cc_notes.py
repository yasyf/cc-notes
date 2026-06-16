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
  * ``cc-notes task`` — durable, git-synced, branch-scoped work that outlives the
    session or coordinates multiple agents (claim, deps, lifecycle);
  * ``cc-notes note`` — durable, git-synced, repo-global design decisions & facts.
"""

from __future__ import annotations

import shutil

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    Input,
    Tool,
    Warn,
    nudge,
)
from captain_hook.types import Command

NATIVE_TASK_MIRROR_THRESHOLD = 5

GIT_MERGE_PULL = r"^git\s+(?:-\S+\s+)*(?:merge|pull)\b"
GIT_COMMIT = r"^git\s+(?:-\S+\s+)*commit\b"


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


nudge(
    "Merged-branch tasks live at refs/cc-notes/tasks/<merged-branch>/* and are invisible "
    "here until promoted. Run `cc-notes reconcile` to promote the merged branch's open "
    "tasks into this branch, then `cc-notes sync` to converge the refs with the remote. "
    "Both are idempotent. (jj merges never fire git hooks — reconcile is the explicit step.)",
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
    "Commit landed. Capture any durable decisions or facts behind it as "
    "`cc-notes note add \"...\" --path <file> --tag design`, and run `cc-notes sync` to "
    "share your task/note refs with collaborators (notes & tasks ride the same remote as "
    "code, but only sync when you push them).",
    only_if=[Command(GIT_COMMIT), CcNotesAdopted()],
    events=Event.PostToolUse,
    tests={
        Input(command="git commit -m 'add retry ceiling'"): Warn(pattern="cc-notes note add"),
        Input(command="git commit --amend"): Warn(pattern="cc-notes sync"),
        Input(command="git status"): Allow(),
    },
)


nudge(
    "Plan approved. Native TaskCreate/TaskUpdate is your private, this-session scratchpad "
    "for the in-session steps. Anything durable or cross-branch — work another agent should "
    "be able to find and claim — belongs in `cc-notes task add` (branch-scoped, synced); "
    "design decisions and durable facts belong in `cc-notes note add` (repo-global, synced). "
    "Use both: decompose a cc-notes task into native todos while you execute it.",
    only_if=[Tool("ExitPlanMode"), CcNotesAdopted()],
    events=Event.PostToolUse,
    tests={
        Input(tool="ExitPlanMode"): Warn(pattern="cc-notes task add"),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)


nudge(
    "Your native task list is getting large. Native tasks vanish at session end and are "
    "private to this agent — mirror any that are durable or cross-branch (a bug to fix "
    "later, work another agent should claim) into `cc-notes task add` so they survive and "
    "coordinate. Keep the purely in-session steps as native todos.",
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
