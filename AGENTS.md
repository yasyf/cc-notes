# cc-notes Development Guide

Git-native notes and tasks layer for agents, written in Go (module `github.com/yasyf/cc-notes`). Ships as a single static binary `cc-notes`, distributed via GitHub Release assets. All data lives as objects in the git ODB on `refs/cc-notes/*` — synced with the repo, invisible in checkouts.

## Repository Structure

```
cc-notes/
├── cmd/cc-notes/     # Binary entrypoint — signal-aware main, exit-code mapping
├── internal/         # Go core (not importable outside the module)
│   ├── model/        #   entity ids, Note/Task/Sprint/Project snapshots, commit/path/dir/branch anchor kinds, task validation criteria, kind-tagged ops, pack codec
│   ├── refs/         #   pure ref-name build/parse (notes global, tasks global with an LWW branch attribute, sprints and projects global)
│   ├── fold/         #   pure CRDT core — linearize + deterministic fold, LWW, claim rule, sprint/project fold and task criterion status, checkpoint replay
│   ├── gitobj/       #   go-git object/ref layer, CheckAndSetReference CAS appends
│   ├── gitcmd/       #   exec git — fetch/push with user credentials, config, update-ref
│   ├── store/        #   entity store: create, append (CAS), load, list, resolve
│   ├── sync/         #   refspec install, sync loop, union merge, reconcile (relocate tasks on merge)
│   ├── cli/          #   cobra command tree: note/task/sprint/project noun groups, task validation criteria + validate, init, sync, mount
│   ├── fusefs/       #   FUSE mount (build tag fuse) — render/parse/diff + cgofuse ops, flat sprint/project files + nested symlink browse tree
│   └── version/      #   ldflags-injected build metadata
├── scripts/          # install.sh — curl-able release-binary installer
├── .github/          # GitHub Actions workflows (CI, tag-driven releases; release.yml renders + publishes the Homebrew formula to the shared yasyf/homebrew-tap)
├── AGENTS.md         # This file — shared conventions
└── README.md         # Project overview
```

## Ask Before Assuming

When the user's request has ambiguity — unclear scope, multiple plausible interpretations, undefined edge cases, or unspecified tradeoffs — stop and ask. Propose 2-4 concrete options and let the user pick, or list the assumptions you'd otherwise make and ask which ones hold. There is no such thing as too many questions; one wrong implementation costs more than ten clarifying exchanges. Default to interrogating the user when in doubt — multiple short questions early beat a wrong direction later.

## Code Review Response (Plan Re-Entry)

When the user reviews code you wrote and re-enters plan mode — whether by leaving inline diff comments, pasting a numbered list of issues, or otherwise sending review-shaped feedback after a recent edit cycle — you MUST:

