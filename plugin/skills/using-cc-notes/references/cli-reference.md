# cc-notes CLI reference

The command surface, grouped by noun: repo, task, sprint, project, note. Every command takes
`-h`/`--help`. Every note, task, sprint, project, sync, and reconcile command takes `--json`
for a machine-readable record; without it, mutations echo a lean tab-separated line and
listings print one lean line per entity.

Sprints and projects are an optional planning layer over tasks — group work into a time-boxed
sprint or a long-lived project without touching the canonical task and note flow. A task that
joins neither behaves exactly as a task does today.

## The model in one screen

- **Tasks are global.** A task lives at a flat `refs/cc-notes/tasks/<id>` — one ref per task,
  like a note. Its branch is a mutable last-write-wins attribute (the `Branch` field), not part
  of its identity.
- **Your branch's tasks** are the ones whose `Branch` equals your current `HEAD`. `task list`
  and `task ready` default to that scope.
- **The backlog** is the shared queue of work pinned to no branch (`Branch == ""`), visible to
  every agent on every branch. Create one with `task add --backlog`; pull from it with
  `task backlog`.
- **Moving a task to a branch** is an attribute write: `task move <id> --to <branch>`, or
  automatically when you `task start` it.
- **Ids are global.** Every id-addressed command (`show`, `claim`, `start`, `done`, `edit`,
  `comment`, `dep`, `move`, `renew`, …) resolves by id alone — there is no `--branch` flag on
  id-addressed commands. `--branch`, `--backlog`, and `--all-branches` are reader filters on
  `list`/`ready` and setters on `add`/`move`.
- **Notes are repo-global** with optional commit, path, directory, and branch anchors pointing at
  the code they describe. A directory anchor covers a subtree and drifts on any change beneath it. A
  note records when it was last verified true; a superseded note points at its replacement and drops
  out of default listings.
- **Identity.** Writes are signed by `CC_NOTES_ACTOR` (`"Name <email>"`) when set, else by git
  `user.name`/`user.email`. The claim and lease primitives key on this actor.
- **Sprints and projects are repo-wide.** A task carries an independent sprint pointer and
  project pointer — both optional, both last-write-wins, both applied across the whole
  repository, not per branch. A sprint carries an optional project pointer. Membership is always
  an upward pointer; "tasks in a sprint", "sprints in a project", and "tasks in a project"
  (direct ∪ via-sprint, deduplicated) are derived reverse indexes, never stored. Sprint ids,
  project ids, and criterion ids resolve by id prefix like tasks — an ambiguous prefix exits 5,
  no match exits 3.

## Lean-line formats

| Entity | Fields (tab-separated) |
|--------|------------------------|
| Task | `<short7-id>` `<status>` `P<priority>` `<assignee\|->` `<title>` |
| Sprint | `<short7-id>` `<status>` `<title>` |
| Project | `<short7-id>` `<status>` `<title>` |
| Note | `<short7-id>` `<YYYY-MM-DD updated, UTC>` `<tags csv\|->` `<title>` |

Short ids are the first 7 hex chars; `-` stands in for an empty field. A criterion's short id is
the first 7 hex chars of its 32-hex nonce; `task criterion list` and the validation logs print
`<short7-crit-id>` `<status>` `<text>`. `task stale` appends a trailing idle marker to the task
line; `note review` appends a verdict to the note line. JSON output uses full 40-hex ids,
RFC3339 UTC timestamps, `null` for unset optionals, and sorted set slices.

## Repo commands

### `cc-notes init`

Set up cc-notes in a repository — run once per repo. Installs the `refs/cc-notes/*` fetch and
push refspecs, then does everything the repo is ready for. After init, plain `git push` and
`git pull` carry the cc-notes refs alongside your branches.

When a `.claude/` directory exists, init registers the cc-notes plugin in
`.claude/settings.json` and enables the cc-notes capt-hook pack (via `capt-hook pack add`). When
a `.github/` directory exists, it installs the reconcile CI workflow. init never creates
`.claude/` — it wires Claude Code only when the repo already uses it.

