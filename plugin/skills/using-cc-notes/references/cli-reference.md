# cc-notes CLI reference

The complete command surface, grouped by noun. Every command takes `-h`/`--help`. Note,
task, sync, and reconcile commands take `--json` for a machine-readable record; without
it, mutations echo a lean tab-separated line and listings print one lean line per entity.

## Lean-line formats

| Entity | Fields (tab-separated) |
|--------|------------------------|
| Task | `<short7-id>` `<status>` `P<priority>` `<assignee\|->` `<title>` |
| Note | `<short7-id>` `<YYYY-MM-DD updated, UTC>` `<tags csv\|->` `<title>` |

Short ids are the first 7 hex chars; `-` stands in for an empty field. JSON output uses
full 40-hex ids, RFC3339 UTC timestamps, `null` for unset optionals, and sorted set
slices.

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

Converge `refs/cc-notes/*` with a remote and push. Fetches, union-merges divergent refs
(never clobbers), and pushes, looping until stable. The lean report prints only nonzero
verbs followed by the round count.

| Flag | Default | Meaning |
|------|---------|---------|
| `--remote <name>` | `origin` | Remote to sync with |
| `--json` | off | Emit JSON |

```console
$ cc-notes sync
pushed: 2
rounds: 1
```

JSON shape: `{"created":int,"fast_forwarded":int,"merged":int,"pushed":int,"rounds":int}`.

### `cc-notes reconcile`

Promote a merged branch's open and in-progress tasks into a target branch. Tasks scope to
the branch they were created on (`refs/cc-notes/tasks/<branch>/*`); after a git or jj merge
they stay on the merged branch's namespace and are invisible on the target until reconcile
moves them. Run it after merging.

| Flag | Default | Meaning |
|------|---------|---------|
| `--into <branch>` | current branch | Target branch to promote tasks into |
| `--from <branch>` | (auto-discover) | Restrict to named source branches; repeatable. Deterministic — use in CI |
| `--force` | off | Skip the merge test; only valid with `--from`. For squash-merges and deleted branches |
| `--dry-run` | off | Report what would be promoted without writing |
| `--json` | off | Emit JSON |

Reconcile auto-discovers branches fully merged into the target. `--from` narrows scanning
to named branches for a deterministic CI run; `--force` (only with `--from`) skips the
merge-ancestry test so squash-merged or already-deleted branches still promote. It is
idempotent — safe to re-run and to run in CI. It works under both git and jj; jj never
fires git hooks, which is why promotion is an explicit command rather than a merge hook.

```console
$ cc-notes reconcile --into main
scanned: 1
merged: 1
promoted: 2
into: main
feature/x:
08118da	open	P1	-	build the widget
b932fd9	open	P2	-	test the widget
```

JSON shape:
`{"into":string,"scanned":int,"merged":int,"promoted":int,"branches":[{"branch":string,"merged":bool,"reason":string,"tasks":[full-id,…]}]}`.
`reason` is empty when the branch's tasks were promoted and names the skip cause otherwise.

### `cc-notes mount [MOUNTPOINT]`

Mount notes and tasks as an editable filesystem in the foreground; Ctrl-C unmounts. Notes
render as Markdown, tasks as JSON; edits and diffs flow back into the object database.
`MOUNTPOINT` is optional and defaults to a managed directory when omitted. Requires a
`_fuse` binary plus a FUSE implementation (`fuse-t` on macOS, `fuse3` on Linux).

```console
$ cc-notes mount ./cc-notes-fs
```

### `cc-notes version`

Print the cc-notes version.

```console
$ cc-notes version
v0.2.0 (dd02f2d)
```

## Note commands

Notes are repo-global with optional commit, path, and branch anchors. The `*-anchor` and
`*-tag` flags are repeatable arrays.

### `cc-notes note add TITLE`

Create a note.

| Flag | Default | Meaning |
|------|---------|---------|
| `--body <text>` | empty | Note body; `-` reads stdin |
| `--tag <tag>` | none | Tag; repeatable |
| `--commit <sha>` | none | Commit anchor; repeatable |
| `--path <path>` | none | Path anchor; repeatable |
| `--branch <branch>` | none | Branch anchor; repeatable |
| `--json` | off | Emit JSON |

