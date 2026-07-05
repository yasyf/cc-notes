# Choosing native todos, cc-notes tasks, notes, docs, and logs

Five tools record "things to remember," and picking the wrong one is the most common mistake. The native todo tool, `cc-notes task`, `cc-notes note`, `cc-notes doc`, and `cc-notes log` differ along two axes — how long the record lives, and who can see it — plus, for the three durable knowledge records, the *form* the knowledge takes. Get those right and the choice is mechanical.

## The two axes

**Lifetime.** Native todos are ephemeral — they live for one session and vanish when it ends. cc-notes tasks, notes, docs, and logs are durable git objects on `refs/cc-notes/*`, so they persist across sessions, machines, and agents; `git push` carries them out, and `cc-notes sync` folds in what other agents pushed.

**Scope.** Native todos are private to the current agent. A `cc-notes task` is global: it lives at a single flat ref, and any agent who syncs the repo sees it. Its branch is a mutable attribute, not its identity — `task list` and `task ready` default to tasks on your current branch, but the shared backlog (tasks with no branch) is visible to every agent on every branch. cc-notes notes, docs, and logs are repo-global, shared the same way.

| Tool | Lifetime | Scope | Records |
|------|----------|-------|---------|
| Native todos | Session only | This agent, private | The steps of the task I am doing right now |
| `cc-notes task` | Durable, synced | Global; a branch attribute plus a shared backlog | A unit of work that outlives the session or coordinates agents |
| `cc-notes note` | Durable, synced | Repo-global, shared | A one-line decision or fact worth remembering |
| `cc-notes doc` | Durable, synced | Repo-global, shared | Long-form guidance written for the next agent, with a when-to-read trigger |
| `cc-notes log` | Durable, synced | Repo-global, shared | An append-only chronology — each entry an immutable timestamped fact, never edited, optionally carrying attached evidence files |

## The decision

Ask, in order:

1. **Will this matter after the session ends?** No: native todo. Yes: cc-notes.
2. **Is it work to do, or knowledge to remember?** Work is a `cc-notes task`; knowledge — a fact, a guide, or a running record — is a note, a doc, or a log (next question).
3. **A standing fact, living guidance, or a growing chronology?** A single verified fact or decision is a `cc-notes note`. Multi-paragraph guidance written *for the next agent* — a handoff, a current-state brief, a *read this before you touch X* — is a `cc-notes doc`, carrying a free-text `--when` trigger that says when the next agent should read it. A chronology you keep adding to over time — an incident timeline, a rollout log, a debugging session — is a `cc-notes log`: each `log append` is an immutable timestamped entry, and the log never drifts because it never claims to be current truth. Machine-generated evidence from a run — logs, panic dumps, repro archives — rides the entry as `--attach` files; only a human-facing, publishable report belongs in the repo tree.
4. **If it is work, who picks it up?** Anyone — drop it in the shared backlog with `cc-notes task add --backlog`. Only this line of work — file it on your current branch with a plain `cc-notes task add`.

Native todos and cc-notes tasks are not exclusive. Decompose a durable task into in-session native todos while you execute it: the cc-notes task is the durable unit of work, the native todos are your private scratchpad for finishing it.

## Worked examples

**Implementing a function across three files, this session.** Native todos. The breakdown — edit the model, wire the CLI, add a test — is scaffolding for *how* you do the current task right now. None of it should outlive the session, and no other agent needs to see it.

**A bug you found but will not fix today.** `cc-notes task --backlog`. It must survive the session, and another agent (or you, next week) should find it in the shared queue and pick it up — so it belongs in the backlog, not pinned to whatever branch you happen to be on.

```console
$ cc-notes task add "Login retries ignore the backoff ceiling" --type bug --priority 1 --label auth --backlog --criterion "login retries honor the backoff ceiling"
3f9a1c2	open	P1	-	Login retries ignore the backoff ceiling
```

**Why the API client retries the way it does.** `cc-notes note`. It is a decision, not a unit of work — a fact about the codebase future readers need. Anchor it to the file with `--path` so the note is born verified against current content and drift is computed for you later.

```console
$ cc-notes note add "Retry backoff caps at 30s" --path internal/api/client.go --tag design --body "The server drops connections past 30s, so exponential backoff is clamped. Do not raise the ceiling without checking the gateway timeout."
b71e0d4	2026-06-16	design	Retry backoff caps at 30s
```

**Handing off a half-finished migration.** `cc-notes doc`. It is durable knowledge, not a unit of work, but it is paragraphs of guidance written *for whoever resumes the cutover* — too long for a note's one line. Store it as a doc with a `--when` trigger so the next agent surfaces it exactly when they pick the work back up, and anchor it to the directory so drift is computed against the real code.