Under jj, `jj git push`/`jj git fetch` bridge only `refs/heads/*` and leave the
`refs/cc-notes/*` refs behind; run `cc-notes sync` (it drives the git binary directly, carrying
the refs regardless of front-end) or real `git push`/`git pull`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--remote <name>` | `origin` | Remote to wire |
| `--ci` | auto | Force-install the reconcile workflow even without a `.github/` directory (installed by default when `.github/` exists); mutually exclusive with `--no-ci` |
| `--no-ci` | off | Skip the reconcile workflow even when a `.github/` directory exists |
| `--hook` | off | Also install a git post-merge hook running `cc-notes reconcile` (git-only; skipped by jj, rebase, and server-side squash) |

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
registered: cc-notes plugin in .claude/settings.json
```

### `cc-notes sync`

Converge `refs/cc-notes/*` with a remote and push. Fetches, union-merges divergent event logs
(never clobbers), and pushes, looping until stable. The lean report prints only nonzero verbs
followed by the round count.

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

A sectioned, read-only view to orient before picking up work:

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

Reconcile auto-discovers branches fully merged into the target — a branch whose tip is an
ancestor of the target tip. `--from` narrows to named branches for a deterministic CI run;
`--force` (only with `--from`) skips the ancestry test so squash-merged or already-deleted
branches still carry over. It is idempotent — safe to re-run and to run in CI — and works under
both git and jj; jj never fires git hooks, which is why this is an explicit command.

| Flag | Default | Meaning |
|------|---------|---------|
| `--into <branch>` | current branch | Target branch to carry tasks onto |
| `--from <branch>` | (auto-discover) | Restrict to named source branches; repeatable. Deterministic — use in CI |
| `--force` | off | Skip the ancestry test; only valid with `--from`. For squash-merges and deleted branches |
| `--dry-run` | off | Report what would change without writing |
| `--json` | off | Emit JSON |

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
`reason` is empty when the branch's tasks were carried over and names the skip cause otherwise.

### `cc-notes relevant PATH`

Surface the notes most relevant to a file an agent is about to edit, ranked by a summed score with
the matched reasons attached. It reads the live note set, scores every note against `PATH`, the
current branch, and `HEAD`, drops zero-score notes, and orders by score descending, then `updated`
descending, then id ascending. The lean line is the note line followed by a tab, the matched
reasons as a comma-separated list, and — when the note has a verdict — a final tab and the drift
verdict.

| Signal | Reason | Fires when |
|--------|--------|------------|
| path | `path` | A path anchor equals `PATH` |
| dir | `dir` | A directory anchor covers `PATH` (the deepest covering anchor only, so overlapping dir anchors do not stack) |
| branch | `branch` | A branch anchor names the weighed branch |
| merged commit | `merged-commit` | A commit anchor is an ancestor of `HEAD` — its work has merged into the current history |
| merged branch | `merged-branch` | A branch anchor names another branch whose tip has merged into `HEAD` |
| sibling | `sibling` | A path anchor names a different file in `PATH`'s directory |
| cross-author | `cross-author` | A note already matched near `PATH` is anchored to a file a teammate edited in `base..HEAD` but you have not; it never creates a match on its own |

Reasons render in that fixed priority order. The cross-author boost only fires on a note already
matched via a path, dir, or sibling anchor, so it sharpens an existing match and never opens a new
one.

| Flag | Default | Meaning |
|------|---------|---------|
| `--branch <branch>` | current HEAD branch | Branch to weigh against; a detached HEAD skips branch signals |
| `--base <ref>` | remote default branch, else `main` | Merge-base reference for the cross-author range |
| `--limit <N>` | `10` | Maximum results; negative is unlimited |
| `--attached` | off | Keep only notes anchored to `PATH` or a parent directory (a `path` or `dir` reason) |
| `--worktree` | off | Drift-check path anchors against the on-disk working-tree blob, surfacing uncommitted edits |
| `--json` | off | Emit JSON |

```console
$ cc-notes relevant services/auth/login.go
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes	path,branch	DRIFTED
```

JSON shape:
`[{"note":{<note shape>},"score":int,"reasons":[string,…]}]`. Each `note` is the full note document
(carrying its `drift` verdict); `score` is the summed signal weight; `reasons` are the matched
reason labels in fixed priority order.

### `cc-notes blame <sha>`

List the task(s) a commit implemented, resolved from the commit's `cc-task:` trailer and from
task commit-anchors. This is the reverse of the commit list `task show` prints.

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

