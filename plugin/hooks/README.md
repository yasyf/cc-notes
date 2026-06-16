# cc-notes nudge hooks

`cc_notes.py` is a [capt-hook](https://pypi.org/project/capt-hook/) hook module
that nudges agents to keep cc-notes in step with the git work they do. It ships
with cc-notes and is what `cc-notes hooks install` wires into a target repo.

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

Tasks are global. The id addresses a task no matter which branch it lives on, and
its branch is a mutable attribute. `cc-notes task add --backlog` parks work in the
shared queue; `cc-notes task start <id>` claims it and pulls it onto your branch.

## The six nudges

| # | Trigger | Nudge |
|---|---------|-------|
| 1 | Session start, first `UserPromptSubmit`, fires once | Run `cc-notes status` to see the backlog, your in-progress tasks, and who holds what before picking up work. |
| 2 | `ExitPlanMode` (PostToolUse) | Native todos are your private scratchpad; durable shared work is `cc-notes task add --backlog`, branch-specific work is plain `cc-notes task add`, and decisions are `cc-notes note add`. |
| 3 | `git commit` (PostToolUse) | Add a `cc-task: <id>` trailer to link the commit, capture durable decisions as `cc-notes note add ... --tag design`, and `cc-notes sync` to share. |
| 4 | `git merge` / `git pull` (PostToolUse, max 3) | A merged branch's open tasks stay put until carried over, so run `cc-notes reconcile --into <target>`, then `cc-notes sync`. |
| 5 | `cc-notes task claim` / `task start` (PostToolUse, max 2) | You hold a lease now, so `cc-notes sync` to let other agents see the claim, `task renew` on long work, `task done` when finished, `task claim --steal` to reclaim a crashed hold. |
| 6 | Many open native tasks after `TaskCreate` (max 2) | Mirror durable or cross-agent items into `cc-notes task add`, to the backlog if they're shared. |

Nudges 1 and 6 are reflexes about the native-vs-durable line; 2 through 5 keep the
git workflow and cc-notes coordination in lockstep.

## Silent unless the repo uses cc-notes

Every nudge is gated behind the `CcNotesAdopted` condition, which requires
**both**:

1. the `cc-notes` binary on `PATH`, and
2. at least one `refs/cc-notes/*` ref in the repo, which `cc-notes init` creates.

Installed into a repo that hasn't adopted cc-notes, the module stays inert, with no
output and no overhead beyond a single `git for-each-ref`. The reconcile nudge
serves `jj` users too. Since `jj` never runs git hooks, `cc-notes reconcile` is the
explicit step they run by hand after a merge.

## Install

```console
$ cc-notes hooks install
```

This drops `cc_notes.py` into the repo's `.claude/hooks/` and merges the capt-hook
event wiring into `.claude/settings.json`. capt-hook runs via `uvx`, so there is
nothing else to install.

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
hook in `.claude/hooks/`, and emits at most one nudge. The session-start nudge
fires on `UserPromptSubmit`; the other five fire on `PostToolUse`. `PreToolUse` is
wired so other hook modules in the same directory keep working.

## Test

The hooks carry inline tests. Run them against the module directory:

```console
$ uvx capt-hook --hooks plugin/hooks test
```

Each `nudge(...)` declares its own `tests={Input(...): Warn()/Allow()}` cases
covering a firing trigger and a near-miss that must stay silent.
