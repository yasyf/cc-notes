# cc-notes Development Guide

Git-native notes and tasks layer for agents, written in Go (module `github.com/yasyf/cc-notes`). Ships as a single static binary `cc-notes`, distributed via GitHub Release assets. All data lives as objects in the git ODB on `refs/cc-notes/*` — synced with the repo, invisible in checkouts.

## Repository Structure

```
cc-notes/
├── cmd/cc-notes/     # Binary entrypoint — signal-aware main, exit-code mapping
├── model/            # Public domain vocabulary — entity ids, Note/Doc/Log/Task/Sprint/Project/Runbook snapshots, commit/path/dir/branch anchor kinds, task validation criteria, kind-tagged ops, pack codec
├── notes/            # Public in-process client (notes.Client) — the domain API the CLI and embedders drive: entity CRUD/read/search, status, relevant, history, blame, sync, reconcile, gc
├── internal/         # Go core (not importable outside the module)
│   ├── refs/         #   pure ref-name build/parse (notes global, tasks global with an LWW branch attribute, sprints and projects global)
│   ├── fold/         #   pure CRDT core — linearize + deterministic fold, LWW, claim rule, sprint/project fold and task criterion status, checkpoint replay
│   ├── gitobj/       #   go-git object writes + all reads — ref tips, prefix listings, commit chains (ref writes live in gitcmd)
│   ├── gitcmd/       #   exec git — fetch/push with user credentials, config, update-ref
│   ├── store/        #   entity store: create, append (CAS), load, list, resolve
│   ├── sync/         #   refspec install, sync loop, union merge, reconcile (relocate tasks on merge)
│   ├── trail/        #   per-commit entity change trails — classify fold steps as create/edit/checkpoint, diff snapshots into field deltas
│   ├── viz/          #   branch/entity visualization — swimlane graph + lifecycle events, commit DAG API, loopback HTTP server, SSE ref-watcher
│   ├── cli/          #   cobra command tree: note/task/sprint/project noun groups, task validation criteria + validate, init, sync, mount, viz
│   ├── fusefs/       #   FUSE mount (build tag fuse) — render/parse/diff + cgofuse ops, flat sprint/project files + nested symlink browse tree
│   └── version/      #   ldflags-injected build metadata
├── web/              # viz single-page app (Vite + TypeScript) — dist/ embedded by `-tags webui` builds (embed.go / embed_stub.go)
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
- **Workflow Plan** — required in every plan; a plan without it is incomplete. One line on what the main agent alone does (track state, dispatch, decide, report), then a `Phase | Shape | Agents | Verification` table covering every fan-out the plan anticipates: Shape is `pipeline` / `parallel` / `loop`; Agents names each phase's model and effort per the Models table (e.g. `opus xhigh ×4`, `sonnet low → codex`); Verification names the check that gates each phase's output. When nothing fans out, one line saying everything stays at the main-agent level replaces the table.
- **Verification** — how to prove it works end to end: the exact commands to run, tests to add, and behavior to observe.
