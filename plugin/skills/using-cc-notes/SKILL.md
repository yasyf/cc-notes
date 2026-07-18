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
  reclaims stale claims; verifies or supersedes a durable fact; records or executes a
  repeatable procedure as a runbook with per-run step tracking; syncs tasks and notes
  with a remote; reconciles tasks after merging a branch; or links commits to the task
  they implemented.
allowed-tools: Bash(cc-notes:*), Read
---

# Using cc-notes

cc-notes is a git-native notes and tasks layer for agents. Every entity — a note or a
task — is an event-log CRDT: an append-only log of operation packs, one per git commit,
on hidden `refs/cc-notes/*` refs inside the repo's object database. The data is versioned
and invisible in checkouts and diffs. Plain `git push` publishes your refs; a plain `git
fetch`/`git pull` stages incoming refs in a tracking namespace, and `cc-notes sync` folds
them into your view — the cc-notes capt-hook pack and CI workflow run that for you (under
jj, whose git bridge skips the cc-notes refs, `cc-notes sync` covers both directions). A
deterministic fold replays each log into a snapshot, so every replica reads the same
state.

Reach for cc-notes when work or knowledge must survive the current session or reach another
agent. Track moment-to-moment steps for what you are doing right now in the harness's own
todo tool.

## MCP tools — the primary interface

Where the Claude Code plugin is enabled (or any MCP client points at `cc-notes mcp`), the
agent-facing command surface is MCP tools — one `noun_verb` tool per command (`note_add`,
`task_start`, `runbook_run_done`, …), surfaced as `mcp__plugin_cc-notes_cc-notes__<tool>`.
**Call the tool; shell out only when no server is live.** The schemas structurally prevent
the flag-spelling errors that dog the CLI path — repeatable flags are arrays, toggles are
booleans, and the properties are named in the schema instead of remembered — and each tool
drives the CLI in-process, so validation, semantics, and output are identical: a tool
result is the command's `--json`.

Property names mirror the flags. The four anchored kinds — note, doc, log, runbook — take
anchor arrays (commits, paths, dirs, branches) on their add tools and the add_*/rm_* octet
on their edit tools; task, sprint, project, and the criterion/step tools do not carry anchors
(tasks scope to a branch). `--label` becomes `labels`. Long text rides the `body` property
directly — no `--checkout` buffer round-trip, no stdin; the literal `"-"` is rejected over
MCP, so pass the text itself.

Three calls carry most days. Record a durable fact with `note_add` — body and path anchor
inline:

```json
{
  "title": "Auth tokens expire after 15 minutes",
  "body": "Refresh client-side before expiry; the API returns 401 with no Retry-After header.",
  "paths": ["services/auth/login.go"],
  "labels": ["design"]
}
```

Capture shared work onto the backlog with `task_add` — acceptance criteria gate
`task_done`. task_add accepts criteria (list) or no_validation_criteria (bool); the schema
marks both optional, and the server enforces at dispatch that you pass exactly one of them —
omitting both fails at call time, not schema time:

```json
{
  "title": "Add retry backoff to the API client",
  "criteria": ["backoff caps at 30s", "go test ./... passes"],
  "backlog": true,
  "priority": 1
}
```

Store a repeatable procedure with `runbook_add` — steps in order, anchored to the code it
operates on exactly like a note:

```json
{
  "title": "Deploy hotfix",
  "steps": ["drain traffic", "deploy", "verify health"],
  "paths": ["scripts/deploy.sh"],
  "branches": ["main"]
}
```

The same shape covers the rest: `doc_add` takes a `when` read-trigger and the full
markdown in `body`; `log_append` takes `entry` plus `attach` paths; `search` ranks across
every kind. `references/cli-reference.md` documents both surfaces — every command block
opens with an `MCP:` line naming the tool and its properties. Operator commands (`init`,
`mount`, `gc`, `compact`, `viz`, `version`, the installers) are CLI-only on purpose. Where
the server is active, cc-notes' own capt-hook nudges name the tools; the CLI forms below
are the fallback for sessions without it.

## Seven tools, seven jobs

Get this distinction right first. Native todos, cc-notes tasks, cc-notes notes, cc-notes docs,
cc-notes logs, cc-notes investigations, and cc-notes papercuts differ along two axes — how long
the record lives and who can see it — and the five durable knowledge records, a note, a doc, a
log, an investigation, and a papercut, split once more by form.

