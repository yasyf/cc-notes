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

## The nudges

| # | Trigger | Nudge |
|---|---------|-------|
| 1 | Session start, first `UserPromptSubmit`, fires once | Float this session's durable tasks: your branch's open/in-progress tasks topped up from the shared backlog, capped at seven with a `+K more` tail, pointing at `cc-notes status`. Silent when there are no tasks. |
| 2 | `Read` (PostToolUse) | Float the notes, docs, and logs `cc-notes relevant <path>` ranks for the file just read — title, reasons, and any drift flag — so durable context surfaces as you explore. Each entry floats once per session. |
| 3 | `Edit` / `Write` / `MultiEdit` (PostToolUse) | After an edit, `cc-notes relevant <path> --attached --worktree` checks the notes and docs anchored to that path; any with a non-null drift verdict prompt reconciliation via `verify`, `edit`, or `supersede` on the matching kind (`cc-notes note …` or `cc-notes doc …`). Each entry is asked about once per session. |
| 4 | `ExitPlanMode` (PostToolUse) | Native todos are your private scratchpad; durable shared work is `cc-notes task add --backlog`, branch-specific work is plain `cc-notes task add`, and decisions are `cc-notes note add`. |
| 5 | `git commit` (PostToolUse) | Add a `cc-task: <id>` trailer to link the commit, capture durable decisions as `cc-notes note add ... --tag design`, and `cc-notes sync` to share. |
| 6 | `git merge` / `git pull` (PostToolUse, max 3) | A merged branch's open tasks stay put until carried over, so run `cc-notes reconcile --into <target>`, then `cc-notes sync`. |
| 7 | `cc-notes task claim` / `task start` (PostToolUse, max 2) | You hold a lease now, so `cc-notes sync` to let other agents see the claim, `task renew` on long work, `task done` when finished, `task claim --steal` to reclaim a crashed hold. |
| 8 | Many open native tasks after `TaskCreate` (max 2) | Mirror durable or cross-agent items into `cc-notes task add`, to the backlog if they're shared. |
| 9 | `cc-notes` binary missing from `PATH`, first `UserPromptSubmit`, fires once | The pack is enabled but the binary is missing, so name the two install paths — `brew install yasyf/tap/cc-notes` or `curl -fsSL …/install.sh \| sh` — to break the silent dead-end. |
| 10 | `Write` / `Edit` of a `.md` file (PostToolUse, LLM-gated, max 2) | A cheap name/dir/size pre-gate filters out public files (`README`, `CHANGELOG`, `docs/`, anything under ~600 chars) before any model call; an LLM then classifies the surviving body, and when it reads as an internal agent-handoff the nudge points at `cc-notes doc add "<title>" --when "<when>" --dir <area> --body -` — store the brief as a doc that surfaces to the next agent automatically, not a loose `.md` nobody opens. |
| 11 | `Write` / `Edit` / `MultiEdit` of a cc-pool memory file (PostToolUse) | Mirror a repo-relevant agent memory (`feedback` / `project` / `reference`) into a durable note keyed by a `memory:<slug>` tag — the first write creates it, a later edit updates the same note. The `MEMORY.md` index and `user` memories are skipped. It is the one handler that *writes* (an idempotent note upsert), yet it still never blocks and falls closed to silence on any failure. |

Nudges 1–3 shell out to `cc-notes` and render its live state (tasks, relevant
notes, docs, and logs, drift verdicts) into the nudge. Nudges 2 and 3 dedup per
entry per purpose: entry ids floated as Read context (nudge 2) and entry ids
asked about for staleness (nudge 3) live in two separate per-session sets, so a
single entry can be floated as context once *and* prompt reconciliation once. Nudges 1 and 8 are reflexes
about the native-vs-durable line; 4 through 7 keep the git workflow and cc-notes
coordination in lockstep.

Nudge 10 is the lone LLM-gated detector. It watches `.md` writes, filters out
public files with a cheap name/dir/size pre-gate, and only then asks the model
whether the body reads as an internal agent-handoff — nudging the long-form brief
into `cc-notes doc add … --when …` instead of a loose file the next agent never
opens. The pre-gate runs first so the model is never called for an obvious
`README` or a one-line note, and `max_fires=2` caps it per session.

