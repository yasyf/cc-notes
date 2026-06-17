# cc-notes CLI reference

The complete command surface, grouped by noun: repo, task, sprint, project, note. Every
command takes `-h`/`--help`, and every note, task, sprint, project, sync, and reconcile
command takes `--json` for a machine-readable record. Without `--json`, mutations echo a
lean tab-separated line and listings print one lean line per entity.

Sprints and projects are an **optional planning layer** on top of tasks — group work into a
time-boxed sprint or a long-lived project without touching the canonical task and note flow.
A task that joins neither behaves exactly as it did before.

## The model in one screen

- **Tasks are global.** A task lives at a flat `refs/cc-notes/tasks/<id>` — one ref per
  task, exactly like a note. Its branch is a mutable attribute (the `Branch` field,
  last-write-wins), not part of its identity.
- **Your branch's tasks** are the ones whose `Branch` equals your current `HEAD`. `task
  list` and `task ready` default to that scope.
- **The backlog** is the shared queue of work not pinned to any branch: `Branch == ""`.
  Backlog tasks are visible to every agent on every branch. Create one with `task add
  --backlog`; pull from it with `task backlog`.
- **Moving a task to a branch** is a plain attribute write: `task move <id> --to <branch>`,
  or automatically when you `task start` it. No ref moves, no promotion machinery.
- **Ids are global**, so every id-addressed command (`show`, `claim`, `start`, `done`,
  `edit`, `comment`, `dep`, `move`, `renew`, …) resolves by id alone. There is **no
  `--branch` flag on id-addressed commands**. `--branch`, `--backlog`, and `--all-branches`
  are reader filters on `list`/`ready` and setters on `add`/`move`.
- **Notes are repo-global** with optional commit, path, and branch anchors pointing at the
  code they describe. A note records when it was last verified true; a superseded note
  points at its replacement and drops out of default listings.
- **Identity.** Writes are signed by `CC_NOTES_ACTOR` (`"Name <email>"`) when set, else by
  git `user.name`/`user.email`. The claim and lease primitives key on this actor.
- **Sprints and projects are an optional, repo-wide planning layer.** A task carries an
  independent sprint pointer **and** project pointer — both optional, both last-write-wins,
  and applied across the whole repository, not per branch. A sprint carries an optional project
  pointer. Membership is always an upward pointer; "tasks in a sprint", "sprints in a
  project", and "tasks in a project" (direct ∪ via-sprint, deduplicated) are **derived**
  reverse indexes, never stored. Sprint ids, project ids, and criterion ids all resolve by id
  prefix exactly like tasks — an ambiguous prefix exits 5, no match exits 3.

## Lean-line formats

| Entity | Fields (tab-separated) |
|--------|------------------------|
| Task | `<short7-id>` `<status>` `P<priority>` `<assignee\|->` `<title>` |
| Sprint | `<short7-id>` `<status>` `<title>` |
| Project | `<short7-id>` `<status>` `<title>` |
| Note | `<short7-id>` `<YYYY-MM-DD updated, UTC>` `<tags csv\|->` `<title>` |

A criterion's short id is the first 7 hex chars of its 32-hex nonce; `task criterion list`
and the validation logs print `<short7-crit-id>` `<status>` `<text>`.

Short ids are the first 7 hex chars; `-` stands in for an empty field. `task stale` appends
a trailing idle marker to the task line; `note review` appends a verdict to the note line.
JSON output uses full 40-hex ids, RFC3339 UTC timestamps, `null` for unset optionals, and
sorted set slices.

## Repo commands

### `cc-notes init`

Install the `refs/cc-notes/*` refspecs on a remote. Run once per repo. After init, plain
`git push` and `git pull` carry the cc-notes refs alongside your branches. Under jj that
doesn't hold: `jj git push`/`jj git fetch` bridge only `refs/heads/*`, leaving the
`refs/cc-notes/*` refs behind — run `cc-notes sync` (it drives the real git binary directly
and carries the refs regardless of front-end) or real `git push`/`git pull`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--remote <name>` | `origin` | Remote to wire |
| `--ci` | off | Also install a GitHub Actions workflow reconciling merged tasks onto the default branch (recommended; works under git and jj) |
| `--hook` | off | Also install a git post-merge hook running `cc-notes reconcile` (git-only; skipped by jj, rebase, and server-side squash) |

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
```

### `cc-notes sync`

Converge `refs/cc-notes/*` with a remote and push. Fetches, union-merges divergent event
logs (never clobbers), and pushes, looping until stable. The lean report prints only nonzero
verbs followed by the round count.

| Flag | Default | Meaning |
|------|---------|---------|
| `--remote <name>` | `origin` | Remote to sync with |
| `--full` | off | Force a whole-namespace reconcile scan instead of the changed-refs-only pass |
| `--json` | off | Emit JSON |

```console
$ cc-notes sync
pushed: 2
rounds: 1
```

JSON shape: `{"created":int,"fast_forwarded":int,"merged":int,"pushed":int,"rounds":int}`.

### `cc-notes status` (alias `cc-notes board`)

Orient before picking up work. A sectioned, read-only view of:

1. the shared backlog (`Branch == ""`),
2. your current branch's open and in-progress tasks,
3. every in-progress task across all branches, grouped by assignee, each flagged `fresh` or
   `STALE` by its lease,
4. a note summary, including how many notes need review.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

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

### `cc-notes reconcile`

Carry merged branches' open and in-progress tasks onto a target branch by setting their
`Branch` to the target. After a git or jj merge, a branch's still-open tasks keep their old
`Branch` and stay off the target until reconcile re-homes them. Run it after merging.

| Flag | Default | Meaning |
|------|---------|---------|
| `--into <branch>` | current branch | Target branch to carry tasks onto |
| `--from <branch>` | (auto-discover) | Restrict to named source branches; repeatable. Deterministic — use in CI |
| `--force` | off | Skip the ancestry test; only valid with `--from`. For squash-merges and deleted branches |
| `--dry-run` | off | Report what would change without writing |
| `--json` | off | Emit JSON |

Reconcile auto-discovers branches fully merged into the target — a branch whose tip is an
ancestor of the target tip. `--from` narrows to named branches for a deterministic CI run;
`--force` (only with `--from`) skips the ancestry test so squash-merged or already-deleted
branches still carry over. It is idempotent — safe to re-run and to run in CI — and works
under both git and jj; jj never fires git hooks, which is why this is an explicit command.

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

JSON shape:
`{"into":string,"scanned":int,"merged":int,"carried":int,"branches":[{"branch":string,"merged":bool,"reason":string,"tasks":[full-id,…]}]}`.
`reason` is empty when the branch's tasks were carried over and names the skip cause
otherwise.

### `cc-notes blame <sha>`

List the task(s) a commit implemented, resolved from the commit's `cc-task:` trailer and
from task commit-anchors. This is the reverse of the commit list `task show` prints.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes blame 4f1c2ab
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

### `cc-notes compact <id>`

Collapse an entity's op-log into a checkpoint so future folds are cheap. The id and the full
folded state are preserved; objects stay in the ODB.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes gc`

Local maintenance. By default it tidies local state; with `--prune-remote` it physically
deletes tombstoned entity refs on the remote via `git push --delete`. Pruning is best-effort
and non-convergent — a stale clone can re-advertise a pruned ref — so it is opt-in and never
part of normal sync.

| Flag | Default | Meaning |
|------|---------|---------|
| `--prune-remote` | off | Physically delete tombstoned refs on the remote |
| `--json` | off | Emit JSON |

### `cc-notes mount [MOUNTPOINT]`

Mount the cc-notes entities as an editable filesystem in the foreground; Ctrl-C unmounts.
Notes render as Markdown under `/notes`; tasks, sprints, and projects render as JSON under
`/tasks`, `/sprints`, and `/projects`, with edits and diffs flowing back into the object
database. A read-only nested tree of symlinks under `/projects` and `/sprints` walks the
hierarchy; the [sprints and projects reference](sprints-and-projects.md) covers it.
`MOUNTPOINT` is optional and defaults to a managed per-repo directory under
`~/.cc-notes/mnt`. Requires a `_fuse` binary plus a FUSE implementation (`fuse-t` on macOS,
`fuse3` on Linux).

```console
$ cc-notes mount ./cc-notes-fs
```

### `cc-notes version`

Print the cc-notes version.

```console
$ cc-notes version
v0.2.0 (dd02f2d)
```

## Setup commands

Wire the Claude Code integration into a repository. `skills` and `workflows` write into
the repo and take `--dir` to redirect the destination, relative to the repo root; `hooks`
delegates to `capt-hook pack add`.

### `cc-notes hooks install`

Enable the cc-notes capt-hook pack. Runs `uvx capt-hook pack add
github:yasyf/cc-notes@<binary version>`, which caches the pinned pack tarball,
records `[packs.cc-notes]` in `.claude/hooks/packs.toml`, and wires the events
into `.claude/settings.local.json`. Takes no flags.

### `cc-notes skills install`

Install the `using-cc-notes` skill into the repository.

| Flag | Default | Meaning |
|------|---------|---------|
| `--dir <path>` | `.claude/skills` | Destination directory, relative to the repo root |

### `cc-notes workflows install`

Install the cc-notes CI workflow into the repository — a GitHub Actions job that runs `cc-notes
reconcile` against the default branch on every push to it, using the release binary. This is what
`cc-notes init --ci` writes; install it standalone here. Requires cc-notes >= 0.3.0.

| Flag | Default | Meaning |
|------|---------|---------|
| `--dir <path>` | `.github/workflows` | Destination directory, relative to the repo root |

## Task commands

Tasks are global, addressed by id. Id-addressed commands take **no** `--branch`. `--branch`,
`--backlog`, and `--all-branches` are reader filters on `list`/`ready` and setters on
`add`/`move`/`edit`.

### `cc-notes task add TITLE`

Create a task. `Branch` defaults to your current branch; `--backlog` sets it to `""`;
`--branch` sets it explicitly.

| Flag | Default | Meaning |
|------|---------|---------|
| `--priority <0-3>` | `2` | Priority; 0 is most urgent |
| `--type <type>` | `task` | One of `task`, `bug`, `epic`, `question` |
| `--label <label>` | none | Label; repeatable |
| `--desc <text>` | empty | Description; `-` reads stdin |
| `--criterion <text>` | none | Acceptance criterion; repeatable, **required by default** (see below) |
| `--no-validation-criteria` | off | Create with no criteria; mutually exclusive with `--criterion` |
| `--parent <id>` | none | Parent task id |
| `--sprint <id>` | none | Join a sprint (id prefix) |
| `--project <id>` | none | Join a project directly (id prefix) |
| `--blocked-by <id>` | none | Blocker task id; repeatable, resolved globally |
| `--branch <branch>` | current branch | Set the task's branch explicitly |
| `--backlog` | off | Set `Branch=""` (shared, branch-less) |
| `--json` | off | Emit JSON |

**Acceptance criteria are required by default.** A `task add` with no `--criterion` and no
`--no-validation-criteria` is a usage error (exit 2) — the default nudges every task to
record what "done" means up front. Pass `--no-validation-criteria` to opt out explicitly;
it cannot be combined with `--criterion`. Each criterion starts `pending`; gate `task done`
on them and drive them with the `task criterion` subgroup below.

`--sprint` and `--project` are independent: a task may join a sprint, a project, both, or
neither. Joining a sprint leaves the project pointer untouched; `project show` still counts
the task through its sprint.

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1 --label api \
    --sprint afd8362 --criterion "go test ./... passes" --criterion "p99 latency under 200ms"
286d87c	open	P1	-	Add retry backoff to the API client
$ cc-notes task add "Spike: evaluate gRPC" --no-validation-criteria --project 07daf88
94c0917	open	P2	-	Spike: evaluate gRPC
$ cc-notes task add "Rotate signing keys quarterly" --backlog --no-validation-criteria
5d3e9c1	open	P2	-	Rotate signing keys quarterly
```

### `cc-notes task list`

List tasks. Defaults to open and in-progress on your current branch. `--all-branches` and
`--backlog` group output by branch.

| Flag | Default | Meaning |
|------|---------|---------|
| `--status <csv>` | `open,in_progress` | Status filter, comma-separated |
| `--all` | off | Every status |
| `--include-archived` | off | Include archived (old done/cancelled) tasks |
| `--assignee <user>` | none | Require assignee |
| `--label <label>` | none | Require label; repeatable, ANDed |
| `--type <type>` | none | Require type |
| `--branch <branch>` | current branch | List a named branch |
| `--backlog` | off | List the shared backlog (`Branch==""`) |
| `--all-branches` | off | List every branch, grouped |
| `--json` | off | Emit JSON |

```console
$ cc-notes task list
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
```

### `cc-notes task ready`

List open, unassigned, unblocked tasks — the pickup queue.

| Flag | Default | Meaning |
|------|---------|---------|
| `--branch <branch>` | current branch | Ready set for a named branch |
| `--backlog` | off | Ready set from the shared backlog |
| `--all-branches` | off | Ready set across every branch, grouped |
| `--json` | off | Emit JSON |

### `cc-notes task backlog`

Shorthand for `task list --backlog --status open` — the open, branch-less queue.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes task backlog
5d3e9c1	open	P2	-	Rotate signing keys quarterly
```

### `cc-notes task show ID`

Show one task: a fixed-order header block (id, branch, title, type, status, priority,
assignee, labels, blocked_by, blocks, parent, created, updated, started, closed), the
description after a blank line, each comment as a `-- <author> <rfc3339>` block, then the
list of commits that implemented it.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task start ID`

Claim the task (deterministic first-wins) and, once the claim is won, set its `Branch` to your
current branch, opening a lease. The one-step "I'm taking this and pulling it onto my branch" — a
lost claim leaves the task on its original branch.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes task start 5d3e9c1
5d3e9c1	in_progress	P2	ada <ada@example.com>	Rotate signing keys quarterly
```

### `cc-notes task claim ID`

Claim an open, unassigned task and move it to in-progress, opening a lease. The fold's claim
rule resolves a race deterministically: first-writer wins on every replica.

| Flag | Default | Meaning |
|------|---------|---------|
| `--steal` | off | Reclaim an in-progress task whose lease has expired; a holder who renewed in time keeps it |
| `--sync` | off | Claim, then sync and re-check, yielding if another agent won the race |
| `--json` | off | Emit JSON |

```console
$ cc-notes task claim d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
```

### `cc-notes task renew ID`

Refresh the lease heartbeat on a task you hold. Use it during long silent stretches so the
claim does not look stale.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task done ID`

Close a task as done and anchor your `HEAD` commit onto it, so `task show` can list the
commits that implemented it.

| Flag | Default | Meaning |
|------|---------|---------|
| `--force` | off | Close even with unmet criteria |
| `--json` | off | Emit JSON |

**`done` is gated on acceptance criteria.** While any criterion is `pending` or `failed`,
`done` lists the unmet ones and refuses to close (exit 2). Mark them `met` (by
hand or via `task validate`), or pass `--force` to close anyway — a force-close sets the
derived `closed_forced` flag in the task's `--json` so the override stays visible.

```console
$ cc-notes task done 286d87c
usage: 286d87c has 2 unmet criterion/criteria (pass --force to close anyway):
  c5144db [pending] go test ./... passes
  f06100e [pending] p99 latency under 200ms
$ cc-notes task criterion met 286d87c c5144db
286d87c	open	P1	-	Add retry backoff to the API client
$ cc-notes task done 286d87c --force
286d87c	done	P1	-	Add retry backoff to the API client
```

### `cc-notes task cancel ID`

Close a task as cancelled.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task move ID --to <branch>`

Set the task's `Branch` — handoff or re-home. A plain attribute write; pass `--backlog`
to move it back to the backlog. `--to` and `--backlog` are mutually exclusive.

| Flag | Default | Meaning |
|------|---------|---------|
| `--to <branch>` | (required unless `--backlog`) | Destination branch |
| `--backlog` | off | Move to the backlog (clear the branch) |
| `--json` | off | Emit JSON |

```console
$ cc-notes task move 5d3e9c1 --to main
5d3e9c1	open	P2	-	Rotate signing keys quarterly
```

### `cc-notes task comment ID BODY`

Append a comment; `BODY` of `-` reads stdin. Any comment refreshes the task's lease.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task dep ID BLOCKER`

Mark `ID` blocked by `BLOCKER`; blocker ids resolve globally. A task with any open blocker
drops out of `ready`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task undep ID BLOCKER`

Remove a blocked-by edge.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task edit ID`

Edit a task without lifecycle transition checks — the escape hatch when the guided verbs do
not fit.

| Flag | Meaning |
|------|---------|
| `--title <text>` | New title |
| `--desc <text>` | New description; `-` reads stdin |
| `--status <status>` | One of `open`, `in_progress`, `done`, `cancelled` |
| `--priority <0-3>` | New priority |
| `--type <type>` | One of `task`, `bug`, `epic`, `question` |
| `--assignee <user>` | Set assignee |
| `--unassign` | Clear the assignee |
| `--add-label` / `--rm-label <label>` | Add or remove a label; repeatable |
| `--parent <id>` | Set parent |
| `--no-parent` | Clear the parent |
| `--sprint <id>` | Join a sprint (id prefix); mutually exclusive with `--no-sprint` |
| `--no-sprint` | Clear the sprint |
| `--project <id>` | Join a project (id prefix); mutually exclusive with `--no-project` |
| `--no-project` | Clear the project |
| `--json` | Emit JSON |

### `cc-notes task criterion`

The criterion subgroup manages a task's structured acceptance criteria — the `pending` /
`met` / `failed` checks that gate `task done`. Every verb addresses a criterion by an id
prefix (`CRIT`, case-insensitive): no match exits 3, an ambiguous prefix exits 5. Criteria
are also editable by id through the task's FUSE JSON file.

| Verb | Args | Meaning |
|------|------|---------|
| `add` | `TASK "TEXT" [--script FILE]` | Add a criterion (`pending`); `--script` stores a file's contents as its check command |
| `rm` | `TASK CRIT` | Remove a criterion |
| `met` | `TASK CRIT` | Mark a criterion `met` |
| `failed` | `TASK CRIT` | Mark a criterion `failed` |
| `reset` | `TASK CRIT` | Reset a criterion to `pending` |
| `script` | `TASK CRIT FILE` \| `TASK CRIT --clear` | Set or clear a criterion's validation script |
| `list` | `TASK [--json]` | List a task's criteria |

Every verb also takes `--json`. The lean `list` line is `<short7-crit-id>` `<status>`
`<text>`.

```console
$ cc-notes task criterion add 286d87c "build succeeds" --script ./scripts/check-build.sh
286d87c	open	P1	-	Add retry backoff to the API client
$ cc-notes task criterion list 286d87c
c5144db	met	go test ./... passes
f06100e	pending	p99 latency under 200ms
40ddcd3	pending	build succeeds
```

### `cc-notes task validate ID`

Run a task's criterion validation scripts locally and record each verdict. This is the only
command that executes stored criterion scripts, and it is **explicit and confirmation-gated**
on purpose: scripts arrive over git sync from other agents and remotes, so running one runs
untrusted code in your working tree. It **never** runs during `sync`, `list`, fold, `done`,
or any render.

| Flag | Default | Meaning |
|------|---------|---------|
| `--yes` | off | Run without the interactive confirmation prompt |
| `--timeout <dur>` | `5m` | Per-script timeout |
| `--json` | off | Emit JSON |

Validate collects the criteria that carry a script, prints each script to stderr first, then
requires consent before running anything: `--yes`, or a `y`/`yes` answer to the interactive
`[y/N]` prompt. A non-terminal stdin without `--yes` is a hard error — a piped or automated
invocation can never run a script silently. Each script runs under `sh -c` in the repo root;
exit 0 marks the criterion `met`, a non-zero exit or a timeout marks it `failed`. A task with
no scripted criteria prints `no criteria have validation scripts` and exits 0.

```console
$ cc-notes task validate 286d87c --yes
criterion 40ddcd3 build succeeds:
go build ./...

40ddcd3 met build succeeds
286d87c	open	P1	-	Add retry backoff to the API client
```

(The `criterion …` preview and the per-script verdict lines go to stderr; the final task line
or `--json` document is stdout.)

### `cc-notes task stale`

List in-progress tasks whose lease has exceeded the threshold — a crashed agent's abandoned
claim. The lean line gains a trailing idle marker. Reclaim one with `task claim <id>
--steal`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--idle-after <dur>` | `cc-notes.leaseTTL` / `CC_NOTES_LEASE_TTL` (default 1h) | Idle threshold |
| `--json` | off | Emit JSON |

```console
$ cc-notes task stale
b932fd9	in_progress	P2	ben <ben@example.com>	test the widget	idle 2h13m
```

### `cc-notes task archived`

List done and cancelled tasks older than the threshold. Archived tasks stay out of default
and `--all` views unless `--include-archived` is passed there.

| Flag | Default | Meaning |
|------|---------|---------|
| `--closed-before <when>` | (TTL default) | Archive cutoff |
| `--json` | off | Emit JSON |

### Lease model

A claim opens a lease. The heartbeat is the `AuthorTime` of the assignee's latest op, so any
edit, comment, or `renew` refreshes it. Staleness is judged by the **reader** against the
TTL threshold — never inside the fold — so it stays deterministic across replicas. Pin
`CC_NOTES_LEASE_TTL` (or `cc-notes.leaseTTL` in git config) per-repo so every agent agrees,
and keep it larger than your sync interval, or a healthy holder behind a slow sync looks
stale.

### JSON task shape

`{"id":string,"branch":string,"title":string,"description":string,"type":string,"status":string,"priority":int,"assignee":string|null,"labels":[…],"blocked_by":[id,…],"blocks":[id,…],"parent":string|null,"comments":[{"author":string,"ts":rfc3339,"body":string}],"commits":[sha,…],"lease":{"holder":string|null,"heartbeat":rfc3339|null},"created_at":rfc3339,"updated_at":rfc3339,"started_at":rfc3339|null,"closed_at":rfc3339|null,"sprint":id|null,"project":id|null,"criteria":[{"id":string,"text":string,"script":string,"status":string}],"closed_forced":bool}`.
`blocks` is the derived reverse index of `blocked_by`; `branch` is `""` for a backlog task.
`sprint` and `project` are the task's independent membership pointers (`null` when unset);
each criterion's `status` is `pending`, `met`, or `failed`, and `script` is `""` when it
carries none; `closed_forced` is `true` only for a `done` task closed with at least one
criterion still unmet.

## Project commands

Projects are long-lived, repo-wide, branch-less groupings of sprints and tasks. A task joins
a project directly with `--project`, or inherits one through its sprint; a project never
points down — `project show` derives both reverse indexes. Status advances from `active` to
one of `completed`, `archived`, or `cancelled`, and the lifecycle verbs only fire from `active`.
Projects resolve by id prefix like tasks. Every command takes `--json`.

### `cc-notes project add TITLE`

Create a project. It is born `active`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--desc <text>` | empty | Description; `-` reads stdin |
| `--label <label>` | none | Label; repeatable |
| `--json` | off | Emit JSON |

```console
$ cc-notes project add "Billing platform" --desc "Revenue and invoicing" --label infra
07daf88	active	Billing platform
```

### `cc-notes project list`

List projects, each as its current folded state. Default lists every status.

| Flag | Default | Meaning |
|------|---------|---------|
| `--status <csv>` | all | Status filter, comma-separated |
| `--json` | off | Emit JSON |

```console
$ cc-notes project list
07daf88	active	Billing platform
```

### `cc-notes project show ID`

Show one project: a fixed-order header block (id, title, status, labels, created, updated,
closed, commits), the description after a blank line, each comment as a `-- <author>
<rfc3339>` block, then a `sprints:` line and a `tasks:` line of short ids. `sprints` is the
sprints pointed at this project; `tasks` is the union of tasks pointed directly at the
project and tasks reached through one of its sprints, deduplicated.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes project show 07daf88
id: 07daf88299b3418c4067425e9e6ce336602eb729
title: Billing platform
status: active
labels: infra
created: 2026-06-16T19:14:07Z
updated: 2026-06-16T19:14:07Z
closed: -
commits: -

Revenue and invoicing
sprints: afd8362
tasks: 286d87c,94c0917
```

### `cc-notes project complete ID` · `archive ID` · `cancel ID`

Move a project to a terminal status. Each fires only from `active`; a project already
`completed`, `archived`, or `cancelled` is a conflict (exit 4).

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes project complete 07daf88
07daf88	completed	Billing platform
```

### `cc-notes project edit ID`

Edit a project without transition checks — at least one flag is required.

| Flag | Meaning |
|------|---------|
| `--title <text>` | New title |
| `--desc <text>` | New description; `-` reads stdin |
| `--add-label` / `--rm-label <label>` | Add or remove a label; repeatable |
| `--json` | Emit JSON |

### `cc-notes project comment ID BODY`

Append a comment; `BODY` of `-` reads stdin.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### JSON project shape

`{"id":string,"title":string,"description":string,"status":string,"labels":[…],"commits":[sha,…],"comments":[{"author":string,"ts":rfc3339,"body":string}],"author":string,"created_at":rfc3339,"updated_at":rfc3339,"closed_at":rfc3339|null,"sprints":[id,…],"tasks":[id,…]}`.
`status` is `active`, `completed`, `archived`, or `cancelled`; `sprints` and `tasks` are the
derived reverse indexes as full-hex ids.

## Sprint commands

Sprints are time-boxed, repo-wide groupings of tasks, optionally within a project. A task
joins a sprint with `--sprint`; a sprint points at an optional project with `--project`.
`sprint show` derives the task reverse index. Status advances from `planned` to `active` to
`completed`, or to `cancelled` from either open state; `start`, `complete`, and `cancel` only
fire from `planned` or `active`. Sprints resolve by id prefix like tasks. Every command takes
`--json`.

### `cc-notes sprint add TITLE`

Create a sprint. It is born `planned`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--desc <text>` | empty | Description; `-` reads stdin |
| `--project <id>` | none | Owning project (id prefix) |
| `--label <label>` | none | Label; repeatable |
| `--start <YYYY-MM-DD>` | none | Start date |
| `--end <YYYY-MM-DD>` | none | End date |
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint add "Sprint 7" --project 07daf88 --start 2026-06-15 --end 2026-06-29
afd8362	planned	Sprint 7
```

### `cc-notes sprint list`

List sprints, each as its current folded state. Default lists every status.

| Flag | Default | Meaning |
|------|---------|---------|
| `--project <id>` | none | Filter to a project (id prefix) |
| `--status <csv>` | all | Status filter, comma-separated |
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint list --project 07daf88
afd8362	planned	Sprint 7
```

### `cc-notes sprint show ID`

Show one sprint: a fixed-order header block (id, project short id, title, status, start_date,
end_date, labels, created, updated, started, closed, commits), the description after a blank
line, each comment as a `-- <author> <rfc3339>` block, then a `tasks:` line of the short ids
of the tasks pointed at this sprint.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint show afd8362
id: afd8362cc6e5a816b3987e9cc7ccd2bca775d397
project: 07daf88
title: Sprint 7
status: planned
start_date: 2026-06-15T00:00:00Z
end_date: 2026-06-29T00:00:00Z
labels: -
created: 2026-06-16T19:14:58Z
updated: 2026-06-16T19:14:58Z
started: -
closed: -
commits: -

Invoicing polish
tasks: 286d87c
```

### `cc-notes sprint start ID` · `complete ID` · `cancel ID`

Advance a sprint's status: `start` sets it `active`, `complete` sets it `completed`, `cancel`
sets it `cancelled`. Each fires only from `planned` or `active`; a sprint already `completed`
or `cancelled` is a conflict (exit 4).

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint start afd8362
afd8362	active	Sprint 7
```

### `cc-notes sprint edit ID`

Edit a sprint without transition checks — at least one flag is required. The `--project`,
`--start`, and `--end` setters each pair with a `--no-*` clearer, and the two are mutually
exclusive.

| Flag | Meaning |
|------|---------|
| `--title <text>` | New title |
| `--desc <text>` | New description; `-` reads stdin |
| `--project <id>` | Set the owning project (id prefix) |
| `--no-project` | Clear the project |
| `--start <YYYY-MM-DD>` | Set the start date |
| `--no-start` | Clear the start date |
| `--end <YYYY-MM-DD>` | Set the end date |
| `--no-end` | Clear the end date |
| `--add-label` / `--rm-label <label>` | Add or remove a label; repeatable |
| `--json` | Emit JSON |

### `cc-notes sprint comment ID BODY`

Append a comment; `BODY` of `-` reads stdin.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### JSON sprint shape

`{"id":string,"project":id|null,"title":string,"description":string,"status":string,"start_date":rfc3339|null,"end_date":rfc3339|null,"labels":[…],"commits":[sha,…],"comments":[{"author":string,"ts":rfc3339,"body":string}],"author":string,"created_at":rfc3339,"updated_at":rfc3339,"started_at":rfc3339|null,"closed_at":rfc3339|null,"tasks":[id,…]}`.
`status` is `planned`, `active`, `completed`, or `cancelled`; `start_date`/`end_date` are the
user-set dates (`null` when unset); `tasks` is the derived reverse index as full-hex ids.

## Note commands

Notes are repo-global with optional commit, path, and branch anchors. The `*-tag` and
anchor flags are repeatable arrays. Drift, verification, and supersession are first-class:
drift is computed against the recorded witness, and supersession is a real edge — not a tag
convention.

### `cc-notes note add TITLE`

Create a note. A note is born verified against `HEAD`: its anchors get a content witness at
creation.

| Flag | Default | Meaning |
|------|---------|---------|
| `--body <text>` | empty | Note body; `-` reads stdin |
| `--tag <tag>` | none | Tag; repeatable |
| `--commit <sha>` | none | Commit anchor; repeatable |
| `--path <path>` | none | Path anchor; repeatable |
| `--branch <branch>` | none | Branch anchor; repeatable |
| `--json` | off | Emit JSON |

```console
$ cc-notes note add "Auth tokens expire after 15 minutes" --path services/auth/login.go --tag design --body "Refresh client-side before expiry; the API returns 401 with no Retry-After header."
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes
```

### `cc-notes note edit ID`

Edit a note. Title and body replace; anchors and tags add or remove individually.

| Flag | Meaning |
|------|---------|
| `--title <text>` | New title |
| `--body <text>` | New body; `-` reads stdin |
| `--add-tag` / `--rm-tag <tag>` | Add or remove a tag; repeatable |
| `--add-commit` / `--rm-commit <sha>` | Add or remove a commit anchor; repeatable |
| `--add-path` / `--rm-path <path>` | Add or remove a path anchor; repeatable |
| `--add-branch` / `--rm-branch <branch>` | Add or remove a branch anchor; repeatable |
| `--json` | Emit JSON |

### `cc-notes note verify ID`

Record that the note is still true as of now, refreshing the witness against the current
content of its anchors. This is how a note re-earns "fresh" after `note review` flags it.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes note verify ebba9fb
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

### `cc-notes note supersede OLD --by NEW`

Record that `NEW` replaces `OLD`. `OLD` drops from default listings and points at `NEW`;
history is preserved. `--remove` undoes the edge.

| Flag | Default | Meaning |
|------|---------|---------|
| `--by <id>` | (required) | The replacement note |
| `--remove` | off | Remove the supersede edge |
| `--json` | off | Emit JSON |

```console
$ cc-notes note supersede ebba9fb --by 7a3f10c
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

### `cc-notes note review`

Surface notes needing attention, each with a verdict appended to the lean line: `DRIFTED`
(an anchored path or commit changed since the note was verified), `STALE` (verified too long
ago), `UNVERIFIED` (never verified), and dangling supersede edges.

| Flag | Default | Meaning |
|------|---------|---------|
| `--stale-after <dur>` | (config default) | Staleness threshold |
| `--drift` | off | Limit to drifted notes |
| `--unverified` | off | Limit to never-verified notes |
| `--json` | off | Emit JSON |

```console
$ cc-notes note review
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes	DRIFTED
```

### `cc-notes note list`

List notes. Default drops superseded and tombstoned notes.

| Flag | Default | Meaning |
|------|---------|---------|
| `--tag <tag>` | none | Require tag; repeatable, ANDed |
| `--commit <sha>` | none | Require commit anchor |
| `--path <path>` | none | Require path anchor |
| `--branch <branch>` | none | Require branch anchor |
| `--all` | off | Include tombstoned notes |
| `--include-superseded` | off | Include superseded notes |
| `--json` | off | Emit JSON |

### `cc-notes note search QUERY`

Ranked full-text search (title > tags > body, with a recency boost), bounded and scopable.

| Flag | Default | Meaning |
|------|---------|---------|
| `--tag <tag>` | none | Require tag; repeatable, ANDed |
| `--limit <N>` | (default cap) | Maximum results |
| `--author <user>` | none | Require author |
| `--anchor-path <path>` | none | Require path anchor |
| `--anchor-branch <branch>` | none | Require branch anchor |
| `--anchor-commit <sha>` | none | Require commit anchor |
| `--json` | off | Emit JSON |

```console
$ cc-notes note search "token expiry" --tag design
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes
```

### `cc-notes note show ID`

Show one note: a fixed-order header block (id, title, tags, anchors, author, created,
updated, verified_at/by, superseded_by, supersedes, drift verdict) then the body after a
blank line. `supersedes` is a text-only field — the computed reverse index of
`superseded_by` — with no JSON counterpart, so don't parse it from `--json`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes note rm ID`

Tombstone a note. It drops out of listings; history survives.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### JSON note shape

`{"id":string,"title":string,"body":string,"tags":[…],"anchors":[{"kind":string,"value":string,"witness":string|null}],"author":string,"created_at":rfc3339,"updated_at":rfc3339,"verified_at":rfc3339|null,"verified_by":string|null,"superseded_by":string|null,"drift":string|null,"deleted":bool}`.
`drift` is the computed verdict (`null` when fresh); `superseded_by` is the replacement note
id or `null`.