| Tool | Lifetime | Scope | Use for |
|------|----------|-------|---------|
| Native todos (`TaskCreate`/`TaskUpdate`) | Ephemeral — this session, gone at session end | This agent's private scratchpad | Decomposing the *current* task into in-session steps |
| `task_*` / `cc-notes task` | Durable — git ODB, synced across machines and agents | Global: one flat ref per task, with a mutable `branch` attribute and a shared backlog every agent sees | Work that outlives the session or coordinates agents: claim, lease, deps, comments, priority, lifecycle |
| `note_*` / `cc-notes note` | Durable — git ODB, synced | Repo-global, optionally anchored to a commit, path, or branch | Design decisions and durable facts, verified and searchable |
| `doc_*` / `cc-notes doc` | Durable — git ODB, synced | Repo-global, anchored like a note, plus a `when` read-trigger | Multi-paragraph guidance written *for the next agent*, verified and floated on read |
| `log_*` / `cc-notes log` | Durable — git ODB, synced | Repo-global, anchored like a doc | An append-only chronological journal — a rollout log, a migration diary — whose entries are never edited or reordered, with no verify/drift/supersede lifecycle and no verdict |
| `investigation_*` / `cc-notes investigation` | Durable — git ODB, synced | Repo-global, anchored like a note | A debugging arc: an immutable premise, an append-only evidence timeline, findings with per-finding dispositions, and a typed status that carries the verdict (`open → root_caused → fixed → confirmed`, plus `exonerated`/`abandoned`) |
| `papercut` / `cc-notes papercut` | Durable — git ODB, synced | Repo-wide: one shared journal, auto-created on first use | One-paragraph friction complaints — a dead-end tool call, a broken link, a misleading doc — filed instead of silently pushed through |

Tasks are **global**. Each task is a single flat ref at `refs/cc-notes/tasks/<id>`, exactly
like a note. Its branch is a *mutable attribute*, not part of its identity: `task_list` and
`task_ready` default to your current branch, the shared **backlog** is every task with no
branch (`task_add` with `backlog: true`, visible to every agent on every branch), and
`task_edit` with a `branch` (or `task_start`, automatically) re-homes a task by setting that
attribute. Because the id is global, ids resolve with no branch qualifier — `branch` filters
what `list`/`ready` read and sets a task's placement on `add`/`edit`. In a colocated jj repo —
git HEAD detached at the working-copy parent — the current branch still resolves: the nearest
unmerged bookmark, else the trunk. When nothing resolves, branch-scoped task commands degrade
to the backlog instead of failing; an explicit `branch` on `task_start` sets one.

A note records when it was last **verified** true; superseding a note points it at its
replacement and drops it from default listings.

A **doc** is the long-form sibling of a note — the same durable, repo-global, born-verified
lifecycle (`doc_verify`/`doc_expire`/`doc_supersede`), but it holds multi-paragraph guidance
written *for the next agent* where a note holds a one-line fact, and it carries a free-text
`when` read-trigger that names when that agent should open it. Like a note it anchors to the
code it describes, drifts when that code changes, and floats into a relevant agent's context —
but only its title, `when` text, and a `doc_show` pointer surface, never the body. The title is
a short handle — capped at 256 bytes, like every title — so the guidance lives in the body:
pass the full markdown through `doc_add`'s `body` parameter. On the bare CLI, check out a
prefilled buffer with `cc-notes doc add --checkout`, write the guidance into it with your file
tools, then `cc-notes doc add --apply`; a positional `BODY`, `--body`, or stdin (`-`) carries a
short body inline. Never cram the guidance into the title, and never point the doc at a `/tmp`
or scratchpad file purged before the next agent reads it — `attach` an artifact that must ride
along.

A **log** looks like a doc — durable, repo-global, anchored, floated on read — but it is the
opposite kind of record. A doc is *living guidance* kept fresh: you replace its body and re-verify
it, and it drifts when the code moves out from under it. A log is an *immutable running record*:
`log_append` adds a timestamped, authored entry and that entry never moves or changes, so a log
has no freshness lifecycle at all — no verify, no expire, no supersede, and it never drifts,
because an append-only journal never claims to be current truth. Reach for a log when the value is
the chronology itself and no verdict is coming — a rollout log, a migration diary — rather than a
single fact (a note) or a guide you keep current (a doc). A chronology that opens on a suspicion
and closes on a verdict is an **investigation**, not a log.

