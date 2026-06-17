# Coordinating work across agents and time

cc-notes is built for more than one agent and more than one moment. Every task and note
rides the git object database, so anything one agent writes another can read after a sync,
on a different machine, in a different session, days later. This page explains the model
that makes that hold together. Tasks are global with a branch attribute, the backlog is the
shared queue, claims open leases that expire, dependencies gate the ready set, reconcile
carries work across merges, and sync converges everything by union-merge. The throughline
stays constant. Coordination is explicit commands plus CI, never a git hook.

## Tasks are global; the branch is an attribute

A task lives at one flat ref keyed by its id, exactly like a note. Its branch is a mutable
attribute — a `Branch` field resolved last-write-wins — not part of its identity. That has
two consequences worth internalizing:

- **Every id-addressed command resolves by id alone.** `show`, `start`, `claim`, `done`,
  `edit`, `comment`, `dep`, `move`, `renew` — none take a `--branch` flag, because the id is
  global. `--branch`, `--backlog`, and `--all-branches` are reader filters on `list` and
  `ready`, and setters on `add`, `move`, and `edit`. Nothing more.
- **Moving a task between branches is a plain attribute write.** `cc-notes task move <id>
  --to <branch>` sets the `Branch` field. No ref moves, no relocation machinery, just the
  same CRDT append as editing a title.

`cc-notes task list` and `cc-notes task ready` default to the tasks on your current branch
(`Branch == HEAD`). Point them elsewhere with `--branch <branch>`, or widen to
`--all-branches` to see every line of work at once, grouped by branch.

## The backlog is the shared queue

The backlog is the set of tasks not assigned to any branch: `Branch == ""`. Backlog tasks
are visible to **every agent on every branch**, and they are the cross-agent queue. Capture
shared work there; capture work specific to your current branch with a plain `task add`.

```console
$ cc-notes task add "Add retry backoff to the API client" --backlog --priority 1 --label api
d82c087	open	P1	-	Add retry backoff to the API client
```

`cc-notes task backlog` (shorthand for `task list --backlog --status open`) shows the open
queue. `cc-notes status` (alias `board`) puts the backlog in its own section alongside your
branch's tasks and a cross-branch view of who holds what.

## Claiming opens a lease

`cc-notes task start <id>` is the everyday way to grab work. It atomically **claims** the
task (sets the assignee to your actor) **and** moves it onto your current branch
(`Branch = HEAD`), opening a lease. One command takes a backlog item and makes it yours,
here.

```console
$ cc-notes task start d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
```

The actor that signs the claim is `CC_NOTES_ACTOR` (`"Name <email>"`) if set, otherwise
your git `user.name`/`user.email`. The claim and lease primitives key on that actor.

`cc-notes task claim <id>` is the narrower primitive. It claims an open, unassigned task
without forcing a branch move. Use `task start` when you are picking up work to do now;
reach for `task claim` when you want the claim semantics without re-homing the task.

### First-wins, resolved deterministically

Two agents can claim the same task before either syncs. The fold's claim rule resolves the
race the same way on every replica. The first claim wins, deterministically, so after the
next `cc-notes sync` the losing agent sees the task already assigned and not a corrupt
double-claim. `cc-notes task claim <id> --sync` makes that check explicit — it claims, then
syncs and re-checks, yielding the task if another agent won the race.

### Heartbeats and the reader-side TTL

A claim is a lease rather than a permanent grab; otherwise a crashed agent would strand work
forever. The lease heartbeat is the author-time of the latest op by the assignee, so **any**
edit, comment, or `renew` refreshes it for free. For a long silent stretch where you are not
otherwise touching the task, beat the heart by hand:

```console
$ cc-notes task renew d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
```

Staleness is judged by the **reader** against a TTL threshold, never inside the fold. That
is the guarantee that keeps everything deterministic. The fold only records when the last op
happened, and each reader compares that timestamp to `now` against the configured TTL. The
fold never depends on wall-clock, so every replica folds to the same snapshot, and the
stale verdict is a pure presentation-time function of the threshold.

