# cc-notes nudge hooks

This directory is a [capt-hook](https://pypi.org/project/capt-hook/) pack whose
hooks nudge agents to keep cc-notes in step with the git work they do. It is
enabled in a repo by `cc-notes hooks install`, and the Claude Code plugin's own
`hooks.json` session-attaches it wherever the cc-notes plugin is enabled. A
same-named `packs.toml` pin takes precedence over the attached copy, so a repo
carrying both never double-loads the pack.

These are **nudges plus allow-only approvers** — no hook ever blocks or
rewrites a tool call. The nudges only warn; the approvers in `approval.py`
answer a would-be permission dialog with *allow* for cc-notes' own surface and
stay silent on everything else.

## What the hooks teach

The whole module exists to make one distinction reflexive: when to reach for
native task tracking versus durable, git-synced cc-notes entities.

| Tool | Lifetime | Scope | Use for |
|------|----------|-------|---------|
| Native `TaskCreate`/`TaskUpdate` | This session only | This agent, private | In-session steps of the task you're doing right now |
| `cc-notes task add --backlog` | Durable, git-synced | The shared backlog (`Branch == ""`), visible to every agent on every branch | Unassigned work any agent can claim and start |
| `cc-notes task add` | Durable, git-synced | Your current branch | Branch-specific work that outlives the session, with claim, deps, and lifecycle |
| `cc-notes note add` | Durable, git-synced | Repo-global | Design decisions and durable facts |
| `cc-notes doc add` | Durable, git-synced | Repo-global, anchored like a note, plus a `--when` read-trigger | Long-form guidance written for the next agent, verified and floated on read |
| `cc-notes log add` | Durable, git-synced | Repo-global, anchored like a doc | An append-only chronological journal — entries are immutable, never edited or reordered |
| `cc-notes papercut` | Durable, git-synced | Repo-global, the shared `papercuts` journal | A one-paragraph complaint about friction hit during work — a dead-end tool call, a broken link, a misleading doc |

Tasks are global. The id addresses a task no matter which branch it lives on, and
its branch is a mutable attribute. `cc-notes task add --backlog` parks work in the
shared queue; `cc-notes task start <id>` claims it and pulls it onto your branch.

## The framework

Every nudge is one shape — **static recall → LLM precision → act** — running in one of
two directions.

**Surface (pull)** takes a file you just touched, recalls the durable records anchored
to it, and a small LLM keeps the subset worth your attention. It fails *open*: if the
model call errors, every recalled record is shown rather than hide context by breaking.

**Record (push)** takes a write, a copy, a commit, or an approved plan, recalls a candidate
over a cheap glob or diff, and a small LLM confirms the content is durable and routes it to
exactly one primitive — note, doc, log, task, or papercut — or to nothing. It fails *closed* to
silence.

The cheap layer (a path glob, the `cc-notes relevant` ranker, a commit diff) over-selects
on purpose; the LLM is the precision gate in both directions. The only deterministic hooks
are the ones with no "which" to pick: the memory mirror, where the file already declares
its type, the evidence-archive router, where the kind is always a log with attachments,
and the pure workflow reminders, where the action is fixed.

### Surface — pull durable records into context

| Trigger | Recall | The LLM picks |
|---------|--------|---------------|
| `Read` a file (PostToolUse) | notes, docs, and logs `cc-notes relevant <path>` ranks | which are worth surfacing now — a lone candidate surfaces directly, two or more are filtered |
| `Edit` / `Write` / `MultiEdit` a file (PostToolUse) | anchored records with a non-null drift verdict (`relevant --attached --worktree`) | which drift actually warrants a `verify` / `edit` / `supersede` / `expire`, named per kind |
| Session start, first `UserPromptSubmit` (once) | your branch's open/in-progress tasks topped up from the backlog | nothing — rendered straight as orientation, capped at seven with a `+K more` → `cc-notes status` |

Each record is LLM-judged at most once per session: a Read floats it as context once, an
edit asks about its staleness once, tracked in two separate per-session sets. The filter
marks every recalled record judged before it picks, so an unpicked one is not re-weighed
later. The session-start float is deterministic orientation, not a filtered surface.

### Record — route new durable content to a primitive

| Trigger | Candidate | The LLM picks |
|---------|-----------|---------------|
| `Write` / `Edit` / `MultiEdit` of an internal-looking file (PostToolUse) | a status/handoff/notes/runbook/`memory/` file the static `DurableInternalWrite` gate flags | note / doc / log / task / papercut / runbook — or none; the subtle calls are doc (living guidance) vs log (append-only chronology) and doc (describes) vs runbook (a procedure you re-execute, tracked per run), while papercut is a one-off friction gripe with nothing to curate or do |
| Bash `cp`/`mv`/`rsync` landing run output in a durable tree, or a `Write`/`Edit` of an evidence-suffixed file (`.log`, `.panic`, `.dump`, …) anywhere in it, `docs/**` included (PostToolUse) | the transfer the static `EvidenceArchive` gate flags — temp/scratchpad, `testdata/` fixtures, `.git` internals, and relative in-repo bulk copies stay exempt | (static, no model) a log entry carrying the artifacts as `--attach` git-lfs attachments; only `cc-notes sync` uploads their content (plain `git push` moves refs without it), and a >1MB payload strengthens the wording |
| Bash `cc-notes note`/`doc`/`log` `add`/`edit`/`append` (or `cc-notes papercut`) whose title or body text names a purge-bound path (`/tmp`, `/var`, a session scratchpad) the static `EphemeralRecordReference` gate flags — an `--attach` value is exempt (PostToolUse) | the record command the gate flags | (static, no model) carry the content in the record itself — `--body -` (or `--checkout` file mode) for text, `--attach <file>` for artifacts, whose bytes land in the ODB and sync with the repo |
| `git commit` / `jj commit` / `jj describe` / `ccx vcs ship` (PostToolUse) | the HEAD commit — message, diffstat, bounded patch | whether the change encodes a durable decision worth a note or doc; the `cc-task:` link reminder always fires regardless, and the sync is an automatic side-effect (see below) |
| `ExitPlanMode` (PostToolUse) | the approved plan's text (`planFilePath`, else inline) | which few plan items are durable work → `cc-notes task add` (`--backlog` if shared); the native-vs-durable teach always fires regardless |
| Many open native tasks after `TaskCreate` | the growing native list | (static, no model) mirror the durable or cross-agent items into `cc-notes task add` |

The internal-write and evidence-archive routers share one ask-once-per-turn slot; the commit router judges each HEAD sha once,
so an amend re-judges while a re-fire on the same commit stays silent; the plan router fires
once per plan file. Every record nudge falls *closed* to silence on any classifier or git
error, never invents a record on a degenerate parse (`record` and the kind both default
empty), and only ever suggests — it never blocks.

### Workflow reminders — fixed action, no model

| Trigger | Reminder |
|---------|----------|
| `git merge` / `git pull` / `jj git fetch` (PostToolUse) | the pack auto-runs `cc-notes reconcile --into <current branch>` (carrying the merged branch's still-open tasks onto your branch) then syncs, confirming "Reconciled merged tasks onto <branch>. Synced cc-notes refs." (a failed push surfaces the retry hint instead; a detached HEAD — the colocated-jj norm — or a failed reconcile falls back to a plain sync, so the fetched refs still ship) |
| `git push` / `jj git push` (PostToolUse) | the pack runs `cc-notes sync` so the cc-notes refs follow the branch push — jj's git bridge carries only `refs/heads/*`, so under jj this sync is the only thing that moves them |
| a cc-notes write — any mutating CLI subcommand or MCP tool (PostToolUse) | the pack syncs the fresh refs to the remote; reads (`list`, `show`, `search`, `status`, …) never trigger |
| `cc-notes task claim` / `task start` (PostToolUse) | you hold a lease — `task renew` on long work, `task done` when finished, `task claim --steal` to reclaim a crashed hold (sync is automatic now) |
| `cc-notes` binary missing, first `UserPromptSubmit` (once) | the pack is enabled but the binary is off `PATH` — name the two install paths (`brew install yasyf/tap/cc-notes` or `curl -fsSL …/install.sh \| sh`) so an opt-in repo isn't left silent |

### Approvals — cc-notes usage never prompts

Two `approval.py` hooks answer the permission dialog with *allow* whenever a
tool call is unambiguously cc-notes' own:

| Approver | Allows |
|----------|--------|
| MCP | any tool on the exact servers `cc-notes` or `plugin_cc-notes_cc-notes`, minus the carve-out below |
| CLI | one plain single-command `cc-notes`/`ccn` Bash invocation, minus the carve-out below |

The **carve-out** keeps the dialog for the calls that reach outside the git
ODB — the ones that read or write an arbitrary filesystem path, or run a stored
script. Auto-approving those would let a prompt-injected agent write any path,
read any secret into its context, or execute code with no human in the loop, so
they always prompt: `attachment get -o/--output`, `--attach`, `--apply`,
`--abort`, `--script`, `workflows install --dest`, `mount --socket`,
`task validate`, and `task criterion script` — and the matching MCP tools
(`task_validate`, plus any tool call carrying an `attach`, `output`, `script`,
or `file` path). Plain `note`/`task`/`doc`/`log` records, `status`, `list`,
`show`, and `sync` stay prompt-free.

Everything else also falls through to the normal dialog: shell expansion
anywhere in the raw text (`$`, backticks, braces, process substitution),
pipelines, chains, redirects and heredocs — so a `--body -` fed by a heredoc
prompts; carry the text in the MCP `body` parameter instead — env-assignment
prefixes, wrappers (`sudo`, `env`, `exec`), a path-qualified binary, and a bare
`--` in the args. Explicit user deny and ask rules always win — Claude Code
evaluates them regardless of a hook allow. On capt-hook >= 9.24.0 the approvers
ride the default `PreToolUse | PermissionRequest` registration, so teammate and
subagent dialogs forwarded to the lead session — which run no `PermissionRequest`
hooks at all — are covered too. Like the rest of the pack, the approvers are
dormant without the captain-hook dispatcher plugin.

### Firing policy

One cap by class. Every Record router and teach-carrying Workflow reminder is capped at
`NUDGE_MAX_FIRES` (three) per session as a backstop, and additionally deduped by its own
key — the turn, the HEAD sha, or the plan path — so it speaks once per real event rather
than on every fire. The pure sync actions (after a merge/pull/fetch, a push, or a cc-notes
write) carry no session cap: their only output is the sync confirm, and the per-turn dedup
already bounds them. The auto-sync action deduplicates per target repo per turn across all of
its triggers — a commit (`git commit` / `jj commit` / `jj describe` / `ccx vcs ship`), a
claim/start, a merge/pull/fetch, a push (`git push` / `jj git push`), and every cc-notes
write (CLI or MCP) — so the several events of one turn drive a single sync per repo they
touched: the session repo, plus any foreign repo a `cd`-prefixed write landed in. The
Surface floaters carry no cap: their per-record session dedup already bounds them. The two once-per-session
orientations (the session-start task float and the install hint) fire exactly once.

Command triggers match on structured argv-prefix conditions, not regexes: any leg of a
compound line matches (`git commit -m x && git push`), a quoted mention (`echo "jj commit
now"`) does not, and the match is a literal prefix — a flag-interleaved form like `git
--no-pager commit` is missed on purpose. A matched leg carrying an effect-nullifying flag —
a `--dry-run`/`-n` push, a `--dry-run` reconcile, a `--help`/`-h` cc-notes invocation — is
dropped, since it publishes or runs nothing.

### The action hooks — side-effecting handlers

A handful of handlers *do* something rather than nudge, all deterministic, idempotent, and
fail-closed. The auto-sync triggers dedup to **at most one sync per target repo per turn** —
a commit and a claim in the same turn sync once. The failure policy is uniform: a repo with
no remote or an offline box is a legitimate state, so the handler is silent (no nag); a
genuine sync failure — a non-fast-forward push rejection, say — surfaces a short "cc-notes
sync failed for <remotes> — run `cc-notes sync --remote <name>` to retry.", naming each failed
wired remote and its own `--remote` retry, or naming the
directory ("cc-notes sync failed in <dir> — run `cc-notes sync` there to retry.") when the
write landed in another repo; and a detached HEAD or a reconcile
error downgrades to a plain sync rather than going silent, since the refs can still ship
even when reconcile can't run.

**Auto-sync.** After a commit (`git commit`, `jj commit`, `jj describe`, `ccx vcs ship`), a
`cc-notes task claim` / `task start`, a `git merge` / `git pull` / `jj git fetch`, a push
(`git push`, `jj git push` — jj's git bridge moves only `refs/heads/*`, never the cc-notes
refs), or any cc-notes write — a mutating CLI subcommand (every noun's write verbs, bare
`reconcile`, and the two-level `task criterion` / `runbook step` / `runbook run` mutations)
or an MCP tool that isn't a known reader (a deny-list of the read tools, so the matcher
fails open: an unlisted future tool costs one harmless idempotent sync) — the pack runs
`cc-notes sync` itself and confirms with
"Synced cc-notes refs." — once per turn across every trigger. The sync covers every
cc-notes-wired remote — each remote whose fetch refspec in git config tracks
`refs/cc-notes/*` — via `cc-notes sync --remote <name>`, falling back to one bare
`cc-notes sync` when none is wired. Reads never sync. This replaces the old
"run cc-notes sync" nudge.

A CLI write can land outside the session repo — `cd /other/repo && cc-notes note add …`
writes the *other* repo's refs. The handler walks the parsed command legs, tracking every
literal `cd` to resolve the directory each write leg runs in, and syncs the written repo,
confirming "Synced cc-notes refs in <dir>." — once per target repo per turn, targets deduped
by realpath. A `cd` it can't resolve structurally — `cd -`, a `$var`, a `~`, a backtick
substitution — falls back to the session repo, and pushd, subshells, and pipeline grouping
are ignored the same way. The cross-repo path covers record writes only: a push, merge, or
claim in another repo keeps session semantics, and an MCP write always targets the session
repo.

**Auto-reconcile.** After a `git merge` / `git pull` / `jj git fetch`, the pack runs
`cc-notes reconcile --into <current branch>` — carrying the merged branch's still-open tasks
onto your branch — then syncs, confirming "Reconciled merged tasks onto <branch>. Synced
cc-notes refs.". The push outcome rides along, so a failed push surfaces the same retry hint
rather than reading as synced. On a detached HEAD (the colocated-jj norm — exactly where
`jj git fetch` runs) or a failed reconcile it falls back to a plain sync — the
fetched refs still ship; the tasks stay put. This replaces the old reconcile-then-sync
nudge. jj merges and rebases match no trigger, so after one of those you still run
`cc-notes reconcile` yourself.

**The SessionEnd backstop.** A write-only session can end without ever hitting a sync
trigger (the memory mirror is the canonical case). At session end an async handler runs a
zero-network dirty check — local `refs/cc-notes/*` tips against their fetched copies under
`refs/cc-notes-sync/<remote>/*`, for every wired remote — and runs `cc-notes sync` only when
some wired remote is missing a local ref or holds a differing tip; a tracking-only ref
(remote ahead) is no push moment. It is silent best-effort end to end — no remote, offline,
and timeout all stay quiet, and async dispatch drops its output anyway. Needs capt-hook >=
9.2, whose captain-hook plugin dispatches `run SessionEnd --async`.

Known limitations of this dirty check: ref-tip equality can't tell that a plain `git push`
already shipped an attachment's git-lfs content, so matching tips read clean even when that
payload never synced; and a remote-ahead ref (tracking newer than local) counts as clean,
since the backstop is a push-moment check, not a pull.

**The memory mirror.** The harness records durable agent memories as
`<slug>.md` files under a `memory/` dir inside a `.cc-pool` tree, and this handler mirrors
the repo-relevant ones into notes so they ride the repo instead of living only in the
harness. A cheap path gate (`MemoryWrite`) rejects everything but a memory slug file before
any disk read; the body is read back from disk so a `Write` and an `Edit` both yield the
final content; and the note is keyed by a `memory:<slug>` label, so the handler upserts —
`note list` to find an existing mirror, then `note edit` in place (skipping the edit when
title and body are unchanged) or `note add`. It mirrors only `feedback`, `project`, and
`reference` memories, never a `user` who-you-are memory or the `MEMORY.md` index. Because it
runs at `PostToolUse` the memory write has already landed, and every cc-notes call falls
closed to silence, so a failed mirror never disturbs it. A memory write is *not* an auto-sync
trigger: the mirror still nudges "Run `cc-notes sync` to share it" rather than syncing for
you, and the SessionEnd backstop sweeps an unpushed mirror at exit. The cc-pool memory tree
is the mirror's alone: the internal-write record router hard-excludes it, so a memory write
is captured once, by the mirror, never also nudged.

### The session bootstrap

The pack bootstraps its own binary. `ensure_cc_notes_binary` runs at `SessionStart` under
async dispatch (the captain-hook plugin's `run SessionStart --async`): it installs the
binary through the canonical installer when missing, reinstalls one older than v0.22.0 (the
flag-cutover floor), then runs `cc-notes mount --auto` — which self-gates on the repo's
opt-in (`cc-notes.autoMount=true`, set by `cc-notes init` unless you pass `--no-mount`) and
on a fuse-capable binary, adopts an already-live mount with zero overhead, and is quiet and
best-effort. Async dispatch drops the handler's output, so the availability line the agent
reads comes from `announce_cc_notes_available`, a once-per-session `UserPromptSubmit` nudge
that surfaces the installed version at the first prompt. The install reminder above speaks
only when the bootstrap couldn't land a binary.

## Silent unless cc-notes is installed

Every *workflow* nudge is gated behind the `CcNotesAvailable` condition, which
requires exactly one thing: the `cc-notes` binary on `PATH`. There is no
`refs/cc-notes/*` ref check — gating on that would be a chicken-and-egg wall,
since the adoption nudges that prompt the *first* cc-notes write would never fire
in a fresh repo that has no refs yet.

The one exception is the install reminder, gated on the inverse `CcNotesMissing`
(binary *not* on `PATH`). It's the only thing that speaks when the binary is absent,
so an opt-in repo whose auto-install didn't land a binary still gets a hint instead
of silence across the board.

The per-repo opt-in is the pack's **presence** in `.claude/hooks/packs.toml`,
which `cc-notes hooks install` records. A repo that doesn't want these nudges
leaves the pack out. Where the pack is enabled but the repo has no cc-notes data
yet, the Surface floaters shell out to `cc-notes` and get nothing back,
so they fall closed to silence on their own. `run_cc_notes` returns `None` on any
failure (missing flag, non-zero exit, timeout) and the parse helpers turn empty
output into nothing to render. The triggers cover the jj surface too —
`jj commit` / `jj describe`, `jj git push`, and `jj git fetch` all sync on their
own; only a jj merge or rebase still calls for a by-hand `cc-notes reconcile`,
since no matched command marks one.

## Install

```console
$ cc-notes hooks install
```

This runs `uvx --isolated capt-hook pack add github:yasyf/cc-notes@latest`, which resolves
`@latest` to the newest release, caches the pack tarball, and records
`[packs.cc-notes]` in `.claude/hooks/packs.toml` — that file is all it writes.
The event wiring ships in the captain-hook plugin's own `hooks.json`; enable that
plugin (the dispatcher) with `uvx --isolated capt-hook skills install`, which `cc-notes init`
runs before the pack add — without it the pack is installed but dormant.
(`--isolated` keeps a machine-wide `uv tool install capt-hook` from silently
pinning `uvx` to a stale environment.) The source is unpinned on purpose:
the pack tracks `@latest`, and capt-hook
re-resolves it at most once a day and auto-fetches new releases, so the nudges
stay current on their own.

The pack cache (`~/.cache/captain-hook`) isn't committed, but `packs.toml` is, and
capt-hook auto-fetches the declared pack on the next hook event — a teammate who
clones the repo needs only the captain-hook plugin enabled. The SessionEnd
backstop needs capt-hook >= 9.2, the first release whose plugin dispatches
`run SessionEnd --async`.

## Test

The hooks carry inline tests. Run them against the module directory:

```console
$ uvx capt-hook --hooks plugin/hooks test
```

Each nudge declares its own `tests={Input(...): Warn()/Allow()}` cases covering a
firing trigger and a near-miss that must stay silent. The Surface floaters carry one
inline test each, proving a non-matching tool stays silent; their firing path shells
out to `cc-notes`, so the inline harness (which stubs only `call_llm`, never the CLI
subprocess) cannot assert it deterministically.

The Record routers are LLM-gated, so their inline `tests={...}` cover only the cheap
static gate: the inline harness stubs `call_llm` to its default verdict, which records
nothing, so a positive can never fire there. What the gate lets through, what the model
routes to (note vs doc vs log vs task vs papercut), the always-on commit and plan teaches, and the
per-key dedup are proven in `tests/test_cc_notes.py`, which stubs `evt.ctx.call_llm`
(and `evt.ctx.git`) directly.

Two handlers carry no inline tests at all: `sync_at_session_end` and
`announce_cc_notes_available`. This repo self-adopts the pack, so their firing paths run
real side-effects — a real sync, a real `cc-notes version` shell-out — the inline harness
can't contain; their coverage lives in `tests/test_cc_notes.py`. The `record.py` inline
tests assert each nudge's branch-invariant core, so they pass with or without a live
cc-notes MCP marker present. `mcp_active` swaps the suggested commands between MCP-tool and
CLI wording, but both branches share that core. The exact per-branch wording is proven in
`tests/test_cc_notes.py`, which sets a real session flag to drive the MCP branch.

The Surface floaters split into thin event wiring over pure helpers for parsing,
rendering, dedup, drift filtering, the precision filter, and task capping. Those
helpers, both gate branches (binary present opens it, binary absent fails it closed)
with `shutil.which` mocked, and the firing handlers with stubbed CLI and LLM output
have direct unit tests in `tests/test_cc_notes.py`:

```console
$ uv run plugin/hooks/tests/test_cc_notes.py
```