An **investigation** is the log's verdict-bearing sibling: the same append-only timeline (and
attachment support), plus an immutable premise set at open, a findings list where each suspect
hypothesis gets an explicit disposition with evidence (`clear` or `confirm`, `--why` required),
and a typed status that holds the verdict — `open → root_caused → fixed → confirmed`, with
`exonerated` (premise falsified) and `abandoned` (no verdict) as the other exits. Open one the
moment you form a falsifiable suspicion — a red CI run you triage, a bug hunt, an anomaly — and
append evidence per triage step, never retro-written at the end. The verdict goes through the
transition verbs, never into the title: a wrong first theory stays visible on the record, which
is the point. Like a log it never drifts; a durable present-tense fact the verdict produces (a
design invariant, say) graduates into a note or doc and is linked as a follow-up.

A **papercut** is the fire-and-forget corner of the log: `papercut` with the complaint as `body`
(CLI: `cc-notes papercut "<complaint>"`) files a one-paragraph friction complaint — a dead-end
tool call, a broken link, a misleading doc — instead of silently pushing through. Every complaint
appends one entry to a single repo-wide journal (a log titled `papercuts`, tagged `papercut`,
auto-created on first use), so there is nothing to set up and no review lifecycle to run: entries
are never edited, `papercut_list` reads the chronology back, and `model` (or `CC_NOTES_MODEL`,
with the parameter winning) records which model hit the friction.

The identity that signs writes is `CC_NOTES_ACTOR` (`"Name <email>"`) if set, else your git
`user.name`/`user.email`. Claims and leases key on that actor.

See `references/tasks-vs-notes.md` for worked examples of choosing among the seven.

## Mount the notes tree (optional)

The `.notes` mount surfaces every note, doc, and task as editable files at the repo root — read-write Markdown and JSON you browse and edit instead of shelling out. On a `_fuse` binary, `cc-notes init` mounts it by default and records the preference, so each new session re-mounts it automatically and the mount survives reboots with no steady-state cost. A pure (non-fuse) binary records the preference but mounts nothing until a fuse-capable session takes over. Opt out at init time, or manage a live mount:

```console
$ cc-notes init --no-mount    # skip the mount and disable auto-mount
$ cc-notes mount              # mount on demand (needs a _fuse binary)
$ cc-notes mount stop .notes  # unmount this repo's .notes
```

The mount mechanics — holder model, teardown, the macOS Network Volumes grant — live in `references/cli-reference.md`.

## Canonical agent flow

The spine of day-to-day use. Run `init` once per repo; everything else recurs as you work.
Each step names the MCP tool first; the CLI form is the fallback when no server is live.

**1. Initialize (once per repo — CLI only).** `cc-notes init` installs the refspecs: plain
`git push` publishes the cc-notes refs alongside your branches, and a plain `git
fetch`/`git pull` stages incoming refs in a tracking namespace that `sync` (or the
capt-hook pack, automatically) folds into your view. init then wires whatever the repo is
already set up for: when a `.claude/` directory exists it registers the cc-notes plugin in
`.claude/settings.json` and enables the cc-notes capt-hook pack (manifest at
`.claude/capt-hook.toml`); when a `.github/` directory exists it installs the reconcile CI
workflow (`--no-ci` to skip, `--ci` to force without `.github/`). init never creates
`.claude/` — it wires Claude Code only when the repo already uses it. Under jj the plain-git
path doesn't hold (`jj git push`/`jj git fetch` bridge only `refs/heads/*`, leaving
`refs/cc-notes/*` behind), so run `sync`, which drives git directly and carries the refs
both ways regardless of front-end.

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
registered: cc-notes plugin in .claude/settings.json
```

**2. Orient.** `status` (CLI: `cc-notes status`, alias `board`) is a read-only, sectioned
view: the shared backlog, your current branch's open and in-progress tasks, every
in-progress task across all branches grouped by assignee with a fresh/STALE lease flag, and
how many notes need review. Run it before picking up work.

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
The backlog `task_add` is the second worked example above; a branch-scoped one drops
`backlog` and keeps `criteria`. CLI: `cc-notes task add "<title>" --priority 1 --label api
--criterion "backoff caps at 30s"` (`--backlog` for shared work).

**4. Grab.** `task_start` — `{"id": "d82c087"}` — atomically claims the task
(deterministic first-wins) and moves it onto your current branch, opening a lease. CLI:
`cc-notes task start d82c087`.

**5. Stay alive.** Any change you make to a task refreshes its lease; for long silent
stretches, `task_renew`. `task_stale` surfaces in-progress tasks whose lease has expired —
a crashed agent's abandoned claim — and `task_claim` with `{"id": "7c1e3f0", "steal": true}`
reclaims one. CLI: `cc-notes task stale`, `cc-notes task claim 7c1e3f0 --steal`.

**6. Work and link.** Commit code with plain git, adding a `cc-task: <id>` trailer so the
commit links to the task (queryable with `git log --grep` and `blame`). `task_done` closes
the task and anchors your HEAD commit onto it; `task_show` then lists the commits that
implemented it.

```console
$ git commit -m "Clamp API retry backoff at 30s

