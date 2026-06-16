---
name: using-cc-notes
description: >-
  Use cc-notes to record durable tasks and notes that outlive a session, stored as
  git objects on refs/cc-notes/*. Triggers when an agent needs to record a task or
  note for later, asks what work is open or wants to claim a task, coordinates work
  across branches or multiple agents, persists a design decision or durable fact,
  syncs notes and tasks with a remote, or reconciles tasks after merging a branch.
  Covers the distinction between the harness's ephemeral in-session todos and
  cc-notes' durable, synced, branch-scoped tasks and repo-global notes, plus the
  canonical add/ready/claim/done flow, note hygiene, and the reconcile-after-merge
  step.
allowed-tools: Bash(cc-notes:*), Read
---

# Using cc-notes

cc-notes is a git-native notes and tasks layer for agents. Tasks and notes live as
objects on hidden `refs/cc-notes/*` refs inside the repo's object database — versioned,
synced by plain `git push`/`git pull`, and invisible in checkouts and diffs. Tasks are
branch-scoped; notes are repo-global with optional anchors to a commit, path, or branch.
Reach for cc-notes when work or knowledge must survive the current session or reach
another agent. Keep in-session step tracking in the harness's own todo tool.

## The distinction: native todos vs cc-notes task vs cc-notes note

This is the one thing to get right. Three tools, three lifetimes.

| Tool | Lifetime | Scope | Use for |
|------|----------|-------|---------|
| Native todos (`TaskCreate`/`TaskUpdate`) | Ephemeral — this session only, gone at session end | This agent's private scratchpad | Decomposing the *current* task into in-session steps |
| `cc-notes task` | Durable — git ODB, synced across machines and agents | Branch-scoped (`refs/cc-notes/tasks/<branch>/<id>`) | Work that outlives the session or coordinates agents: claim, deps, comments, priority, lifecycle |
| `cc-notes note` | Durable — git ODB, synced | Repo-global, anchorable to commit/path/branch | Design decisions and durable facts, full-text searchable |

cc-notes complements native todos rather than replacing them. Keep the in-session
checklist for what you are doing right now in native todos. Capture work that must persist
or be picked up by someone else as a `cc-notes task`. Record a decision or durable fact as
a `cc-notes note`. When in doubt: if closing the session would lose something that matters,
it belongs in cc-notes. See `references/tasks-vs-notes.md` for worked examples.

## Canonical agent flow

Run `init` once per repo, then capture, claim, and close durable work as you go.

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
```

`init` installs the refspecs so plain `git push` and `git pull` carry the refs alongside
your branches. On a branch, capture work as you discover it:

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1 --label api
d82c087	open	P1	-	Add retry backoff to the API client
```

Every mutation echoes the entity's new state as a lean tab-separated line. List what an
agent can pick up right now — open, unassigned, no open blockers — then claim and close:

```console
$ cc-notes task ready
d82c087	open	P1	-	Add retry backoff to the API client
$ cc-notes task claim d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

Record a durable decision, anchored to the file it describes:

```console
$ cc-notes note add "Auth tokens expire after 15 minutes" --path services/auth/login.go --tag design --body "Refresh client-side before expiry; the API returns 401 with no Retry-After header."
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes
```

Commit your code with plain git. After merging another branch into this one, promote the
merged branch's open and in-progress tasks here, then converge with the remote:

```console
$ cc-notes reconcile --into main
scanned: 1
merged: 1
promoted: 2
into: main
feature/x:
08118da	open	P1	-	build the widget
b932fd9	open	P2	-	test the widget
$ cc-notes sync
pushed: 2
rounds: 1
```

## Command cheat-sheet

The verbs you reach for most. Full surface, flags, and output shapes are in
`references/cli-reference.md`.

| Command | Purpose |
|---------|---------|
| `cc-notes init` | Install the `refs/cc-notes/*` refspecs on a remote (once per repo) |
| `cc-notes sync` | Converge all cc-notes refs with the remote and push |
| `cc-notes reconcile --into <branch>` | Promote a merged branch's open tasks into the target branch |
| `cc-notes task add "<title>"` | Capture durable work on the current branch |
| `cc-notes task ready` | List unblocked, unassigned, open tasks — the pickup queue |
| `cc-notes task list` | List tasks on a branch (defaults to open + in_progress) |
| `cc-notes task claim <id>` | Take an open, unassigned task (sets assignee to the git user) |
| `cc-notes task done <id>` | Close a task |
| `cc-notes task dep <id> <blocker>` | Mark a task blocked by another |
| `cc-notes task comment <id> "<text>"` | Append a comment for cross-agent context |
| `cc-notes note add "<title>"` | Record a durable fact or decision |
| `cc-notes note search "<query>"` | Full-text search notes by title, body, and tags |
| `cc-notes note list` | List notes, filterable by tag or anchor |

Append `--json` to any note, task, sync, or reconcile command for a machine-readable
record instead of the lean line.

## Note hygiene

Notes are durable, so stale ones mislead. Keep them honest:

- **Supersede, don't accumulate.** When a decision changes, edit the note in place with
  `cc-notes note edit <id> --body "…"` so the record reflects current reality. Retire a
  note once it no longer applies with `cc-notes note rm <id>` (a tombstone — the history
  survives, the note drops out of listings).
- **Tag lifecycle state.** Adopt a `stale` or `superseded` tag convention via
  `cc-notes note edit <id> --add-tag superseded` when you keep a note for context but it
  no longer holds. Filter it out with the ANDed `--tag` flags on `list` and `search`.
- **Anchor to a commit or path.** Attach `--commit <sha>` or `--path <file>` on
  `note add` so a note is pinned to the code it describes. When that code moves or the
  commit ages out, the drift is visible — an unanchored note silently rots.

## References

- `references/cli-reference.md` — the complete command surface: every flag, default, and
  the lean-line and `--json` output shape for each command.
- `references/coordination.md` — how agents coordinate over time: branches as task
  namespaces, claim and assignee, deps and blocking, promote vs reconcile, union-merge
  sync, and what happens on branch merge, delete, and rename.
- `references/tasks-vs-notes.md` — a deeper treatment of the three-way distinction with
  worked examples of choosing native todo vs cc-notes task vs cc-notes note.
