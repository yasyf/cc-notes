# Coordinating work across branches and agents

cc-notes is built for more than one agent and more than one moment. Tasks and notes ride
the git object database, so anything one agent writes another can read after a sync — on a
different machine, in a different session, days later. This page explains how that
coordination holds together: how branches namespace tasks, how claiming and dependencies
divide work, how promotion and reconciliation move tasks as branches merge, and what sync
guarantees when several agents push to the same remote.

## Branches as task namespaces

Tasks scope to the branch they are created on. A task lives at
`refs/cc-notes/tasks/<branch>/<id>`, so the work captured on `feature/auth` is a distinct
namespace from the work on `main`. This is deliberate: a feature branch carries its own
backlog, and that backlog does not pollute the trunk until the branch lands. Notes, by
contrast, are repo-global — one namespace shared across all branches — because a design
decision is a fact about the codebase, not about a line of work.

`cc-notes task list` and `cc-notes task ready` default to the current branch. Point them at
another namespace with `--branch <branch>` to see what another line of work has queued.

## Claiming and assignee

`cc-notes task claim <id>` takes an open, unassigned task: it sets the assignee to the git
user and moves the task to in-progress. The claim is the coordination primitive — it is how
one agent signals "I have this" to every other agent reading the same refs. The fold's claim
rule resolves a race deterministically: if two agents claim the same task before syncing,
the merge picks one winner consistently on every replica, so the losing agent sees the task
already assigned after the next `cc-notes sync` rather than a corrupt double-claim.

`cc-notes task ready` lists only open, unassigned, unblocked tasks, so an agent looking for
work pulls from a queue that already excludes anything claimed. To hand a task off, clear
the assignee with `cc-notes task edit <id> --unassign`; to reassign directly, use
`cc-notes task edit <id> --assignee <user>`.

## Dependencies and blocking

`cc-notes task dep <id> <blocker>` records that `<id>` is blocked by `<blocker>`; blocker
ids resolve globally, so a task can depend on one in another branch's namespace. A task with
any open blocker drops out of `cc-notes task ready` — the queue surfaces only work that can
start now. Closing the blocker (`done` or `cancel`) frees the dependent task back into the
ready set on the next listing. `cc-notes task undep <id> <blocker>` removes the edge. On
`cc-notes task show`, the `blocked_by` field lists a task's blockers and the derived
`blocks` field lists what waits on it.

Use `--parent` (on `add` or `edit`) for hierarchy — an epic with child tasks — which is
about structure, not scheduling. Dependencies gate the ready queue; parents do not.

## Promote vs reconcile

Two commands move tasks between branch namespaces, at two levels of abstraction.

`cc-notes task promote --to <branch> [ID]...` is the low-level move: it relocates tasks from
one namespace to another, all of them or a named subset, regardless of status or merge
state. Reach for it to manually re-home work — for example, when you split a task onto a new
branch.

`cc-notes reconcile --into <branch>` is the high-level, merge-aware operation. After you
merge a branch into the target, its tasks still sit on the merged branch's namespace and
stay invisible on the target. Reconcile auto-discovers branches fully merged into the
target and promotes their open and in-progress tasks into it — done and cancelled tasks stay
behind as settled history. It is idempotent, so re-running it (or running it in CI on every
merge) never double-promotes.

The everyday rule: merge code with git or jj, then run `cc-notes reconcile --into <target>`
to carry the still-open work forward. Use `task promote` only when you need a manual move
that reconcile's merge logic would not make for you.

## Sync semantics: union-merge, never clobber

`cc-notes sync` converges `refs/cc-notes/*` with the remote: it fetches, merges any
divergent refs, pushes, and loops until the remote and local agree. The merge is a union of
event logs, not a last-writer-wins overwrite. Each entity is an event-log CRDT, so two
agents that edited the same task on different replicas both keep their events; the
deterministic fold replays the union into one consistent snapshot on every replica. Sync
never clobbers another agent's writes — concurrent edits combine rather than one stomping
the other. Field-level conflicts (two agents set the same field) resolve last-write-wins by
timestamp; structural additions (comments, labels, dependencies) all survive.

After `cc-notes init`, plain `git push` and `git pull` carry the refs too, so a normal git
workflow keeps cc-notes data in step. `cc-notes sync` is the explicit converge-and-push when
you want the cc-notes refs reconciled without touching your branches.

## Multiple agents on a shared remote

The shared-remote pattern is the point. Several agents — across machines or sessions — point
at one remote and coordinate through it:

1. Each agent runs `cc-notes sync` to pull the current task and note state.
2. An agent calls `cc-notes task ready` and `cc-notes task claim <id>` to take work; the
   claim marks it taken for everyone else.
3. The agent works, appends `cc-notes task comment <id>` notes for context, and closes with
   `cc-notes task done <id>`.
4. `cc-notes sync` publishes the changes; the next agent's sync sees the claim and the
   close.

Because the merge is a union and claims resolve deterministically, agents do not need a lock
server or a live connection to each other — the git remote is the entire coordination
substrate. Sync often enough that the ready queue reflects reality; the more agents share a
remote, the more a stale local view risks two agents reaching for the same task before a
claim propagates.

## Branch merge, delete, and rename

- **Merge.** Merging a branch does not move its tasks — the merge touches your code refs,
  not `refs/cc-notes/*`. Run `cc-notes reconcile --into <target>` afterward to promote the
  merged branch's open and in-progress tasks into the target. Under jj this is the only
  path, since jj never fires the git hooks that might otherwise automate it; reconcile is an
  explicit command precisely so it works identically under git and jj.
- **Squash-merge.** A squash collapses history, so reconcile's ancestry test cannot prove
  the branch merged. Name the branch explicitly and skip the test:
  `cc-notes reconcile --into <target> --from <branch> --force`.
- **Delete.** Deleting a git branch leaves its task namespace intact —
  `refs/cc-notes/tasks/<branch>/*` is independent of `refs/heads/<branch>`. Reconcile
  before deleting (or with `--from <branch> --force` after) to avoid stranding open tasks in
  a namespace no one lists. Notes are unaffected; they are repo-global.
- **Rename.** Renaming a branch does not rename its task namespace. Move the tasks across
  with `cc-notes task promote --from <old> --to <new>` so they follow the new branch name.