Local maintenance. By default it tidies local state; with `--prune-remote` it physically deletes
tombstoned entity refs on the remote via `git push --delete`. Pruning is best-effort and
non-convergent — a stale clone can re-advertise a pruned ref — so it is opt-in and never part of
normal sync.

| Flag | Default | Meaning |
|------|---------|---------|
| `--prune-remote` | off | Physically delete tombstoned refs on the remote |
| `--json` | off | Emit JSON |

### `cc-notes mount [MOUNTPOINT]`

Mount the repository's notes and tasks as an editable filesystem. Notes render as Markdown under
`/notes`; tasks, sprints, and projects render as JSON under `/tasks`, `/sprints`, and
`/projects`, with edits and diffs flowing back into the object database. A read-only nested tree
of symlinks under `/projects` and `/sprints` walks the hierarchy; the [sprints and projects
reference](sprints-and-projects.md) covers it. Requires a `_fuse` binary plus a FUSE
implementation (`fuse-t` on macOS, `fuse3` on Linux).

`mount` detaches by default: a background mount holder serves the mount, the command prints the
path and returns, and the mount persists after the command exits. With no `MOUNTPOINT` the mount
is served at a managed per-repo default under `~/.cc-notes/mnt` and presented in the repo as a
`.notes` symlink into it (`cd .notes` to browse); the symlink is kept out of git via
`.git/info/exclude`, never the tracked `.gitignore`, so the live mount stays out of the working
tree. Pass an explicit `MOUNTPOINT` to serve there instead — it is created if missing and no
symlink is made. Tear down with `mount --stop .notes` (or `--stop DIR`) or plain `umount`; `--stop`
and `--shutdown` remove the `.notes` symlink they created. The holder-management modes (`--list`,
`--shutdown`, `--stop`) are mutually exclusive with each other, with a `MOUNTPOINT`, and with
`--foreground`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--foreground` / `-f` | off | Serve in the foreground and unmount on Ctrl-C (bypasses the holder) |
| `--stop <DIR>` | none | Unmount the mount at `DIR` (or the `.notes` symlink), then exit |
| `--shutdown` | off | Unmount everything and stop the mount holder, then exit |
| `--list` | off | List the mounts the holder serves, then exit |

```console
$ cc-notes mount
/abs/path/to/repo/.notes
$ cc-notes mount --stop .notes
cc-notes: unmounted /Users/me/.cc-notes/mnt/repo-1a2b3c4d
```

### `cc-notes version`

Print the cc-notes version.

```console
$ cc-notes version
v0.5.0 (dd02f2d)
```

## Setup commands

Wire the Claude Code integration into a repository. Most repos get all of this from
`cc-notes init`; these are the granular pieces.

### `cc-notes skills install`

Register the cc-notes plugin in `.claude/settings.json` — shallow-merge the cc-notes marketplace
(`yasyf/cc-notes`) and the `cc-notes@cc-notes` plugin into the committed settings, preserving
every other key. The `using-cc-notes` skill then loads from the plugin (tracking the repository)
on folder-trust instead of being copied into `.claude/skills/`. Pass `--global` to enable the
plugin in the user-global `~/.claude/settings.json` instead of the repo.

### `cc-notes hooks install`

Enable the cc-notes capt-hook pack. Runs `uvx capt-hook pack add
github:yasyf/cc-notes@latest`, which caches the pack tarball, records `[packs.cc-notes]` in
`.claude/hooks/packs.toml`, and wires the events into `.claude/settings.local.json`. The source
tracks `@latest` (unpinned) so `uvx capt-hook pack update` carries pack fixes without re-running
install. The pack manifest lives at `.claude/capt-hook.toml`. Takes no flags.

### `cc-notes workflows install`

Install the cc-notes CI workflow — a GitHub Actions job that runs `cc-notes reconcile` against
the default branch on every push to it, using the release binary. This is what `cc-notes init`
writes when `.github/` exists (or `cc-notes init --ci` forces); install it standalone here.
Requires cc-notes >= 0.3.0.

| Flag | Default | Meaning |
|------|---------|---------|
| `--dir <path>` | `.github/workflows` | Destination directory, relative to the repo root |

## Task commands

Tasks are global, addressed by id. Id-addressed commands take no `--branch`. `--branch`,
`--backlog`, and `--all-branches` are reader filters on `list`/`ready` and setters on
`add`/`move`/`edit`.

### `cc-notes task add TITLE`

Create a task. `Branch` defaults to your current branch; `--backlog` sets it to `""`; `--branch`
sets it explicitly.

| Flag | Default | Meaning |
|------|---------|---------|
| `--priority <0-3>` | `2` | Priority; 0 is most urgent |
| `--type <type>` | `task` | One of `task`, `bug`, `epic`, `question` |
| `--label <label>` | none | Label; repeatable |
| `--desc <text>` | empty | Description; `-` reads stdin |
| `--criterion <text>` | none | Acceptance criterion; repeatable, required by default (see below) |
| `--no-validation-criteria` | off | Create with no criteria; mutually exclusive with `--criterion` |
| `--parent <id>` | none | Parent task id |
| `--sprint <id>` | none | Join a sprint (id prefix) |
| `--project <id>` | none | Join a project directly (id prefix) |
| `--blocked-by <id>` | none | Blocker task id; repeatable, resolved globally |
| `--branch <branch>` | current branch | Set the task's branch explicitly |
| `--backlog` | off | Set `Branch=""` (shared, branch-less) |
| `--json` | off | Emit JSON |

Acceptance criteria are required by default: a `task add` with no `--criterion` and no
`--no-validation-criteria` is a usage error (exit 2) — the default nudges every task to record
what "done" means up front. Pass `--no-validation-criteria` to opt out explicitly; it cannot be
combined with `--criterion`. Each criterion starts `pending`; it gates `task done` and is driven
by the `task criterion` subgroup below.

`--sprint` and `--project` are independent: a task may join a sprint, a project, both, or
neither. Joining a sprint leaves the project pointer untouched; `project show` still counts the
task through its sprint.

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

Show one task: a fixed-order header block (id, branch, title, type, status, priority, assignee,
labels, blocked_by, blocks, parent, created, updated, started, closed), the description after a
blank line, each comment as a `-- <author> <rfc3339>` block, then the list of commits that
implemented it.

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

Claim an open, unassigned task and move it to in-progress, opening a lease. The fold's claim rule
resolves a race deterministically: first-writer wins on every replica.

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

Refresh the lease heartbeat on a task you hold. Use it during long silent stretches so the claim
does not look stale.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task done ID`