cc-task: d82c087"
$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

**7. Chase a suspicion.** The moment work turns into debugging with a falsifiable premise —
a red CI run, a bug hunt, an anomaly — `investigation_open` records that premise immutably.
`investigation_append` logs evidence per triage step, `investigation_finding_add` then
`_clear`/`_confirm` (with `why`) gives each suspect an explicit disposition, and the arc
closes through the transition verbs — `investigation_root_cause`, `investigation_fix`,
`investigation_confirm`, or `investigation_exonerate` when the premise falls. CLI:
`cc-notes investigation open "<title>" "<premise>"`.

**8. Record facts.** A note is born verified against the current HEAD — the first worked
example above. Re-confirm it later with `note_verify`, flag one that has gone out of date
with `note_expire`, and replace a changed decision with `note_supersede` (`{"id": "<old>",
"by": "<new>"}`).

**9. Merge and reconcile.** Merge code with git or jj, then carry the merged branches'
still-open tasks onto the target and converge with the remote: `reconcile` with
`{"into": "main"}`, then `sync`. Both steps are idempotent.

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

**10. Maintain.** `note_review` surfaces drifted, stale, and unverified facts; `task_archived`
hides long-closed work; `cc-notes gc --prune-remote` (CLI-only, opt-in, best-effort)
physically reclaims tombstoned refs.

## Command cheat-sheet

The verbs reached for most: the MCP tool with its key properties, then the CLI fallback.
The full surface — every flag, property, default, and output shape — is in
`references/cli-reference.md`.