```console
$ printf 'The new token endpoint is live behind a feature flag; the legacy handler still serves. Resume by flipping the flag in config and deleting the legacy handler. Do not touch the refresh path until the gateway timeout is confirmed.\n' | cc-notes doc add "Auth migration handoff" --when "resuming the auth cutover" --dir internal/api --tag handoff --body -
5c7d279	2026-06-23	handoff	Auth migration handoff	resuming the auth cutover
```

**Recording a production incident as it unfolds.** `cc-notes log`. The value is the chronology, not a single fact: a timeline of timestamped, authored entries that you keep appending as the incident develops, and that nobody ever rewrites afterward. A note would flatten the sequence into one line; a doc would invite editing the body as the situation changed, but an incident record must stay exactly as it was written. Create the log, anchor it to the affected code, then append each entry as you learn more.

```console
$ cc-notes log add "Checkout 500s incident 2026-06-23" --dir internal/checkout --tag incident
9f2c0e1	2026-06-23	incident	Checkout 500s incident 2026-06-23
$ cc-notes log append 9f2c0e1 "16:02 — error rate spiked to 12% after the pricing deploy"
9f2c0e1	2026-06-23	incident	Checkout 500s incident 2026-06-23
$ cc-notes log append 9f2c0e1 "16:31 — rolled back the deploy; error rate back to baseline"
9f2c0e1	2026-06-23	incident	Checkout 500s incident 2026-06-23
```

Each entry carries the author and timestamp of the commit that wrote it, and `log show 9f2c0e1` replays them in order. There is no `verify`, `supersede`, or `expire` — the record is the history, and history does not drift.

**Decomposing that bug fix once you pick it up.** Both. The bug is a durable task in the backlog. `task start` atomically claims it (deterministic first-wins), moves it onto your current branch, and opens a lease:

```console
$ cc-notes task start 3f9a1c2
3f9a1c2	in_progress	P1	ada <ada@example.com>	Login retries ignore the backoff ceiling
```

Now `TaskCreate` the in-session steps (reproduce, patch, test) as private todos. When the fix lands, `cc-notes task done 3f9a1c2` closes the durable record and anchors your HEAD commit onto it; the native todos vanish with the session.

**A multi-agent feature with dependencies.** `cc-notes task --backlog`, with `dep` for ordering. The tasks are durable and shared, so several agents draw ready work from one backlog; the dependency keeps the schema migration ahead of the code that reads it.

```console
$ cc-notes task add "Migrate sessions table" --label db --backlog --no-validation-criteria
9c4e2a1	open	P2	-	Migrate sessions table
$ cc-notes task add "Read sessions from the new schema" --label api --backlog --no-validation-criteria
e0b8f73	open	P2	-	Read sessions from the new schema
$ cc-notes task dep e0b8f73 9c4e2a1
e0b8f73	open	P2	-	Read sessions from the new schema
```

`cc-notes task ready --backlog` now surfaces the migration but hides the dependent task until the migration closes. The agent that grabs ready work with `task start 9c4e2a1` takes the migration onto its branch; the reader stays parked in the backlog until the blocker is done. No agent can start the reader early.

## Anti-patterns

- **Native todos for cross-session work.** The session ends and the work is lost. If it must survive, it is a `cc-notes task`.
- **A cc-notes note for an action item.** Notes are facts, not a queue — they have no claim, lease, status, or ready-list. Track work as a `cc-notes task`.
- **A cc-notes task for in-session steps.** A durable, synced task for "edit this file next" clutters the shared view with one agent's transient scaffolding. Keep those in native todos.
- **Shared work filed on your branch instead of the backlog.** A plain `task add` lands the task on your current branch, where it stays out of other agents' default view until you `task move` or merge. If anyone could pick it up, use `--backlog` so it is visible to every agent from the start.
- **A cc-notes note no one can place.** An unanchored note about a specific file rots silently. Anchor decisions to a `--path` or `--commit` so the note is born verified and drift is computed against the real code.
- **Run evidence committed to the repo tree.** Copying VM or CI run output — scenario logs, panic dumps, repro archives — under `docs/` or an `assets/` directory bakes megabytes of one-shot evidence into git history that every clone downloads forever. The chronology is a `cc-notes log`; each run's files ride `log append --attach`, stored in git-lfs and carried by `cc-notes sync`. Repo files are for the human-facing report, not the evidence behind it.
- **A loose `HANDOFF.md` for the next agent.** Nothing surfaces a loose markdown file — the next agent never opens it, it drifts unchecked, and it clutters the human-facing tree. Store the same guidance as a `cc-notes doc` with a `--when` trigger: born verified, drift-checked, and floated into the next agent's context the moment the trigger matches.