Close a task as done and anchor your `HEAD` commit onto it, so `task show` can list the commits
that implemented it.

`done` is gated on acceptance criteria. While any criterion is `pending` or `failed`, `done`
lists the unmet ones and refuses to close (exit 2). Mark them `met` (by hand or via
`task validate`), or pass `--force` to close anyway — a force-close sets the derived
`closed_forced` flag in the task's `--json` so the override stays visible.

| Flag | Default | Meaning |
|------|---------|---------|
| `--force` | off | Close even with unmet criteria |
| `--json` | off | Emit JSON |

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

Set the task's `Branch` — handoff or re-home. An attribute write; pass `--backlog` to move it
back to the backlog. `--to` and `--backlog` are mutually exclusive.

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

Mark `ID` blocked by `BLOCKER`; blocker ids resolve globally. A task with any open blocker drops
out of `ready`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task undep ID BLOCKER`

Remove a blocked-by edge.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### `cc-notes task edit ID`

Edit a task without lifecycle transition checks — the escape hatch when the guided verbs do not
fit.

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

The criterion subgroup manages a task's structured acceptance criteria — the `pending` / `met` /
`failed` checks that gate `task done`. Every verb addresses a criterion by an id prefix (`CRIT`,
case-insensitive): no match exits 3, an ambiguous prefix exits 5. Criteria are also editable by
id through the task's FUSE JSON file. Every verb takes `--json`; the lean `list` line is
`<short7-crit-id>` `<status>` `<text>`.

| Verb | Args | Meaning |
|------|------|---------|
| `add` | `TASK "TEXT" [--script FILE]` | Add a criterion (`pending`); `--script` stores a file's contents as its check command |
| `rm` | `TASK CRIT` | Remove a criterion |
| `met` | `TASK CRIT` | Mark a criterion `met` |
| `failed` | `TASK CRIT` | Mark a criterion `failed` |
| `reset` | `TASK CRIT` | Reset a criterion to `pending` |
| `script` | `TASK CRIT FILE` \| `TASK CRIT --clear` | Set or clear a criterion's validation script |
| `list` | `TASK [--json]` | List a task's criteria |

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
command that executes stored criterion scripts, and it is explicit and confirmation-gated on
purpose: scripts arrive over git sync from other agents and remotes, so running one runs
untrusted code in your working tree. It never runs during `sync`, `list`, fold, `done`, or any
render.

Validate collects the criteria that carry a script, prints each script to stderr first, then
requires consent before running anything: `--yes`, or a `y`/`yes` answer to the interactive
`[y/N]` prompt. A non-terminal stdin without `--yes` is a hard error — a piped or automated
invocation can never run a script silently. Each script runs under `sh -c` in the repo root; exit
0 marks the criterion `met`, a non-zero exit or a timeout marks it `failed`. A task with no
scripted criteria prints `no criteria have validation scripts` and exits 0.

| Flag | Default | Meaning |
|------|---------|---------|
| `--yes` | off | Run without the interactive confirmation prompt |
| `--timeout <dur>` | `5m` | Per-script timeout |
| `--json` | off | Emit JSON |

```console
$ cc-notes task validate 286d87c --yes
criterion 40ddcd3 build succeeds:
go build ./...