Set the threshold with `cc-notes.leaseTTL` in git config (or `CC_NOTES_LEASE_TTL`),
conservatively 1h by default. Pin it per-repo so every agent agrees, and keep it **larger
than your sync interval** — a healthy holder whose last heartbeat hasn't propagated yet
looks stale to a reader who synced more recently.

### Reclaiming a stale claim

`cc-notes task stale` surfaces in-progress tasks whose lease has exceeded the threshold,
the abandoned claims of crashed or walked-away agents. Its lean line carries a trailing idle
marker. Reclaim one with `--steal`:

```console
$ cc-notes task stale
08118da	in_progress	P1	bob <bob@example.com>	build the widget	idle 2h14m
$ cc-notes task claim 08118da --steal
08118da	in_progress	P1	ada <ada@example.com>	build the widget
```

`--steal` is deterministic and safe against a holder who is merely slow to sync: a holder
who renewed inside the TTL keeps the task even if the steal raced, because the steal only
takes effect when the prior lease has expired by the same reader-side rule.

## Dependencies and blocking

`cc-notes task dep <id> <blocker>` records that `<id>` is blocked by `<blocker>`; blocker
ids resolve globally, so a task can depend on one anywhere — another branch, the backlog,
anywhere. A task with any open blocker drops out of `cc-notes task ready`, so the pickup
queue surfaces only work that can start now. Closing the blocker (`done` or `cancel`) frees
the dependent back into the ready set on the next listing. `cc-notes task undep <id>
<blocker>` removes the edge.

```console
$ cc-notes task dep e0b8f73 9c4e2a1
e0b8f73	open	P2	-	Read sessions from the new schema
```

Use `--parent` (on `add` or `edit`) for hierarchy — an epic with child tasks. Parents are
structure, not scheduling. Dependencies gate the ready queue, parents do not.

## Reconcile carries work across merges

Merging a branch's code leaves its tasks untouched. The merge moves your `refs/heads/*`, not
the cc-notes refs. A merged branch's still-open tasks keep their old `Branch` value and stay
invisible on the target until you carry them over. That is the job of `cc-notes reconcile
--into <target>`. It finds the branches merged into the target and **sets the `Branch`** of
their open and in-progress tasks to the target. Done and cancelled tasks stay behind as
settled history.

```console
$ cc-notes reconcile --into main
scanned: 1
merged: 1
carried: 2
into: main
feature/x:
08118da	open	P1	-	build the widget
b932fd9	open	P2	-	test the widget
```

- **Auto-discovery.** With no `--from`, reconcile discovers branches fully merged into the
  target — a branch whose tip is an ancestor of the target tip — and carries them.
- **Deterministic in CI.** `--from <branch>` narrows to named branches; use it in CI so a
  merge job carries exactly the branch it merged.
- **Squash-merges and deletes.** A squash collapses history, so the ancestry test can't
  prove the branch merged; name it and skip the test with `--from <branch> --force`. `--force`
  is only valid with `--from`.
- **Idempotent.** Re-running reconcile (or running it on every merge in CI) never
  double-carries — setting `Branch` to a value it already holds is a no-op fold.
- **`--dry-run`** reports what would move without writing.

The everyday rule: merge code with git or jj, run `cc-notes reconcile --into <target>`, then
`cc-notes sync`.

### Why there is no git hook

It would be tempting to wire reconcile to a post-merge git hook and forget about it. cc-notes
deliberately does not, because **jj fires no git hooks**. An agent driving the repo through
jj would silently skip a hook-based reconcile, stranding the merged branch's tasks with no
error. So reconcile (and sync) are explicit commands that behave identically under git and
jj. Coordination is explicit commands plus CI, never a hook that one of your VCS front-ends
silently ignores.

### Automate it in CI (recommended)

`cc-notes init --ci` installs a GitHub Actions workflow that runs `cc-notes reconcile` against
the default branch on every push to it, using the release binary. This is the recommended
automation path — and the only reliable one for jj users, since git post-merge hooks never fire
under jj. The job installs cc-notes, then runs sync, reconcile, and sync again, so a merged
branch's open tasks land on the default branch without any local hook. `cc-notes init --hook` remains as a
git-only fallback for repos that never touch jj. Install the workflow standalone with `cc-notes
workflows install`.

