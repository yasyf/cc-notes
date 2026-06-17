# Choosing native todos, cc-notes tasks, and cc-notes notes

Three tools record "things to remember," and picking the wrong one is the most common
mistake. The native todo tool, `cc-notes task`, and `cc-notes note` differ along two
axes: how long the record lives, and who can see it. Get those two right and the choice is
mechanical.

## The two axes

**Lifetime.** Native todos are ephemeral — they exist for this session and vanish when it
ends. cc-notes tasks and notes are durable — they are git objects on `refs/cc-notes/*`, so
they persist across sessions, machines, and agents and ride the repo's normal push and pull.

**Scope.** Native todos are private to the current agent. A cc-notes task is global: it
lives at a single flat ref and any agent who syncs the repo can see it. Its branch is a
mutable attribute, not its identity — `task list` and `task ready` default to the tasks on
your current branch, but the shared backlog (tasks with no branch) is visible to every
agent on every branch. cc-notes notes are repo-global and shared the same way.

| Tool | Lifetime | Scope | Records |
|------|----------|-------|---------|
| Native todos | Session only | This agent, private | The steps of the task I am doing right now |
| `cc-notes task` | Durable, synced | Global; a branch attribute plus a shared backlog | A unit of work that outlives the session or coordinates agents |
| `cc-notes note` | Durable, synced | Repo-global, shared | A decision or fact worth remembering |

## The decision

Ask, in order:

1. **Will this matter after the session ends?** If no, use a native todo; if yes, reach for
   cc-notes.
2. **Is it work to do, or a fact to remember?** Work is a `cc-notes task`; a fact or
   decision is a `cc-notes note`.
3. **If it is work, who should pick it up?** Anyone — drop it in the shared backlog with
   `cc-notes task add --backlog`. Only this line of work — file it on your current branch
   with a plain `cc-notes task add`.

That is the whole rule. The one nuance: native todos and cc-notes tasks are not exclusive.
Decompose a durable cc-notes task into in-session native todos while you execute it — the
cc-notes task is the durable unit of work, the native todos are your private scratchpad for
finishing it.

## Worked examples

**Implementing a function across three files, this session.**
Native todos. The breakdown — edit the model, wire the CLI, add a test — is scaffolding for
*how you are doing the current task right now*. None of it should outlive the session, and
no other agent needs to see it. `TaskCreate` each step, `TaskUpdate` as you go, and let it
evaporate when you finish.

**A bug you found but will not fix today.**
`cc-notes task --backlog`. It must survive this session, and another agent (or you, next
week) should be able to find it in the shared queue and pick it up — so it belongs in the
backlog, not pinned to whatever branch you happen to be on.

```console
$ cc-notes task add "Login retries ignore the backoff ceiling" --type bug --priority 1 --label auth --backlog --criterion "login retries honor the backoff ceiling"
3f9a1c2	open	P1	-	Login retries ignore the backoff ceiling
```

**Why the API client retries the way it does.**
`cc-notes note`. It is a decision, not a unit of work, and it is a fact about the codebase
that future readers need — anchor it to the file so the note is born verified against the
current content and drift is computed for you later.

```console
$ cc-notes note add "Retry backoff caps at 30s" --path internal/api/client.go --tag design --body "The server drops connections past 30s, so exponential backoff is clamped. Do not raise the ceiling without checking the gateway timeout."
b71e0d4	2026-06-16	design	Retry backoff caps at 30s
```

**Decomposing that bug fix once you pick it up.**
Both. The bug is a durable `cc-notes task` sitting in the backlog — `task start` it, which
atomically claims it (deterministic first-wins) and moves it onto your current branch,
opening a lease:

```console
$ cc-notes task start 3f9a1c2
3f9a1c2	in_progress	P1	ada <ada@example.com>	Login retries ignore the backoff ceiling
```

Now `TaskCreate` the in-session steps (reproduce, patch, test) as private todos. When the
fix lands, `cc-notes task done 3f9a1c2` closes the durable record and anchors your HEAD
commit onto it; the native todos vanish with the session.

**A multi-agent feature with dependencies.**
`cc-notes task --backlog`, with `dep` to express ordering. The tasks are durable and shared,
so several agents draw ready work from the same backlog; the dependency keeps the schema
migration ahead of the code that reads it.

```console
$ cc-notes task add "Migrate sessions table" --label db --backlog --no-validation-criteria
9c4e2a1	open	P2	-	Migrate sessions table
$ cc-notes task add "Read sessions from the new schema" --label api --backlog --no-validation-criteria
e0b8f73	open	P2	-	Read sessions from the new schema
$ cc-notes task dep e0b8f73 9c4e2a1
e0b8f73	open	P2	-	Read sessions from the new schema
```

`cc-notes task ready --backlog` now surfaces the migration but hides the dependent task
until the migration closes — so the agent that grabs ready work with `task start 9c4e2a1`
takes the migration onto its branch, and the reader stays parked in the backlog until the
blocker is done. No agent can start the reader early.

## Anti-patterns

- **Native todos for cross-session work.** The session ends and the work is lost. If it must
  survive, it is a `cc-notes task`.
- **A cc-notes note for an action item.** Notes are facts, not a queue — they have no claim,
  lease, status, or ready-list. Track work as a `cc-notes task`.
- **A cc-notes task for in-session steps.** A durable, synced task for "edit this file next"
  clutters the shared view with one agent's transient scaffolding. Keep those in native
  todos.
- **Shared work filed on your branch instead of the backlog.** A plain `task add` lands the
  task on your current branch, where it stays out of other agents' default view until you
  `task move` or merge. If anyone could pick it up, use `--backlog` so it is visible to
  every agent from the start.
- **A cc-notes note no one can place.** An unanchored note about a specific file rots
  silently. Anchor decisions to a `--path` or `--commit` so the note is born verified and
  drift is computed against the real code.