40ddcd3 met build succeeds
286d87c	open	P1	-	Add retry backoff to the API client
```

The `criterion …` preview and the per-script verdict lines go to stderr; the final task line or
`--json` document is stdout.

### `cc-notes task stale`

List in-progress tasks whose lease has exceeded the threshold — a crashed agent's abandoned
claim. The lean line gains a trailing idle marker. Reclaim one with `task claim <id> --steal`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--idle-after <dur>` | `cc-notes.leaseTTL` / `CC_NOTES_LEASE_TTL` (default 1h) | Idle threshold |
| `--json` | off | Emit JSON |

```console
$ cc-notes task stale
b932fd9	in_progress	P2	ben <ben@example.com>	test the widget	idle 2h13m
```

### `cc-notes task archived`

List done and cancelled tasks older than the threshold. Archived tasks stay out of default and
`--all` views unless `--include-archived` is passed there.

| Flag | Default | Meaning |
|------|---------|---------|
| `--closed-before <when>` | (TTL default) | Archive cutoff |
| `--json` | off | Emit JSON |

### Lease model

A claim opens a lease. The heartbeat is the `AuthorTime` of the assignee's latest op, so any
edit, comment, or `renew` refreshes it. Staleness is judged by the reader against the TTL
threshold — never inside the fold — so it stays deterministic across replicas. Pin
`CC_NOTES_LEASE_TTL` (or `cc-notes.leaseTTL` in git config) per-repo so every agent agrees, and
keep it larger than your sync interval, or a healthy holder behind a slow sync looks stale.

### JSON task shape

`{"id":string,"branch":string,"title":string,"description":string,"type":string,"status":string,"priority":int,"assignee":string|null,"labels":[…],"blocked_by":[id,…],"blocks":[id,…],"parent":string|null,"comments":[{"author":string,"ts":rfc3339,"body":string}],"commits":[sha,…],"lease":{"holder":string|null,"heartbeat":rfc3339|null},"created_at":rfc3339,"updated_at":rfc3339,"started_at":rfc3339|null,"closed_at":rfc3339|null,"sprint":id|null,"project":id|null,"criteria":[{"id":string,"text":string,"script":string,"status":string}],"closed_forced":bool}`.
`blocks` is the derived reverse index of `blocked_by`; `branch` is `""` for a backlog task.
`sprint` and `project` are the task's independent membership pointers (`null` when unset); each
criterion's `status` is `pending`, `met`, or `failed`, and `script` is `""` when it carries none;
`closed_forced` is `true` only for a `done` task closed with at least one criterion still unmet.

## Project commands

Projects are long-lived, repo-wide, branch-less groupings of sprints and tasks. A task joins a
project directly with `--project`, or inherits one through its sprint; a project never points
down — `project show` derives both reverse indexes. Status advances from `active` to one of
`completed`, `archived`, or `cancelled`, and the lifecycle verbs fire only from `active`.
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
closed, commits), the description after a blank line, each comment as a `-- <author> <rfc3339>`
block, then a `sprints:` line and a `tasks:` line of short ids. `sprints` is the sprints pointed
at this project; `tasks` is the union of tasks pointed directly at the project and tasks reached
through one of its sprints, deduplicated.

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