## Sync: union-merge, never clobber

`cc-notes sync` converges `refs/cc-notes/*` with the remote. It fetches, merges divergent
refs, pushes, and loops until the remote and local agree. The merge is a **union of event
logs**, not a last-writer overwrite. Each entity is an event-log CRDT, so two agents who
edited the same task on different replicas both keep their events; the deterministic fold
replays the union into one consistent snapshot everywhere. Sync never clobbers another
agent's writes.

The conflict policy follows from that:

- **Field-level conflicts resolve last-write-wins by timestamp.** If two agents set the same
  scalar field — title, priority, the `Branch` attribute — the later write wins, consistently
  on every replica.
- **Structural additions all survive.** Comments, labels, dependencies, anchors — additive
  events union together; nothing is dropped because two agents added concurrently.

```console
$ cc-notes sync
pushed: 2
rounds: 1
```

After `cc-notes init`, plain `git push` and `git pull` carry the cc-notes refs alongside
your branches, so a normal git workflow keeps the data in step. Under jj it does not: `jj
git push`/`jj git fetch` bridge only `refs/heads/*`, so the `refs/cc-notes/*` refs never ride
along — run `cc-notes sync`, which drives the real git binary directly and carries the refs
regardless of front-end. `cc-notes sync` is the explicit converge-and-push when you want the
refs reconciled without touching your branches; `--full` forces a whole-namespace scan
instead of the default changed-refs-only pass.

## Linking commits to tasks

A task's value compounds when it points at the code that implemented it. Two mechanisms tie
them together, and `blame` reads them back:

- **The `cc-task:` trailer.** Add `cc-task: <id>` as a git trailer on the commits that do
  the work. The link is then queryable with `git log --grep` and resolved by `cc-notes
  blame <sha>`.
- **The done anchor.** `cc-notes task done <id>` closes the task **and** anchors your HEAD
  commit onto it. `cc-notes task show <id>` then lists the commits that implemented the task.

```console
$ git commit -m "Clamp retry backoff to 30s

cc-task: d82c087"
$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
$ cc-notes blame 4f1c9ab
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

`cc-notes blame <sha>` is the reverse of `task show`'s commit list. Given a commit, it names
the task(s) it implemented, resolved from both the `cc-task:` trailer and the task anchors.

## The shared-remote multi-agent loop

The shared-remote pattern is the point. Several agents — across machines or sessions — point
at one remote and coordinate entirely through it. No lock server, no live connection between
agents is needed, since the git remote is the whole coordination substrate.

1. **Orient.** `cc-notes status` — the backlog, your branch's tasks, and every in-progress
   task across branches grouped by assignee with a fresh-or-STALE lease flag.
2. **Grab.** `cc-notes task start <id>` claims a backlog item and moves it onto your branch;
   the claim marks it taken for everyone.
3. **Stay alive.** Any edit or comment refreshes the lease; `cc-notes task renew <id>` on a
   long silent stretch. `cc-notes task stale` finds abandoned claims; `cc-notes task claim
   <id> --steal` reclaims one.
4. **Work and link.** Commit code with a `cc-task: <id>` trailer; close with `cc-notes task
   done <id>` to anchor the commit onto the task.
5. **Publish.** `cc-notes sync` shares your claims, edits, and closes; the next agent's sync
   sees them.

Sync at the moments that keep the shared view honest: on **orient**, before you claim
anything, so the ready queue reflects what other agents have already taken; after **start**,
**claim**, or **done**, to broadcast your lease and progress to the next agent; after every
`cc-notes reconcile`, to publish the carried tasks; and automatically from **CI**, where the
merging Action runs sync on the shared remote. The more agents share a remote, the more a
stale local view risks two agents reaching for the same task before a claim propagates — and
while the deterministic first-wins rule keeps that race from corrupting state, a fresh sync
keeps both agents from wasting effort on the same work.
