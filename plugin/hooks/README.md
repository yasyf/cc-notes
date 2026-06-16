# cc-notes enforcement hooks

`cc_notes.py` is a [capt-hook](https://pypi.org/project/capt-hook/) hook module that
nudges agents to keep cc-notes in step with the git work they do. It ships with
cc-notes and is what `cc-notes hooks install` wires into a target repo.

These are **nudges, never gates**. cc-notes complements Claude's native task
tracking, so every hook only ever warns. None of them can block a tool call.

## What the hooks teach

The whole module exists to make one distinction reflexive:

| Tool | Lifetime | Scope | Use for |
|------|----------|-------|---------|
| Native `TaskCreate`/`TaskUpdate` | This session only | This agent, private | In-session steps of the task you're doing right now |
| `cc-notes task` | Durable, git-synced | Branch-scoped | Work that outlives the session or coordinates agents, with claim, deps, and lifecycle |
| `cc-notes note` | Durable, git-synced | Repo-global | Design decisions and durable facts |

## The four nudges

| Trigger | Nudge |
|---------|-------|
| `git merge` / `git pull` (PostToolUse) | Merged-branch tasks are invisible until promoted, so run `cc-notes reconcile`, then `cc-notes sync`. |
| `git commit` (PostToolUse) | Capture durable decisions as `cc-notes note add`, and `cc-notes sync` to share your refs. |
| `ExitPlanMode` (PostToolUse) | Native todos are your private scratchpad; durable/cross-branch work is a `cc-notes task`, decisions are a `cc-notes note`. |
| Many open native tasks after `TaskCreate` | Mirror the durable or cross-branch ones into `cc-notes task` so they survive and coordinate. |

## Silent unless the repo uses cc-notes

Every nudge is gated behind the `CcNotesAdopted` condition, which requires **both**:

1. the `cc-notes` binary on `PATH`, and
2. at least one `refs/cc-notes/*` ref in the repo, which `cc-notes init` creates.

Installed into a repo that hasn't adopted cc-notes, the module stays inert, with
no output and no overhead beyond a single `git for-each-ref`. The `reconcile`
nudge helps `jj` users too. `jj` never runs git hooks, so `reconcile` is the
explicit step they run by hand after a merge.

## Install

```console
$ cc-notes hooks install
```

This drops `cc_notes.py` into the repo's `.claude/hooks/` and merges the
capt-hook event wiring into `.claude/settings.json`. capt-hook runs via `uvx`, so
there is nothing else to install.

The merged settings block:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "uvx capt-hook run UserPromptSubmit" }] }
    ],
    "PreToolUse": [
      { "hooks": [{ "type": "command", "command": "uvx capt-hook run PreToolUse" }] }
    ],
    "PostToolUse": [
      { "hooks": [{ "type": "command", "command": "uvx capt-hook run PostToolUse" }] }
    ]
  }
}
```

One dispatcher per event reads the payload on stdin, evaluates every registered
hook in `.claude/hooks/`, and emits at most one nudge. The cc-notes nudges all
fire on `PostToolUse`; `UserPromptSubmit` and `PreToolUse` are wired so other
hook modules in the same directory keep working.

## Test

The hooks carry inline tests. Run them against the module directory:

```console
$ uvx capt-hook --hooks plugin/hooks test
13 tests: 13 passed, 0 failed, 0 errors, 0 skipped
```

Each `nudge(...)` declares its own `tests={Input(...): Warn()/Allow()}` cases
covering the firing command and a near-miss that must stay silent.