| Purpose | MCP tool (key properties) | CLI fallback |
|---------|---------------------------|--------------|
| Set up a repo (once) | — (CLI-only) | `cc-notes init` |
| Orient: backlog, tasks, leases, review count | `status` | `cc-notes status` |
| Union-merge refs + attachments with the remote | `sync` | `cc-notes sync` |
| Carry merged branches' open tasks | `reconcile` (`into`) | `cc-notes reconcile --into <branch>` |
| Surface records anchored near a path | `relevant` (`path`) | `cc-notes relevant <path>` |
| Ranked search across every kind | `search` (`query`, `labels`, `limit`) | `cc-notes search "<query>"` |
| List the task(s) a commit implemented | `blame` (`sha`) | `cc-notes blame <sha>` |
| Show any entity by id | `show` (`id`) | `cc-notes show <id>` |
| An entity's edit history | `history` (`id`, `reverse`, `limit`) | `cc-notes history <id>` |
| Capture work | `task_add` (`title`, `criteria`, `backlog`) | `cc-notes task add "<title>" --criterion <text>` |
| The pickup queue | `task_ready` | `cc-notes task ready` |
| Claim + move onto your branch | `task_start` (`id`) | `cc-notes task start <id>` |
| Reclaim an expired lease | `task_claim` (`id`, `steal`) | `cc-notes task claim <id> --steal` |
| Refresh a lease you hold | `task_renew` (`id`) | `cc-notes task renew <id>` |
| Close and link HEAD | `task_done` (`id`) | `cc-notes task done <id>` |
| Re-home a task | `task_edit` (`id`, `branch` or `backlog`) | `cc-notes task edit <id> --branch <branch>` |
| Thread discussion on a task, sprint, project, or runbook | `task_comment` / `sprint_comment` / `project_comment` / `runbook_comment` (`id`, `body`) | `cc-notes task comment <id> "<text>"` |
| Record a durable fact | `note_add` (`title`, `body`, `paths`) | `cc-notes note add "<title>" --path <path>` |
| Re-confirm a fact | `note_verify` (`id`) | `cc-notes note verify <id>` |
| Flag a fact out-of-date | `note_expire` (`id`, `reason`) | `cc-notes note expire <id>` |
| Review drifted/stale/unverified | `note_review` / `doc_review` | `cc-notes note review` |
| Search one kind | `note_search` (`query`) | `cc-notes note search "<query>"` |
| Guidance for the next agent | `doc_add` (`title`, `when`, `body`) | `cc-notes doc add "<title>" --when "<trigger>" --body -` |
| Revise a doc's body | `doc_edit` (`id`, `body`) | `cc-notes doc edit <id> --checkout` … `--apply` |
| Start an append-only journal | `log_add` (`title`, `entry`) | `cc-notes log add "<title>"` |
| Append an entry / artifacts | `log_append` (`id`, `entry`, `attach`) | `cc-notes log append <id> "<text>"` |
| Read a journal back | `log_show` (`id`) | `cc-notes log show <id>` |
| Open an investigation on a suspicion | `investigation_open` (`title`, `premise`, `findings`) | `cc-notes investigation open "<title>" "<premise>"` |
| Append evidence per triage step | `investigation_append` (`id`, `text`, `attach`) | `cc-notes investigation append <id> "<text>"` |
| Rule a suspect out / in | `investigation_finding_clear` / `_confirm` (`id`, `finding`, `why`) | `cc-notes investigation finding clear <id> <finding> --why "<evidence>"` |
| Record the root cause | `investigation_root_cause` (`id`, `text`) | `cc-notes investigation root-cause <id> "<cause>"` |
| Record the fixing commits | `investigation_fix` (`id`, `commits`) | `cc-notes investigation fix <id> --commit <sha>` |
| Close with proof, or reopen on regression | `investigation_confirm` / `investigation_reopen` (`id`, `text`) | `cc-notes investigation confirm <id> "<proof>"` |
| File a friction complaint | `papercut` (`body`) | `cc-notes papercut "<complaint>"` |
| Read every complaint | `papercut_list` | `cc-notes papercut list` |
| Retrieve an attachment | `attachment_get` (`id`, `name`, `output`) | `cc-notes attachment get <id> <name> -o <path>` |
| Store a procedure | `runbook_add` (`title`, `steps`, `paths`) | `cc-notes runbook add "<title>" --step "<text>"` |
| Add a positioned step | `runbook_step_add` (`id`, `text`, `command`, `after`) | `cc-notes runbook step add <id> "<text>"` |
| Begin a tracked run | `runbook_run_start` (`id`, `task`) | `cc-notes runbook run start <id>` |
| Record a step outcome | `runbook_run_done`/`_skip`/`_fail` (`id`, `step`, `note`) | `cc-notes runbook run done <id> <step>` |
| Close the run | `runbook_run_finish` (`id`, `failed`, `abandoned`) | `cc-notes runbook run finish <id>` |

A tool result is the command's `--json`; on the CLI, append `--json` to any note, doc, log,
investigation, papercut, task, sync, reconcile, or status command for the same machine-readable
record instead of the lean line.

`--repo PATH` (`-R`) targets another repository's store from any cwd — pass any path inside it;
file-path arguments (e.g. `--attach`) still resolve against the invocation cwd.

## Artifacts & evidence

When a session produces machine-generated evidence — a VM or CI run's logs, panic or crash
dumps, a repro archive — its home is a cc-notes log with the files attached, not the repo
tree. A `cp -R` of run output under `docs/` or an `assets/` directory bakes megabytes of
one-shot evidence into git history that every future clone pays for. Attachments store the
bytes in git-lfs, content-addressed and outside the commit graph, and hang them off the
entity by name; only the human-facing, publishable report belongs in the tree.

Create one log per investigation with `log_add`, then `log_append` one entry per run —
verdict in the entry text, evidence attached to the entity:

