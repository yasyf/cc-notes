---
name: using-cc-notes
description: >-
  Use cc-notes to record durable tasks and notes that outlive a session, stored as
  git objects on refs/cc-notes/*. Triggers when an agent records a task or note for
  later; runs status to orient on the backlog and who holds what; claims or starts a
  task; coordinates work across branches and multiple agents; manages leases and
  reclaims stale claims; verifies or supersedes a durable fact; syncs tasks and notes
  with a remote; reconciles tasks after merging a branch; or links commits to the task
  they implemented.
allowed-tools: Bash(cc-notes:*), Read
---

# Using cc-notes

cc-notes is a git-native notes and tasks layer for agents. Every entity — a note or a
task — is an event-log CRDT: an append-only log of operation packs, one per git commit,
on hidden `refs/cc-notes/*` refs inside the repo's object database. The data is versioned,
synced by plain `git push`/`git pull` (or `cc-notes sync` under jj, whose git bridge skips
the cc-notes refs), and invisible in checkouts and diffs. A deterministic fold replays each
log into a snapshot, so every replica reads the same state.

Reach for cc-notes when work or knowledge must survive the current session or reach another
agent. Track moment-to-moment steps for what you are doing right now in the harness's own
todo tool.

## Three tools, three jobs

Get this distinction right first. Native todos, cc-notes tasks, and cc-notes notes differ
along two axes: how long the record lives and who can see it.

| Tool | Lifetime | Scope | Use for |
|------|----------|-------|---------|
| Native todos (`TaskCreate`/`TaskUpdate`) | Ephemeral — this session, gone at session end | This agent's private scratchpad | Decomposing the *current* task into in-session steps |
| `cc-notes task` | Durable — git ODB, synced across machines and agents | Global: one flat ref per task, with a mutable `branch` attribute and a shared backlog every agent sees | Work that outlives the session or coordinates agents: claim, lease, deps, comments, priority, lifecycle |
| `cc-notes note` | Durable — git ODB, synced | Repo-global, optionally anchored to a commit, path, or branch | Design decisions and durable facts, verified and searchable |

Tasks are **global**. Each task is a single flat ref at `refs/cc-notes/tasks/<id>`, exactly
like a note. Its branch is a *mutable attribute*, not part of its identity: `task list` and
`task ready` default to your current branch, the shared **backlog** is every task with no
branch (`task add --backlog`, visible to every agent on every branch), and `task move <id>
--to <branch>` (or `task start`, automatically) re-homes a task by setting that attribute.
Because the id is global, every id-addressed command resolves by id alone — there is no
`--branch` on `show`/`claim`/`start`/`done`/`move`/`renew`.

A note records when it was last **verified** true; superseding a note points it at its
replacement and drops it from default listings.

The identity that signs writes is `CC_NOTES_ACTOR` (`"Name <email>"`) if set, else your git
`user.name`/`user.email`. Claims and leases key on that actor.

See `references/tasks-vs-notes.md` for worked examples of choosing among the three.

## Canonical agent flow

The spine of day-to-day use. Run `init` once per repo; everything else recurs as you work.

**1. Initialize (once per repo).** `cc-notes init` installs the refspecs so plain `git
push`/`git pull` carry the cc-notes refs alongside your branches, then wires whatever the
repo is already set up for: when a `.claude/` directory exists it registers the cc-notes
plugin in `.claude/settings.json` and enables the cc-notes capt-hook pack (manifest at
`.claude/capt-hook.toml`); when a `.github/` directory exists it installs the reconcile CI
workflow (`--no-ci` to skip, `--ci` to force without `.github/`). init never creates
`.claude/` — it wires Claude Code only when the repo already uses it. Under jj the plain-git
path doesn't hold (`jj git push`/`jj git fetch` bridge only `refs/heads/*`, leaving
`refs/cc-notes/*` behind), so run `cc-notes sync`, which drives git directly and carries the
refs regardless of front-end, or real `git push`/`git pull`.

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
registered: cc-notes plugin in .claude/settings.json
```

**2. Orient.** `cc-notes status` (alias `board`) is a read-only, sectioned view: the shared
backlog, your current branch's open and in-progress tasks, every in-progress task across all
branches grouped by assignee with a fresh/STALE lease flag, and how many notes need review.
Run it before picking up work.

```console
$ cc-notes status
backlog
  08118da	open	P1	-	build the widget
  b932fd9	open	P2	-	test the widget
your branch (feature/auth)
  d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
in progress across branches
  ada <ada@example.com>	d82c087	fresh
  ben <ben@example.com>	7c1e3f0	STALE
notes: 14 total, 3 need review
```

**3. Plan.** Capture shared work onto the backlog; capture branch-specific work plainly.

```console
$ cc-notes task add "build the widget" --backlog --priority 1 --no-validation-criteria
08118da	open	P1	-	build the widget
$ cc-notes task add "Add retry backoff to the API client" --priority 1 --label api --criterion "backoff caps at 30s"
d82c087	open	P1	-	Add retry backoff to the API client
```

**4. Grab.** `task start` atomically claims the task (deterministic first-wins) and moves it
onto your current branch, opening a lease.

```console
$ cc-notes task start d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
```

**5. Stay alive.** Any change you make to a task refreshes its lease; for long silent
stretches, `task renew <id>`. `task stale` surfaces in-progress tasks whose lease has expired
— a crashed agent's abandoned claim — and `task claim <id> --steal` reclaims one.

```console
$ cc-notes task stale
7c1e3f0	in_progress	P2	ben <ben@example.com>	Wire the gateway client	idle 2h14m
$ cc-notes task claim 7c1e3f0 --steal
7c1e3f0	in_progress	P2	ada <ada@example.com>	Wire the gateway client
```

**6. Work and link.** Commit code with plain git, adding a `cc-task: <id>` trailer so the
commit links to the task (queryable with `git log --grep` and `cc-notes blame <sha>`).
`task done` closes the task and anchors your HEAD commit onto it; `task show` then lists the
commits that implemented it.

```console
$ git commit -m "Clamp API retry backoff at 30s

cc-task: d82c087"
$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

**7. Record facts.** A note is born verified against the current HEAD. Re-confirm it later
with `note verify`, and replace a changed decision with `note supersede`.

```console
$ cc-notes note add "Retry backoff caps at 30s" --path internal/api/client.go --tag design --body "The server drops connections past 30s, so exponential backoff is clamped."
b71e0d4	2026-06-16	design	Retry backoff caps at 30s
```

**8. Merge and reconcile.** Merge code with git or jj, then carry the merged branches'
still-open tasks onto the target and converge with the remote. Both steps are idempotent.

```console
$ cc-notes reconcile --into main
scanned: 1
merged: 1
carried: 2
into: main
feature/x:
08118da	open	P1	-	build the widget
b932fd9	open	P2	-	test the widget
$ cc-notes sync
pushed: 2
rounds: 1
```

**9. Maintain.** `note review` surfaces drifted, stale, and unverified facts; `task archived`
hides long-closed work; `gc --prune-remote` (opt-in, best-effort) physically reclaims
tombstoned refs.

## Command cheat-sheet

The verbs reached for most. The full surface — every flag, default, and output shape — is in
`references/cli-reference.md`.

| Command | Purpose |
|---------|---------|
| `cc-notes init` | Install the `refs/cc-notes/*` refspecs, plus the plugin and CI the repo is ready for (once per repo) |
| `cc-notes status` | Orient: backlog, your branch's tasks, who holds what, notes needing review |
| `cc-notes sync` | Union-merge the cc-notes refs with the remote and push, looping until stable |
| `cc-notes reconcile --into <branch>` | Carry merged branches' open tasks onto the target |
| `cc-notes blame <sha>` | List the task(s) a commit implemented |
| `cc-notes task add "<title>"` | Capture branch work; add `--backlog` for shared work |
| `cc-notes task ready` | List open, unassigned, unblocked tasks — the pickup queue |
| `cc-notes task start <id>` | Claim a task and move it onto your current branch |
| `cc-notes task claim <id> --steal` | Reclaim an in-progress task whose lease expired |
| `cc-notes task renew <id>` | Refresh the lease heartbeat on a task you hold |
| `cc-notes task done <id>` | Close a task and anchor HEAD onto it |
| `cc-notes task move <id> --to <branch>` | Re-home a task by setting its branch |
| `cc-notes note add "<title>"` | Record a durable fact, born verified against HEAD |
| `cc-notes note verify <id>` | Record that a note is still true as of now |
| `cc-notes note review` | Surface drifted, stale, and unverified notes |
| `cc-notes note search "<query>"` | Ranked search over titles, tags, and bodies |

Append `--json` to any note, task, sync, reconcile, or status command for a machine-readable
record instead of the lean line.

## Projects and sprints (optional)

An optional planning layer sits on top of tasks — skip it for the canonical flow above. A
task can carry an independent **sprint** pointer (a time-boxed grouping) and **project**
pointer (a long-lived one), and a sprint can point at a project; all three are optional and
**repo-wide**, not branch-scoped like a task's `branch`. Membership is an upward pointer the
reader inverts, so `cc-notes sprint show` and `cc-notes project show` derive the tasks (and a
project's sprints) that roll up into them. Independently, a task can carry **validation
criteria** (`cc-notes task add --criterion`, or the `cc-notes task criterion` subgroup):
`cc-notes task done` refuses to close while any criterion is unmet unless you pass `--force`,
and `cc-notes task validate` runs each criterion's check script behind an explicit
confirmation. See `references/sprints-and-projects.md` and `references/validation-criteria.md`.

## References

- `references/cli-reference.md` — the complete command surface: every flag, default, and the
  lean-line and `--json` output shape for each command.
- `references/coordination.md` — how agents coordinate over time: the backlog and the branch
  attribute, claims and leases, stale-claim recovery, deps and blocking, reconcile-on-merge,
  and union-merge sync across a shared remote.
- `references/tasks-vs-notes.md` — the three-way distinction with worked examples of choosing
  native todo vs cc-notes task vs cc-notes note.
- `references/lifecycle-and-hygiene.md` — keeping the record honest: task leases and
  staleness, note verification, drift, and supersession, and the maintenance verbs.
- `references/sprints-and-projects.md` — the optional planning layer: tasks rolling up into
  sprints and projects, the repo-wide upward pointers, and the derived reverse indexes.
- `references/validation-criteria.md` — structured acceptance criteria on a task, the gated
  `task done`, and the explicit, confirmation-gated `task validate` trust boundary.
