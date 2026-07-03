# cc-notes nudge hooks

`cc_notes.py` is a [capt-hook](https://pypi.org/project/capt-hook/) hook module
that nudges agents to keep cc-notes in step with the git work they do. It ships
as the cc-notes capt-hook pack, enabled in a repo by `cc-notes hooks install`.

These are **nudges, never gates**. cc-notes complements Claude's native task
tracking, so every hook only ever warns, and none can block a tool call.

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
exactly one primitive — note, doc, log, or task — or to nothing. It fails *closed* to
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
| `Write` / `Edit` / `MultiEdit` of an internal-looking file (PostToolUse) | a status/handoff/notes/runbook/`memory/` file the static `DurableInternalWrite` gate flags | note / doc / log / task — or none; the subtle call is doc (living guidance) vs log (append-only chronology) |
| Bash `cp`/`mv`/`rsync` landing run output in a durable tree, or a `Write`/`Edit` of an evidence-suffixed file (`.log`, `.panic`, `.dump`, …) anywhere in it, `docs/**` included (PostToolUse) | the transfer the static `EvidenceArchive` gate flags — temp/scratchpad, `testdata/` fixtures, `.git` internals, and relative in-repo bulk copies stay exempt | (static, no model) a log entry carrying the artifacts as `--attach` git-lfs attachments; only `cc-notes sync` uploads their content (plain `git push` moves refs without it), and a >1MB payload strengthens the wording |
| `git commit` (PostToolUse) | the HEAD commit — message, diffstat, bounded patch | whether the change encodes a durable decision worth a note or doc; the `cc-task:` link reminder always fires regardless, and the sync is an automatic side-effect (see below) |
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
| `git merge` / `git pull` (PostToolUse) | the pack auto-runs `cc-notes reconcile --into <current branch>` (carrying the merged branch's still-open tasks onto your branch) then syncs, confirming "Reconciled merged tasks onto <branch>. Synced cc-notes refs." (a failed push surfaces the retry hint instead; jj merges fire no git hooks, so after a jj merge you still run `cc-notes reconcile` / `sync` yourself) |
| `cc-notes task claim` / `task start` (PostToolUse) | you hold a lease — `task renew` on long work, `task done` when finished, `task claim --steal` to reclaim a crashed hold (sync is automatic now) |
| `cc-notes` binary missing, first `UserPromptSubmit` (once) | the pack is enabled but the binary is off `PATH` — name the two install paths (`brew install yasyf/tap/cc-notes` or `curl -fsSL …/install.sh \| sh`) so an opt-in repo isn't left silent |

### Firing policy

One cap by class. Every Record router and Workflow reminder is capped at `NUDGE_MAX_FIRES`
(three) per session as a backstop, and additionally deduped by its own key — the turn, the
HEAD sha, or the plan path — so it speaks once per real event rather than on every fire. The
auto-sync action deduplicates per turn across all of its triggers (commit, claim/start,
merge/pull), so the several events of one turn drive a single sync. The Surface floaters
carry no cap: their per-record session dedup already bounds them. The two
once-per-session orientations (the session-start task float and the install hint) fire
exactly once.

### The action hooks — side-effecting handlers

Three handlers *do* something rather than nudge, all deterministic, idempotent, and
fail-closed. The auto-sync triggers (a commit, a claim/start, a merge/pull) dedup to **at
most one sync per turn** — a commit and a claim in the same turn sync once. The failure
policy is uniform: a repo with no remote or an offline box is a legitimate state, so the
handler is silent (no nag); a genuine sync failure — a non-fast-forward push rejection, say
— surfaces a short "cc-notes sync failed — run `cc-notes sync` to retry."; and a detached
HEAD or a reconcile error stays silent, since it is local-only and carries no hazard.

**Auto-sync.** After a `git commit`, a `cc-notes task claim` / `task start`, or a
`git merge` / `git pull`, the pack runs `cc-notes sync` itself and confirms with "Synced
cc-notes refs." — once per turn across every trigger. This replaces the old "run cc-notes
sync" nudge.

**Auto-reconcile.** After a `git merge` / `git pull`, the pack runs
`cc-notes reconcile --into <current branch>` — carrying the merged branch's still-open tasks
onto your branch — then syncs, confirming "Reconciled merged tasks onto <branch>. Synced
cc-notes refs.". The push outcome rides along, so a failed push surfaces the same retry hint
rather than reading as synced. This replaces the old reconcile-then-sync nudge. jj merges fire
no git hooks, so after a jj merge you still run `cc-notes reconcile` / `sync` yourself.

**The memory mirror.** The harness records durable agent memories as
`<slug>.md` files under a `memory/` dir inside a `.cc-pool` tree, and this handler mirrors
the repo-relevant ones into notes so they ride the repo instead of living only in the
harness. A cheap path gate (`MemoryWrite`) rejects everything but a memory slug file before
any disk read; the body is read back from disk so a `Write` and an `Edit` both yield the
final content; and the note is keyed by a `memory:<slug>` tag, so the handler upserts —
`note list` to find an existing mirror, then `note edit` in place (skipping the edit when
title and body are unchanged) or `note add`. It mirrors only `feedback`, `project`, and
`reference` memories, never a `user` who-you-are memory or the `MEMORY.md` index. Because it
runs at `PostToolUse` the memory write has already landed, and every cc-notes call falls
closed to silence, so a failed mirror never disturbs it. A memory write is *not* an auto-sync
trigger: the mirror still nudges "Run `cc-notes sync` to share it" rather than syncing for
you. The cc-pool memory tree is the mirror's alone: the internal-write record router
hard-excludes it, so a memory write is captured once, by the mirror, never also nudged.

### The session-start mount

Enabling the cc-notes plugin runs a `SessionStart` hook (`scripts/ensure-cc-notes.sh`) that
auto-installs the binary — Homebrew-preferred, the release download as fallback — so it is
usually present before the first prompt; the install reminder above speaks only when that
bootstrap couldn't produce one. That same hook also runs `cc-notes mount --auto`, which
self-gates on the repo's opt-in (`cc-notes.autoMount=true`, set by `cc-notes init` unless
you pass `--no-mount`) and on a fuse-capable binary, adopts an already-live mount with zero
overhead, and is quiet and best-effort. Both are shell `SessionStart` hooks, not capt-hook
nudges.

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
output into nothing to render. The auto-reconcile action
serves `jj` users too. Since `jj` never runs git hooks, `cc-notes reconcile` is
the explicit step they run by hand after a merge.

## Install

```console
$ cc-notes hooks install
```

This runs `uvx capt-hook pack add github:yasyf/cc-notes@latest`, which resolves
`@latest` to the newest release, caches the pack tarball, records
`[packs.cc-notes]` in `.claude/hooks/packs.toml`, and regenerates the event wiring
in `.claude/settings.local.json`. The source is unpinned on purpose: the pack
tracks `@latest`, and capt-hook re-resolves it at most once a day and auto-fetches
new releases, so the nudges stay current on their own. capt-hook derives the event
set from the pack, and the dispatcher runs via `uvx`, so there is nothing else to
install.

The pack cache (`~/.cache/captain-hook`) and `.claude/settings.local.json` aren't
committed, but capt-hook auto-fetches the declared pack on the next hook event, so a
teammate who clones the repo only re-runs `cc-notes hooks install` to regenerate the
local event wiring.

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
routes to (note vs doc vs log vs task), the always-on commit and plan teaches, and the
per-key dedup are proven in `tests/test_cc_notes.py`, which stubs `evt.ctx.call_llm`
(and `evt.ctx.git`) directly.

The Surface floaters split into thin event wiring over pure helpers for parsing,
rendering, dedup, drift filtering, the precision filter, and task capping. Those
helpers, both gate branches (binary present opens it, binary absent fails it closed)
with `shutil.which` mocked, and the firing handlers with stubbed CLI and LLM output
have direct unit tests in `tests/test_cc_notes.py`:

```console
$ uv run plugin/hooks/tests/test_cc_notes.py
```