0. **Delegate context-gathering to a subagent.** Spawn one `Explore` subagent with every cite (file:line + the user's verbatim comment text). Instruct it to, per cite, `Grep` the file with ~5 lines of context either side of the cited line (`-B 5 -A 5`), and only escalate to a full `Read` when the ±5-line window is insufficient (e.g. the comment refers to a function defined further up). Have it also surface sibling call sites with the same issue (Grep across the module). Use the subagent's digest as your source of truth when drafting the plan. Do NOT bulk-`Read` the cited files yourself in the main turn — it bloats the main context window before you've even started writing the plan.
1. **Draft a new plan**, not a code change. Plan-mode re-entry is the user asking "let's align on what you'll do next," not "go fix it."
2. **Inline every comment verbatim** in the plan. Each comment gets a short anchor (`#N`, the file:line if provided, or a quoted excerpt) plus the user's exact wording in a blockquote or `*"…"*` italics. Do not paraphrase. The user must be able to scan the plan and see every comment they wrote reproduced exactly.
3. **Cluster when many.** If there are more than ~5 comments, group them into themes (e.g. "T1 — Guards against impossible states") and list every verbatim trigger per theme. Address every cited line *and* extrapolate the rule to other call sites that have the same problem.
4. **Map every comment.** Maintain a "verbatim feedback table" near the end of the plan with one row per comment: `# | file:line | verbatim | cluster`. No comment may be silently dropped.
5. **Do NOT start implementing** before the plan is approved via `ExitPlanMode`. Delegating reads via #0 is fine; editing source is not.

The canonical shape is the `Overarching themes` table + per-cluster `**#N (verbatim):** *"…"*` anchors + final mapping table. When a comment is ambiguous, ask via `AskUserQuestion` rather than guessing.

### Plan follow-up questions

After you write a plan, the user may respond with questions ("why this approach?", "what about X?", "did you consider Y?") rather than approval. In that case you MUST NOT edit the plan to bake in answers. Instead:

1. **Answer the question conversationally** in your text response — explain the reasoning, the tradeoffs, and what you'd recommend.
2. **Propose options via `AskUserQuestion`** — one question per ambiguity, each with 2–4 concrete options the user can pick from. Batch related questions into one `AskUserQuestion` call.
3. **Wait for the user's choice** before editing the plan. The plan edit then reflects the user's pick, not your assumption.

Editing the plan first robs the user of the choice and forces them to diff the plan to find what you decided. Surface the decision point first.

## Parallelize Independent Work

Sequential is the exception, not the default. Two steps that don't consume each other's output run at the same time; when unsure whether they're independent, assume they are and fan out. The orchestrator routes and synthesizes — it never executes work a subagent could. Pick the surface by scale:

- **Batch tool calls in one message** — the cheapest parallelism and the most missed. Independent reads, greps, globs, and read-only Bash go in a *single* message, never one per turn.
- **Parallel subagent calls in one message** — ad-hoc independent investigations: "explore X while I check Y", multi-file reviews, independent edits. One message, N `Agent` tool uses, results gathered in parallel.
- **Dynamic workflow** — default for substantive multi-step work; the script holds the loop, branching, and intermediate results. See CLAUDE.md `## Plan Execution & Orchestration`.
- **Named team** — long-running peers needing agent-to-agent handoffs mid-run, via `TeamCreate`.

Single-step exception: one task, no parallel sibling, no follow-on → one subagent call is fine.

## Writing Plans

When you write a plan — in plan mode, or any "here's what I'll do" before you start editing — use this shape so it's fast to scan and complete enough to execute:

- **Context** — why this change: the problem or need, what prompted it, the intended outcome.
- **Approach** — the recommended approach only (not every alternative you weighed), as ordered steps. Name the critical files to touch; for a pattern repeated across many files, describe it once with a few representative paths instead of listing them all. Cite existing utilities/patterns you'll reuse, with their paths.
- **Potential Pitfalls** — the sharp edges specific to this work: ordering constraints, code that looks safe to change but isn't, prior art that must not be "fixed", state that diverges from how it's described. One bullet each — front-load the gotchas you'd otherwise hit mid-implementation.
- **Workflow Plan** — required in every plan; a plan without it is incomplete. One line on what the main agent alone does (track state, dispatch, decide, report), then a `Phase | Shape | Agents | Verification` table covering every fan-out the plan anticipates: Shape is `pipeline` / `parallel` / `loop`; Verification names the check that gates each phase's output. When nothing fans out, one line saying everything stays at the main-agent level replaces the table.
- **Verification** — how to prove it works end to end: the exact commands to run, tests to add, and behavior to observe.

## Code Search

`semble` is wired up via `.mcp.json` (project-scoped MCP server, runs via `uvx` — nothing to install). It's the default tool for any "find code by intent or symbol" question:

1. **"How do we do X?" / "Where is the code that does Y?"** → `semble.search("...")`
2. **"Where is `Foo` defined?"** → `semble.search("Foo")` (or `search("class Foo")` for a relevance boost)
3. **"Show me other code like this"** → `semble.find_related` on a prior hit
4. **Cross-repo lookup** → pass an `https://...git` URL as `repo`

`repo` defaults to the current project root for local searches. Semble is purely semantic — it ranks by meaning, not substring, so it won't find literal strings that don't appear in nearby code.

Reach for your **LSP** when the answer must be *exhaustive* or *structural*:

1. **"Who calls X?" / "find every reference"** → `findReferences` / `incomingCalls`
2. **"Rename X → Y"** → `findReferences` first to enumerate every call site
3. **"What's the type of X?"** → `hover`
4. **"What implements Protocol P?"** → `goToImplementation`

Reach for **`Grep`** only for material neither tool indexes: literal *content* of strings/comments/docstrings (error messages, hard-coded URLs, env-var names, TODOs) and non-source files (logs, JSON, YAML, fixtures). File-pattern questions ("all `*.json` under `src/`") go through `Glob`.

## Go Style

Target Go 1.26+. Full rules live in STYLEGUIDE.md; the build/test loop:

- **Build**: `go build ./...` (pure Go, `CGO_ENABLED=0` for release binaries)
- **Test**: `go test -race -count=1 ./...`
- **Vet**: `go vet ./...` before every commit
- **Fuse variant**: `go build -tags fuse ./...` needs cgo + a FUSE implementation (fuse-t on macOS, fuse3 on Linux); the default build must stay pure Go.

@STYLEGUIDE.md

## General Rules

**Minimal changes.** Stay within scope; fix the issue, then stop.

**Match surrounding code.** Follow the conventions of the file you're in, then the module.

**No defensive coding.** No fallbacks, shims, or backwards-compat layers; no guards against impossible states. If unused, delete it. Crash on the unexpected.

**Search before writing.** Before creating a helper, query the codebase via `semble.search` (intent or symbol queries both work). Sibling modules and base classes win over re-implementation.

**Code stewardship.** When you touch a file, fix nearby bugs, style violations, and broken tests; don't wave them off as pre-existing or out of scope. Mechanical lint noise is the exception (see § Mechanical linting).

**Observe, don't infer.** Inspect actual data — read fixtures, dump objects, run the code — before reasoning from assumption.

**Don't use external failures as an excuse to stop.** API quota, rate-limit, and outage errors rarely block the whole task; trace the catch sites and confirm a failure actually stops you before claiming it does.

**Verify before asserting.** Don't report something as working, fixed, blocked, or impossible until you've checked — run it, read the output, reproduce the failure. "It should work" is not "it works."

**Reproduce before fixing.** When something breaks, isolate the smallest failing case before editing or re-running. Re-running the whole command while changing code between runs hides the root cause; narrow to the one failing call, payload, or test first.

**Research after repeated failure.** After ~2 failed approaches, stop guessing and gather evidence — search the web, read the docs and source — before a third attempt.

**Get a second opinion on a plateau.** On a debugging plateau (2 failed attempts before a 3rd), a non-trivial architectural decision, or algorithmic/security-sensitive code, get an outside check (e.g. `/codex`) before committing to the approach.

**Don't contort code to satisfy a checker.** The type checker and linter serve the code, not the other way around. Don't reshape a data model, widen a type, or bolt on a `cast(...)` / narrowing-only `assert isinstance(...)` / blanket ignore just to silence a diagnostic. If a clean fix isn't obvious, leave the diagnostic — a visible diagnostic is preferable to scar tissue. (Most checker noise isn't worth acting on at all; act only when it flags a real bug.)