```json
{
  "id": "4a81c9e",
  "entry": "phase 2: forced unmount wedges the holder; panic captured",
  "attach": ["results/scenario.log", "results/panics/boot.panic"]
}
```

CLI: `cc-notes log add "<title>" --dir <dir> --label evidence`, then
`cc-notes log append <id> --entry "<verdict>" --attach results/scenario.log`.

The `attach` array (CLI `--attach`, repeatable) works the same on `note_add`, `doc_add`, and
`log_add`; on `note_edit`/`doc_edit` it attaches to an entity that already exists, and a log
grows the same way through `log_append`. (The CLI's `--checkout` buffer carries attachments
through at apply time — `cc-notes doc add --apply <path> --attach <file>` ingests them in the
same create transaction.) It is fully offline: the file is hashed into the local LFS store at
write time, no network. Names are unique per entity — an attach that reuses a live name fails
unless you pass `replace: true` (a re-run superseding the last run's `scenario.log`), and
`rm_attachments` on `note_edit`/`doc_edit`/`log_edit` drops names from the live set.

The sharp edges:

- **Only `sync` moves the bytes.** Attachment content transfers over git-lfs during `sync`
  — uploads before the refs push, downloads after. A plain `git push` (the installed
  refspecs) publishes the refs *without* the content, so replicas see the entry but not the
  files until someone who has them runs `sync`.
- **The remote's LFS quota is real.** On GitHub, attachments draw down the repo's LFS
  storage and bandwidth quota — modest on the free tier and separate from ordinary repo
  storage. Attach evidence that earns its bytes; when it stops earning them,
  `rm_attachments` drops it from the live set and future syncs stop carrying it.

Read evidence back with `attachment_get` — it requires an `output` file path and writes the
bytes there, never inline (CLI: `cc-notes attachment get <id> <name> -o <path>`, or stdout
with no `-o`) — or `attachment_path`, which prints the local store path for a zero-copy
read. `show` on the entity lists its attachments and flags any not yet downloaded with a
sync hint. On a mounted `.notes` tree, attachments also browse read-only at
`.notes/attachments/<short-id>/<name>`.

## Memory mirror (automatic)

Where the cc-notes capt-hook pack is enabled, an agent's durable *memory* writes mirror into
notes on their own — you never have to choose between the two stores. A `PostToolUse` hook
watches the harness's memory files and, for the repo-relevant kinds (`feedback`, `project`, and
`reference` — a `user` "who you are" memory is skipped), upserts a note keyed by a `memory:<slug>`
label. The first write to a memory creates the note; a later edit rewrites that same note, so a
memory and its note stay one-to-one. The note takes the memory's one-line description as its title
and the memory body verbatim, labeled `memory`, `memory:<slug>`, and `memory-type:<type>`.

List what has been mirrored with `note_list` and `{"labels": ["memory"]}` (CLI: `cc-notes note
list --label memory`), then `sync` to share it. The memory write itself always lands first and
untouched; a mirror that cannot write stays silent, so it never disturbs the write it shadows.

## Auto-sync / auto-reconcile (automatic)

Where the cc-notes capt-hook pack is enabled, your git workflow keeps cc-notes refs shared on its
own — you no longer run `cc-notes sync` by hand after routine actions. After a commit (`git
commit`, `jj commit`, `jj describe`, `ccx vcs ship`), a push (`git push`, `jj git push` — jj's git
bridge never carries the cc-notes refs, so this sync is what moves them), a `cc-notes task
claim`/`task start`, a `git merge`/`git pull`/`jj git fetch`, or any cc-notes write — a mutating
CLI subcommand or MCP tool; reads never trigger — a `PostToolUse` hook runs `cc-notes sync`
itself, covering every cc-notes-wired remote, at most once per target repo per turn — a commit
and a claim in the same turn sync once. A CLI write behind a `cd` into another repo
(`cd /other/repo && cc-notes note add …`) syncs *that* repo, confirmed as "Synced cc-notes refs
in <dir>."; a `cd` the hook can't resolve structurally (`cd -`, a `$var`, a `~`, backticks) falls
back to the session repo, and only record writes go cross-repo — a push, merge, or claim
elsewhere and every MCP write stay with the session repo. After a `git
merge`/`git pull`/`jj git fetch` it first runs `cc-notes reconcile --into <current branch>`,
carrying the merged branch's still-open tasks onto your branch, then syncs; on a detached HEAD
(the colocated-jj norm) or a failed reconcile it falls back to a plain sync, so the fetched refs
still ship. Only a jj merge or rebase still calls for a by-hand `cc-notes reconcile`.

