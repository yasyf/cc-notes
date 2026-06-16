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
CC_NOTES_CLAIM = r"^cc-notes\s+task\s+(?:claim|start)\b"


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
    "Before picking up work, run `cc-notes status` to orient: the shared backlog "
    "(unassigned work any agent can grab), your current branch's open and "
    "in-progress tasks, who holds what across branches (each flagged fresh or "
    "STALE), and notes needing review.",
    only_if=[CcNotesAdopted()],
    events=Event.UserPromptSubmit,
    max_fires=1,
    tests={
        Input(prompt="let's start on the retry logic"): Warn(pattern="cc-notes status"),
    },
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