**Mechanical linting.** `gofmt` and `go vet` own formatting and mechanical issues (plus `golangci-lint` if a config lands). Run them before committing; fix only what needs human judgment. When reviewing code, don't flag mechanical lint violations (formatting, import order) — tooling owns them.

**Testing.** Go tests live next to the code as `*_test.go`; run `go test -race -count=1 ./...` from the repo root. Table-driven with named cases, strict assertions against specific expected values. Git is not a mock boundary — tests that need a repository run real `git init` in `t.TempDir()` and operate on the real object database; mock only true externals (network, clock).

**Writing docs.** When writing or revising docs, a README, a tutorial, a how-to, or reference, use the `writing-docs` skill (Diataxis modes, voice rules, and runnable code-sample rules) and run `slop-cop check <file> --lang=markdown` before you finish.

**Version control.** This is a colocated `jj` repo over git — prefer `jj` (`jj describe` / `jj commit`, `jj git push`) for day-to-day work; commits stay atomic and scoped, one logical change each. A dirty tree is just the working-copy commit `@`: to land work on an updated remote, `jj git fetch` then `jj rebase -r <change> -o main@origin` (your in-flight `@` rides along untouched), never `git stash` or a worktree + cherry-pick dance. jj's git bridge carries only `refs/heads/*` — to move this repo's `refs/cc-notes/*` notes/tasks, run `cc-notes sync` (it drives real git); `jj git push` alone leaves them behind.

**Releases.** Tagging `v*` triggers the release workflow, which cross-compiles the platform binaries (`cc-notes_<os>_<arch>`, plus `_fuse` variants built with cgo and `-tags fuse`), generates `SHA256SUMS`, and uploads everything as GitHub Release assets; `scripts/install.sh` resolves the right asset per platform, preferring the `_fuse` variant. The version comes from the tag, injected via `-ldflags` into `internal/version`. Tag merged commits on `main` (e.g. `git tag vX.Y.Z origin/main`), not a feature branch. On stable tags (no `-` suffix) the `bump-formula` job renders the Homebrew formula from the canonical inline template in `release.yml`, filling in the tag version and asset sha256s, then publishes it to the shared `yasyf/homebrew-tap` repo via the `yasyf/homebrew-tap/.github/actions/publish@main` composite action, so `brew install yasyf/tap/cc-notes` tracks the latest stable release. The formula no longer lives in this repo, and prerelease tags skip it. The same job separately bumps the capt-hook pack manifest on cc-notes' own `main`. Never re-tag a published stable version — the `--clobber` re-upload changes asset sha256s out from under installed brew users.