Nudge 11 is the lone *side-effecting* handler. The harness records durable agent
memories as `<slug>.md` files under a `memory/` dir inside a `.cc-pool` tree, and
this handler mirrors the repo-relevant ones into notes so they ride the repo
instead of living only in the harness. A cheap path gate (`MemoryWrite`) rejects
everything but a memory slug file before any disk read; the body is read back from
disk so a `Write` and an `Edit` both yield the final content; and the note is keyed
by a `memory:<slug>` tag, so the handler upserts — `note list` to find an existing
mirror, then `note edit` in place (skipping the edit when title and body are
unchanged) or `note add`. It mirrors only `feedback`, `project`, and `reference`
memories, never a `user` who-you-are memory or the `MEMORY.md` index. Because it
runs at `PostToolUse` the memory write has already landed, and every cc-notes call
falls closed to silence, so a failed mirror never disturbs it.

Nudge 9 is the visible fallback for the plugin's auto-installer. Enabling the
cc-notes plugin runs a `SessionStart` hook (`scripts/ensure-cc-notes.sh`) that is
the primary auto-install path — Homebrew-preferred, the release download as
fallback — so the binary is usually present before the first prompt. Nudge 9
speaks only when that bootstrap couldn't produce a binary.

That same `SessionStart` hook also runs `cc-notes mount --auto` — the
session-start ensure-mount — alongside the auto-install. `mount --auto`
self-gates on the repo's opt-in (`cc-notes.autoMount=true`, set by `cc-notes
init` unless you pass `--no-mount`) and on a fuse-capable binary, adopts an
already-live mount with zero overhead, and is quiet and best-effort, so it never
fails the session. This is a shell `SessionStart` hook, not a capt-hook nudge, so
it carries no entry in the nudge table above.

## Silent unless cc-notes is installed

Every *workflow* nudge is gated behind the `CcNotesAvailable` condition, which
requires exactly one thing: the `cc-notes` binary on `PATH`. There is no
`refs/cc-notes/*` ref check — gating on that would be a chicken-and-egg wall,
since the adoption nudges that prompt the *first* cc-notes write would never fire
in a fresh repo that has no refs yet.

The one exception is nudge 9, gated on the inverse `CcNotesMissing` (binary *not*
on `PATH`). It's the only thing that speaks when the binary is absent, so an
opt-in repo whose auto-install didn't land a binary still gets a hint instead of
silence across the board.

The per-repo opt-in is the pack's **presence** in `.claude/hooks/packs.toml`,
which `cc-notes hooks install` records. A repo that doesn't want these nudges
leaves the pack out. Where the pack is enabled but the repo has no cc-notes data
yet, the read-time floaters (1–3) shell out to `cc-notes` and get nothing back,
so they fall closed to silence on their own. `run_cc_notes` returns `None` on any
failure (missing flag, non-zero exit, timeout) and the parse helpers turn empty
output into nothing to render. The reconcile nudge
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

Each static nudge declares its own `tests={Input(...): Warn()/Allow()}` cases
covering a firing trigger and a near-miss that must stay silent. The PostToolUse
floaters (2 and 3) carry one inline test each, proving a non-matching tool stays
silent; their firing path shells out to `cc-notes`, so the inline harness (which
stubs only `call_llm`, never the CLI subprocess) cannot assert it deterministically.

The handoff detector (nudge 10) is LLM-gated, so its inline `tests={...}` cover
only the cheap pre-gate (every case an `Allow()`): the inline harness stubs
`call_llm` to the default `is_handoff=False` verdict, so a positive can never fire
there. The firing/public split — an internal handoff warns, a public `.md` stays
silent, and an exempt path skips the model entirely — is proven in
`tests/test_cc_notes.py`, which stubs `evt.ctx.call_llm` directly.

Handlers 1–3 split into thin event wiring over pure helpers for parsing,
rendering, dedup, drift filtering, and task capping. Those helpers, both gate
branches (binary present opens it, binary absent fails it closed) with
`shutil.which` mocked, and a firing handler with stubbed CLI output have direct
unit tests in `tests/test_cc_notes.py`:

```console
$ uv run plugin/hooks/tests/test_cc_notes.py
```