Sprints are time-boxed, repo-wide groupings of tasks, optionally within a project. A task joins a
sprint with `--sprint`; a sprint points at an optional project with `--project`. `sprint show`
derives the task reverse index. Status advances from `planned` to `active` to `completed`, or to
`cancelled` from either open state; `start`, `complete`, and `cancel` fire only from `planned` or
`active`. Sprints resolve by id prefix like tasks. Every command takes `--json`.

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
line, each comment as a `-- <author> <rfc3339>` block, then a `tasks:` line of the short ids of
the tasks pointed at this sprint.

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
sets it `cancelled`. Each fires only from `planned` or `active`; a sprint already `completed` or
`cancelled` is a conflict (exit 4).

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

Notes are repo-global with optional commit, path, directory, and branch anchors. The `*-tag` and
anchor flags are repeatable arrays. Drift, verification, and supersession are first-class: drift is
computed against the recorded witness, and supersession is a real edge, not a tag convention.

A directory anchor covers a subtree: it matches `PATH` in `relevant` for the directory itself or
any file under it, and its witness is the directory's git tree oid, so it drifts when anything in
the subtree changes — a file added, removed, or edited anywhere beneath it.

### `cc-notes note add TITLE`

Create a note. A note is born verified against `HEAD`: its anchors get a content witness at
creation.

| Flag | Default | Meaning |
|------|---------|---------|
| `--body <text>` | empty | Note body; `-` reads stdin |
| `--tag <tag>` | none | Tag; repeatable |
| `--commit <sha>` | none | Commit anchor; repeatable |
| `--path <path>` | none | Path anchor; repeatable |
| `--dir <dir>` | none | Directory anchor covering a subtree; repeatable |
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
| `--add-dir` / `--rm-dir <dir>` | Add or remove a directory anchor; repeatable |
| `--add-branch` / `--rm-branch <branch>` | Add or remove a branch anchor; repeatable |
| `--json` | Emit JSON |

### `cc-notes note verify ID`

Record that the note is still true as of now, refreshing the witness against the current content
of its anchors. This is how a note re-earns "fresh" after `note review` flags it.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes note verify ebba9fb
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

### `cc-notes note supersede OLD --by NEW`

Record that `NEW` replaces `OLD`. `OLD` drops from default listings and points at `NEW`; history
is preserved. `--remove` undoes the edge.

| Flag | Default | Meaning |
|------|---------|---------|
| `--by <id>` | (required) | The replacement note |
| `--remove` | off | Remove the supersede edge |
| `--json` | off | Emit JSON |

```console
$ cc-notes note supersede ebba9fb --by 7a3f10c
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

### `cc-notes note expire ID`

Flag a note out-of-date by hand — an agent-asserted verdict for a note you know is no longer
accurate but have no replacement for yet. The note surfaces in `note review` as `EXPIRED`, which
takes precedence over every computed verdict. It stays in `note list`; clear the flag with
`note verify` (which re-confirms it true) or `note expire --clear`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--reason <text>` | empty | Why it is out-of-date; `-` reads stdin |
| `--clear` | off | Remove the expired flag |
| `--json` | off | Emit JSON |

```console
$ cc-notes note expire ebba9fb --reason "tokens now live 30 minutes"
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

### `cc-notes note review`

Surface notes needing attention, each with a verdict appended to the lean line: `EXPIRED` (an
agent flagged it out-of-date with `note expire`; top precedence), `DRIFTED` (an anchored path or
commit changed since the note was verified), `STALE` (verified too long ago), `UNVERIFIED` (never
verified), and dangling supersede edges.

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
| `--dir <dir>` | none | Require directory anchor |
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
| `--anchor-dir <dir>` | none | Require directory anchor |
| `--anchor-branch <branch>` | none | Require branch anchor |
| `--anchor-commit <sha>` | none | Require commit anchor |
| `--json` | off | Emit JSON |

```console
$ cc-notes note search "token expiry" --tag design
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes
```

### `cc-notes note show ID`

Show one note: a fixed-order header block (id, title, tags, anchors, author, created, updated,
verified_at/by, superseded_by, supersedes, drift verdict) then the body after a blank line.
`supersedes` is a text-only field — the computed reverse index of `superseded_by` — with no JSON
counterpart, so don't parse it from `--json`.

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
`drift` is the computed verdict (`null` when fresh); `superseded_by` is the replacement note id
or `null`.