```console
$ cc-notes note add "Auth tokens expire after 15 minutes" --path services/auth/login.go --tag design
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

### `cc-notes note list`

List notes. Anchor and tag filters narrow the set; `--tag` is repeatable and ANDed.

| Flag | Default | Meaning |
|------|---------|---------|
| `--tag <tag>` | none | Require tag; repeatable, ANDed |
| `--commit <sha>` | none | Require commit anchor |
| `--path <path>` | none | Require path anchor |
| `--branch <branch>` | none | Require branch anchor |
| `--all` | off | Include tombstoned notes |
| `--json` | off | Emit JSON |

### `cc-notes note search QUERY`

Search notes by title, body, and tags.

| Flag | Default | Meaning |
|------|---------|---------|
| `--tag <tag>` | none | Require tag; repeatable, ANDed |
| `--json` | off | Emit JSON |

### `cc-notes note show ID`

Show one note: a fixed-order header block (id, title, tags, commits, paths, branches,
author, created, updated, and `deleted` when tombstoned) then the body after a blank line.

| Flag | Meaning |
|------|---------|
| `--json` | Emit JSON |

### `cc-notes note rm ID`

Tombstone a note. The note drops out of listings; history survives. `--json` emits JSON.

JSON note shape:
`{"id":string,"title":string,"body":string,"tags":[…],"anchors":[{"kind":…,"value":…}],"author":string,"created_at":rfc3339,"updated_at":rfc3339,"deleted":bool}`.

## Task commands

Tasks are branch-scoped, with claiming, dependencies, and a lifecycle. Every task command
takes `--branch <branch>` (default: current branch) to act on another branch's namespace.

### `cc-notes task add TITLE`

Create a task.

| Flag | Default | Meaning |
|------|---------|---------|
| `--priority <0-3>` | `2` | Priority; 0 is most urgent |
| `--type <type>` | `task` | One of `task`, `bug`, `epic`, `question` |
| `--label <label>` | none | Label; repeatable |
| `--desc <text>` | empty | Description; `-` reads stdin |
| `--parent <id-prefix>` | none | Parent task (id prefix) |
| `--blocked-by <id-prefix>` | none | Blocker task id prefix; repeatable, resolved globally |
| `--branch <branch>` | current branch | Task branch |
| `--json` | off | Emit JSON |

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1 --label api
d82c087	open	P1	-	Add retry backoff to the API client
```

### `cc-notes task list`

List tasks on a branch. Defaults to open and in-progress.

| Flag | Default | Meaning |
|------|---------|---------|
| `--status <csv>` | `open,in_progress` | Status filter, comma-separated |
| `--all` | off | Every status |
| `--assignee <user>` | none | Require assignee |
| `--label <label>` | none | Require label; repeatable, ANDed |
| `--type <type>` | none | Require type |
| `--branch <branch>` | current branch | Branch to list |
| `--json` | off | Emit JSON |

### `cc-notes task ready`

List unblocked, unassigned, open tasks — the pickup queue. `--branch` and `--json` apply.

### `cc-notes task show ID`

Show one task: a fixed-order header block (id, branch, title, type, status, priority,
assignee, labels, blocked_by, blocks, parent, created, updated, started, closed), the
description after a blank line, then each comment as a `-- <author> <rfc3339>` block.
`--branch` and `--json` apply.

### `cc-notes task claim ID`

Claim an open, unassigned task; sets the assignee to the git user and moves it to
in-progress. `--branch` and `--json` apply.

### `cc-notes task done ID`

Mark a task done. `--branch` and `--json` apply.

### `cc-notes task cancel ID`

Mark a task cancelled. `--branch` and `--json` apply.

### `cc-notes task comment ID BODY`

Append a comment; `BODY` of `-` reads stdin. `--branch` and `--json` apply.

### `cc-notes task dep ID BLOCKER`

Mark task `ID` blocked by `BLOCKER`. A task with open blockers drops out of `ready`.
`--branch` and `--json` apply.

### `cc-notes task undep ID BLOCKER`

Remove a blocked-by dependency. `--branch` and `--json` apply.

### `cc-notes task edit ID`

Edit a task without lifecycle transition checks — the escape hatch when the guided verbs
do not fit.

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
| `--parent <id-prefix>` | Set parent (id prefix) |
| `--no-parent` | Clear the parent |
| `--branch <branch>` | Branch |
| `--json` | Emit JSON |

### `cc-notes task promote`

Move tasks to another branch namespace. `--to` is required; bare ids restrict the move to
those tasks, otherwise the whole source branch promotes. Lower-level than `reconcile`,
which auto-discovers merged branches and promotes only open and in-progress tasks.

| Flag | Default | Meaning |
|------|---------|---------|
| `--to <branch>` | (required) | Destination branch |
| `--from <branch>` | current branch | Source branch |
| `[ID]...` | all | Restrict to these task ids |
| `--json` | off | Emit JSON |

JSON task shape:
`{"id":string,"branch":string,"title":string,"description":string,"type":string,"status":string,"priority":int,"assignee":string|null,"labels":[…],"blocked_by":[id,…],"blocks":[id,…],"parent":string|null,"comments":[{"author":string,"ts":rfc3339,"body":string}],"created_at":rfc3339,"updated_at":rfc3339,"started_at":rfc3339|null,"closed_at":rfc3339|null}`.
`blocks` is the derived reverse index of `blocked_by`.
