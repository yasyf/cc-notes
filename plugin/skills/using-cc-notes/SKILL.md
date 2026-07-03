---
name: using-cc-notes
description: >-
  Use cc-notes to record durable tasks and notes that outlive a session, stored as
  git objects on refs/cc-notes/*. Triggers when an agent records a task or note for
  later; stores an artifact, evidence file, or dump — a VM or CI run log, a panic or
  crash dump, a repro archive — durably as a log attachment instead of committing it
  to the repo tree; adopts cc-notes in a repo that has not run init yet; runs status to orient on
  the backlog and who holds what; claims or starts a
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

## Five tools, five jobs

Get this distinction right first. Native todos, cc-notes tasks, cc-notes notes, cc-notes docs,
and cc-notes logs differ along two axes — how long the record lives and who can see it — and the
three durable knowledge records, a note, a doc, and a log, split once more by form.

| Tool | Lifetime | Scope | Use for |
|------|----------|-------|---------|
| Native todos (`TaskCreate`/`TaskUpdate`) | Ephemeral — this session, gone at session end | This agent's private scratchpad | Decomposing the *current* task into in-session steps |
| `cc-notes task` | Durable — git ODB, synced across machines and agents | Global: one flat ref per task, with a mutable `branch` attribute and a shared backlog every agent sees | Work that outlives the session or coordinates agents: claim, lease, deps, comments, priority, lifecycle |
| `cc-notes note` | Durable — git ODB, synced | Repo-global, optionally anchored to a commit, path, or branch | Design decisions and durable facts, verified and searchable |
| `cc-notes doc` | Durable — git ODB, synced | Repo-global, anchored like a note, plus a `--when` read-trigger | Multi-paragraph guidance written *for the next agent*, verified and floated on read |
| `cc-notes log` | Durable — git ODB, synced | Repo-global, anchored like a doc | An append-only chronological journal — an incident timeline, a rollout log, a debugging session — whose entries are never edited or reordered, with no verify/drift/supersede lifecycle |

Tasks are **global**. Each task is a single flat ref at `refs/cc-notes/tasks/<id>`, exactly
like a note. Its branch is a *mutable attribute*, not part of its identity: `task list` and
`task ready` default to your current branch, the shared **backlog** is every task with no
branch (`task add --backlog`, visible to every agent on every branch), and `task move <id>
--to <branch>` (or `task start`, automatically) re-homes a task by setting that attribute.
Because the id is global, every id-addressed command resolves by id alone — there is no
`--branch` on `show`/`claim`/`start`/`done`/`move`/`renew`.

A note records when it was last **verified** true; superseding a note points it at its
replacement and drops it from default listings.

A **doc** is the long-form sibling of a note — the same durable, repo-global, born-verified
lifecycle (`doc verify`/`doc expire`/`doc supersede`), but it holds multi-paragraph guidance
written *for the next agent* where a note holds a one-line fact, and it carries a free-text
`--when` read-trigger that names when that agent should open it. Like a note it anchors to the
code it describes, drifts when that code changes, and floats into a relevant agent's context —
but only its title, `--when` text, and a `doc show` pointer surface, never the body.

A **log** looks like a doc — durable, repo-global, anchored, floated on read — but it is the
opposite kind of record. A doc is *living guidance* kept fresh: you replace its body and re-verify
it, and it drifts when the code moves out from under it. A log is an *immutable running record*:
`log append` adds a timestamped, authored entry and that entry never moves or changes, so a log
has no freshness lifecycle at all — no verify, no expire, no supersede, and it never drifts,
because an append-only journal never claims to be current truth. Reach for a log when the value is
the chronology itself — an incident timeline, a rollout log, a debugging session — rather than a
single fact (a note) or a guide you keep current (a doc).

The identity that signs writes is `CC_NOTES_ACTOR` (`"Name <email>"`) if set, else your git
`user.name`/`user.email`. Claims and leases key on that actor.

See `references/tasks-vs-notes.md` for worked examples of choosing among the five.

## Mount the notes tree (optional)

The `.notes` mount surfaces every note, doc, and task as editable files at the repo root — read-write Markdown and JSON you browse and edit instead of shelling out. On a `_fuse` binary, `cc-notes init` mounts it by default and records the preference, so each new session re-mounts it automatically and the mount survives reboots with no steady-state cost. A pure (non-fuse) binary records the preference but mounts nothing until a fuse-capable session takes over. Opt out at init time, or manage a live mount:

```console
$ cc-notes init --no-mount   # skip the mount and disable auto-mount
$ cc-notes mount               # mount on demand (needs a _fuse binary)
$ cc-notes mount --stop .notes # unmount this repo's .notes
```

The mount mechanics — holder model, teardown, the macOS Network Volumes grant — live in `references/cli-reference.md`.

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
with `note verify`, flag one that has gone out of date with `note expire`, and replace a changed
decision with `note supersede`.

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
| `cc-notes history <id>` | Show an entity's edit history — who changed which fields, when (`--reverse`, `--limit`, `--json`) |
| `cc-notes task add "<title>"` | Capture branch work; add `--backlog` for shared work |
| `cc-notes task ready` | List open, unassigned, unblocked tasks — the pickup queue |
| `cc-notes task start <id>` | Claim a task and move it onto your current branch |
| `cc-notes task claim <id> --steal` | Reclaim an in-progress task whose lease expired |
| `cc-notes task renew <id>` | Refresh the lease heartbeat on a task you hold |
| `cc-notes task done <id>` | Close a task and anchor HEAD onto it |
| `cc-notes task move <id> --to <branch>` | Re-home a task by setting its branch |
| `cc-notes note add "<title>"` | Record a durable fact, born verified against HEAD |
| `cc-notes note verify <id>` | Record that a note is still true as of now |
| `cc-notes note expire <id>` | Flag a note as out-of-date; clear it with `note verify` |
| `cc-notes note review` | Surface expired, drifted, stale, and unverified notes |
| `cc-notes note search "<query>"` | Ranked search over titles, tags, and bodies |
| `cc-notes doc add "<title>" --when "<trigger>"` | Store long-form agent guidance, born verified, with a when-to-read trigger |
| `cc-notes doc edit <id> --checkout` | Render a doc (or note) to an editable file; edit it, then `--apply` (or `--abort`) |
| `cc-notes doc search "<query>"` | Ranked search over doc titles, tags, and bodies |
| `cc-notes log add "<title>"` | Start an append-only chronological journal |
| `cc-notes log append <id> "<text>"` | Append one timestamped, authored entry to a log |
| `cc-notes log show <id>` | Read a log's entries in chronological order |
| `cc-notes attachment get <id> <name>` | Retrieve an attachment's bytes (stdout, or `-o <path>`) |

Append `--json` to any note, doc, log, task, sync, reconcile, or status command for a
machine-readable record instead of the lean line.

## Artifacts & evidence

When a session produces machine-generated evidence — a VM or CI run's logs, panic or crash
dumps, a repro archive — its home is a cc-notes log with the files attached, not the repo
tree. A `cp -R` of run output under `docs/` or an `assets/` directory bakes megabytes of
one-shot evidence into git history that every future clone pays for. Attachments store the
bytes in git-lfs, content-addressed and outside the commit graph, and hang them off the
entity by name; only the human-facing, publishable report belongs in the tree.

Create one log per investigation, then append one entry per run — verdict in the message,
evidence attached to the entity:

```console
$ cc-notes log add "fusekit VM repro: forced unmount" --dir internal/fusefs --tag evidence
4a81c9e	2026-07-02	evidence	fusekit VM repro: forced unmount
$ cc-notes log append 4a81c9e -m "phase 2: forced unmount wedges the holder; panic captured" \
    --attach results/scenario.log --attach results/panics/boot.panic
4a81c9e	2026-07-02	evidence	fusekit VM repro: forced unmount
```

`--attach` is repeatable and works the same on `note add`, `doc add`, and `log add`. It is
fully offline: the file is hashed into the local LFS store at write time, no network. Names
are unique per entity — a `log append` that reuses a live name fails unless you pass
`--replace` (a re-run superseding the last run's `scenario.log`), and `--rm-attachment
<name>` on `note`/`doc`/`log edit` drops one by name.

The sharp edges:

- **Only `cc-notes sync` moves the bytes.** Attachment content transfers over git-lfs
  during `sync` — uploads before the refs push, downloads after. A plain `git push` (the
  installed refspecs) publishes the refs *without* the content, so replicas see the entry
  but not the files until someone who has them runs `cc-notes sync`.
- **The remote's LFS quota is real.** On GitHub, attachments draw down the repo's LFS
  storage and bandwidth quota — modest on the free tier and separate from ordinary repo
  storage. Attach evidence that earns its bytes; when it stops earning them,
  `--rm-attachment` drops it from the live set and future syncs stop carrying it.

Read evidence back with `cc-notes attachment get <id> <name>` (stdout, or `-o <path>`) or
`cc-notes attachment path <id> <name>`, which prints the local store path. `show` on the
entity lists its attachments and flags any not yet downloaded with a `cc-notes sync` hint.
On a mounted `.notes` tree, attachments also browse read-only at
`.notes/attachments/<short-id>/<name>`.

## Memory mirror (automatic)

Where the cc-notes capt-hook pack is enabled, an agent's durable *memory* writes mirror into
notes on their own — you never have to choose between the two stores. A `PostToolUse` hook
watches the harness's memory files and, for the repo-relevant kinds (`feedback`, `project`, and
`reference` — a `user` "who you are" memory is skipped), upserts a note keyed by a `memory:<slug>`
tag. The first write to a memory creates the note; a later edit rewrites that same note, so a
memory and its note stay one-to-one. The note takes the memory's one-line description as its title
and the memory body verbatim, tagged `memory`, `memory:<slug>`, and `memory-type:<type>`.

List what has been mirrored with `cc-notes note list --tag memory`, then `cc-notes sync` to share
it. The memory write itself always lands first and untouched; a mirror that cannot write stays
silent, so it never disturbs the write it shadows.

## Auto-sync / auto-reconcile (automatic)

Where the cc-notes capt-hook pack is enabled, your git workflow keeps cc-notes refs shared on its
own — you no longer run `cc-notes sync` by hand after routine actions. After a `git commit`, a
`cc-notes task claim`/`task start`, or a `git merge`/`git pull`, a `PostToolUse` hook runs `cc-notes
sync` itself, at most once per turn — a commit and a claim in the same turn sync once. After a `git
merge`/`git pull` it first runs `cc-notes reconcile --into <current branch>`, carrying the merged
branch's still-open tasks onto your branch, then syncs.

Both are idempotent and fail-closed: a repo with no remote or an offline box stays silent, while a
genuine sync failure — say a rejected non-fast-forward push — surfaces a short retry hint. A
detached HEAD or a reconcile error is skipped silently. jj merges fire no git hooks, so after a jj
merge you still run `cc-notes reconcile` and `cc-notes sync` yourself. Note the contrast with the
memory mirror above: a memory write is not an auto-sync trigger, so the mirror still asks you to run
`cc-notes sync` to share it.

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
- `references/tasks-vs-notes.md` — the five-way distinction with worked examples of choosing
  native todo vs cc-notes task vs cc-notes note vs cc-notes doc vs cc-notes log.
- `references/lifecycle-and-hygiene.md` — keeping the record honest: task leases and
  staleness, note verification, drift, and supersession, and the maintenance verbs.
- `references/sprints-and-projects.md` — the optional planning layer: tasks rolling up into
  sprints and projects, the repo-wide upward pointers, and the derived reverse indexes.
- `references/validation-criteria.md` — structured acceptance criteria on a task, the gated
  `task done`, and the explicit, confirmation-gated `task validate` trust boundary.