Both are idempotent and fail-closed: a repo with no remote or an offline box stays silent, while a
genuine sync failure — say a rejected non-fast-forward push — surfaces a short retry hint. A
SessionEnd backstop covers whatever slips through: at session exit an async hook compares your
local `refs/cc-notes/*` tips against every wired remote's fetched tracking copies — zero network —
and syncs only when something is unpushed (needs capt-hook >= 9.2). Note the contrast with the
memory mirror above: a memory write is not an auto-sync trigger, so the mirror still asks you to
run `cc-notes sync` to share it — the SessionEnd backstop sweeps an unpushed mirror at exit.

## Projects and sprints (optional)

An optional planning layer sits on top of tasks — skip it for the canonical flow above. A
task can carry an independent **sprint** pointer (a time-boxed grouping) and **project**
pointer (a long-lived one), and a sprint can point at a project; all three are optional and
**repo-wide**, not branch-scoped like a task's `branch`. Membership is an upward pointer the
reader inverts, so `sprint_show` and `project_show` derive the tasks (and a project's
sprints) that roll up into them. Independently, a task can carry **validation criteria**
(`criteria` on `task_add`, or the `task_criterion_*` tools): `task_done` refuses to close
while any criterion is unmet unless you pass `force`, `task_criterion_met`/`_failed` record
a verdict with an evidence `note`, and `task_validate` runs each criterion's check script
behind an explicit confirmation. See `references/sprints-and-projects.md` and
`references/validation-criteria.md`.

## Runbooks (optional)

A **runbook** stores a repeatable procedure — a release checklist, a deploy sequence, an
incident response — as ordered steps, each an instruction with an optional shell command.
The split from a doc is execution: a doc *describes* and is kept fresh; a runbook is
*executed*, and every execution is a first-class **run** recording who ran it, when, and a
per-step outcome (`done`, `failed`, or `skipped` — a step with no recorded outcome is
pending). Runbooks are repo-wide like sprints and projects, with no freshness lifecycle;
they anchor to commits, paths, dirs, and branches exactly like a note (so `relevant`
surfaces them), rank in `runbook_search` and the kind-agnostic `search`, and retire with
`runbook_archive` (`runbook_rm` tombstones one outright).

The execution loop: `runbook_run_start` (a `task` id cites the task the run serves),
`runbook_run_done`/`_skip`/`_fail` per step as you go, then `runbook_run_finish`. Steps
insert and reorder without renumbering (`runbook_step_add` with `after`,
`runbook_step_move` with `first`), and concurrent runs by different agents merge cleanly.
See `references/runbooks.md`.

## References

- `references/cli-reference.md` — the complete command surface: every MCP tool and its
  properties, every flag and default, and the lean-line and `--json` output shape for each
  command.
- `references/coordination.md` — how agents coordinate over time: the backlog and the branch
  attribute, claims and leases, stale-claim recovery, deps and blocking, reconcile-on-merge,
  and union-merge sync across a shared remote.
- `references/tasks-vs-notes.md` — the seven-way distinction with worked examples of choosing
  native todo vs cc-notes task vs cc-notes note vs cc-notes doc vs cc-notes log vs cc-notes
  investigation vs cc-notes papercut.
- `references/investigations.md` — the investigation record: premise, timeline, findings, and
  the status machine; the log-vs-investigation call; and the multi-agent forensics flow.
- `references/lifecycle-and-hygiene.md` — keeping the record honest: task leases and
  staleness, note verification, drift, and supersession, and the maintenance verbs.
- `references/sprints-and-projects.md` — the optional planning layer: tasks rolling up into
  sprints and projects, the repo-wide upward pointers, and the derived reverse indexes.
- `references/runbooks.md` — repeatable procedures: the runbook-vs-doc call, authoring
  steps with positions and commands, and the tracked run loop.
- `references/validation-criteria.md` — structured acceptance criteria on a task, the gated
  `task done`, and the explicit, confirmation-gated `task validate` trust boundary.
