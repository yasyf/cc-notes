# cc-notes CLI reference

The complete command surface, grouped by noun: repo, task, note. Every command takes
`-h`/`--help`, and every note, task, sync, and reconcile command takes `--json` for a
machine-readable record. Without `--json`, mutations echo a lean tab-separated line and
listings print one lean line per entity.

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
  are reader filters on `list`/`ready` and setters on `add`/`move`/`edit`.
- **Notes are repo-global** with optional commit, path, and branch anchors pointing at the
  code they describe. A note records when it was last verified true; a superseded note
  points at its replacement and drops out of default listings.
- **Identity.** Writes are signed by `CC_NOTES_ACTOR` (`"Name <email>"`) when set, else by
  git `user.name`/`user.email`. The claim and lease primitives key on this actor.

## Lean-line formats

| Entity | Fields (tab-separated) |
|--------|------------------------|
| Task | `<short7-id>` `<status>` `P<priority>` `<assignee\|->` `<title>` |
| Note | `<short7-id>` `<YYYY-MM-DD updated, UTC>` `<tags csv\|->` `<title>` |

Short ids are the first 7 hex chars; `-` stands in for an empty field. `task stale` appends
a trailing idle marker to the task line; `note review` appends a verdict to the note line.
JSON output uses full 40-hex ids, RFC3339 UTC timestamps, `null` for unset optionals, and
sorted set slices.

## Repo commands

### `cc-notes init`

Install the `refs/cc-notes/*` refspecs on a remote. Run once per repo. After init, plain
`git push` and `git pull` carry the cc-notes refs alongside your branches.

| Flag | Default | Meaning |
|------|---------|---------|
| `--remote <name>` | `origin` | Remote to wire |

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

Mount notes and tasks as an editable filesystem in the foreground; Ctrl-C unmounts. Notes
render as Markdown, tasks as JSON; edits and diffs flow back into the object database.
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
| `--parent <id>` | none | Parent task id |
| `--blocked-by <id>` | none | Blocker task id; repeatable, resolved globally |
| `--branch <branch>` | current branch | Set the task's branch explicitly |
| `--backlog` | off | Set `Branch=""` (shared, branch-less) |
| `--json` | off | Emit JSON |

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1 --label api
d82c087	open	P1	-	Add retry backoff to the API client
$ cc-notes task add "Rotate signing keys quarterly" --backlog
5d3e9c1	open	P2	-	Rotate signing keys quarterly
```

### `cc-notes task list`

List tasks. Defaults to open and in-progress on your current branch. `--all-branches` and
`--backlog` group output by branch.

| Flag | Default | Meaning |
|------|---------|---------|
| `--status <csv>` | `open,in_progress` | Status filter, comma-separated |
| `--all` | off | Every status |
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

Claim the task and set its `Branch` to your current branch, atomically, opening a lease. The
one-step "I'm taking this and pulling it onto my branch."

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
| `--json` | off | Emit JSON |

```console
$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

### `cc-notes task cancel ID`

Close a task as cancelled.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task move ID --to <branch>`

Set the task's `Branch` — handoff or re-home. A plain attribute write; pass an empty value
to move it back to the backlog.

| Flag | Default | Meaning |
|------|---------|---------|
| `--to <branch>` | (required) | Destination branch (empty string = backlog) |
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
| `--branch <branch>` | Set the task's branch |
| `--json` | Emit JSON |

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

`{"id":string,"branch":string,"title":string,"description":string,"type":string,"status":string,"priority":int,"assignee":string|null,"labels":[…],"blocked_by":[id,…],"blocks":[id,…],"parent":string|null,"comments":[{"author":string,"ts":rfc3339,"body":string}],"commits":[sha,…],"lease":{"holder":string|null,"heartbeat":rfc3339|null},"created_at":rfc3339,"updated_at":rfc3339,"started_at":rfc3339|null,"closed_at":rfc3339|null}`.
`blocks` is the derived reverse index of `blocked_by`; `branch` is `""` for a backlog task.

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
7a3f10c	2026-06-16	design	Auth tokens expire after 30 minutes
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
updated, verified_at/by, superseded_by, drift verdict) then the body after a blank line.

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
