# Sprints and projects: the optional planning layer

Tasks and notes are the whole product. Sprints and projects sit on top of tasks as an optional
planning layer — batch tasks into a time-boxed sprint or a long-lived project when you want that
structure, ignore it when you don't. The canonical loop (`task add`, `task start`, `task done`,
`note add`) is unchanged; a task with no sprint and no project behaves as it always has.

Two noun groups carry the layer: `cc-notes project` and `cc-notes sprint`. A third addition,
acceptance **criteria**, lives on the task and gates `task done`. All three ride the same
`refs/cc-notes/*` object store and sync with the same refspec — no new plumbing.

## The model

The hierarchy is Project > Sprint > Task, and every edge is optional. A project groups sprints
and tasks; a sprint groups tasks and may sit inside a project; a task may point at a sprint, at a
project, at both, or at neither.

- **Attachment is two independent upward pointers.** A task carries a `Sprint` pointer and a
  `Project` pointer, set independently — direct to a project with no sprint, to a sprint that
  belongs to a project, or to a sprint and a different project at once. A sprint carries its own
  `Project` pointer. Membership is always the child pointing up, never the parent holding a list.
- **Reverse indexes are derived, never stored.** "Tasks in a sprint," "sprints in a project," and
  "tasks in a project" are computed by scanning pointers at read time. Tasks in a project is the
  **union** of tasks pointed straight at it and tasks whose sprint belongs to it; a task counted
  both ways appears once.
- **Sprints and projects are repo-wide.** A task's `Branch` attribute scopes it to one line of
  work; a sprint or project has no branch and is visible to every agent on every branch. The
  pointers a task carries are repo-wide too: a task on `feature/x` can belong to a sprint another
  agent planned on `main`.
- **Flat refs, same sync.** A sprint lives at `refs/cc-notes/sprints/<id>` and a project at
  `refs/cc-notes/projects/<id>`, one ref per entity, like a note or a task. `cc-notes init`
  carries them in its refspec; `cc-notes sync` union-merges them. Ids are global 40-hex; every
  id-addressed command resolves by id prefix.

```
project  (active | completed | archived | cancelled)
  └─ sprint  (planned | active | completed | cancelled)   ──┐ Sprint.Project
       └─ task  ── Task.Sprint                              │
  └─ task  ────────────────────────── Task.Project ─────────┘
```

## Lean-line formats

| Entity | Fields (tab-separated) |
|--------|------------------------|
| Sprint | `<short7-id>` `<status>` `<title>` |
| Project | `<short7-id>` `<status>` `<title>` |

Short ids are the first 7 hex chars; `-` stands in for an empty field. Mutations echo the lean
line; `--json` on any command emits the full record with 40-hex ids, RFC3339 UTC timestamps,
`null` for unset optionals, and sorted set slices.

## Project commands

A project is a long-lived grouping of sprints and tasks. It is born `active`, and the three
closing verbs move it to `completed`, `archived`, or `cancelled` (each refuses once the project
has already left `active`). A project has no branch and no start/end date — it is a container, not
a schedule.

### `cc-notes project add TITLE`

Create a project. It starts `active`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--body <text>` | empty | Description; `-` reads stdin |
| `--label <label>` | none | Label; repeatable |
| `--json` | off | Emit JSON |

```console
$ cc-notes project add "Billing platform" --body "Invoicing, payments, dunning" --label infra
8743887	active	Billing platform
```

### `cc-notes project list`

List projects, every status by default.

| Flag | Default | Meaning |
|------|---------|---------|
| `--status <csv>` | all | Status filter, comma-separated (`active,completed,archived,cancelled`) |
| `--json` | off | Emit JSON |

```console
$ cc-notes project list
8743887	active	Billing platform
```

### `cc-notes project show ID`

Show one project: a fixed-order header block (id, title, status, labels, created, updated, closed,
commits), the description after a blank line, each comment as a `-- <author> <rfc3339>` block,
then the derived reverse indexes — `sprints` (sprints pointing at this project) and `tasks` (the
union of direct and via-sprint tasks), both as short ids.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes project show 8743887
id: 87438870a8db6fe34d101a1dfe38ae476206e9e2
title: Billing platform
status: active
labels: infra
created: 2026-06-17T04:19:13Z
updated: 2026-06-17T04:19:13Z
closed: -
commits: -

Invoicing, payments, dunning
sprints: 7016a10
tasks: 25a3e39,77bb68a
```

### `cc-notes project complete | archive | cancel ID`

Move a project out of `active`: `complete` marks it `completed`, `archive` marks it `archived`,
`cancel` marks it `cancelled`. Each refuses with a conflict if the project has already left
`active`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes project complete 8743887
8743887	completed	Billing platform
```

### `cc-notes project edit ID`

Edit fields without a status transition — rename, re-describe, relabel. At least one flag is
required. Status changes go through the verbs above, not here.

| Flag | Meaning |
|------|---------|
| `--title <text>` | New title |
| `--body <text>` | New description; `-` reads stdin |
| `--add-label` / `--rm-label <label>` | Add or remove a label; repeatable |
| `--json` | Emit JSON |

### `cc-notes project comment ID BODY`

Append a comment; `BODY` of `-` reads stdin.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### JSON project shape

`{"id":string,"title":string,"description":string,"status":string,"labels":[…],"commits":[sha,…],"comments":[{"author":string,"ts":rfc3339,"body":string}],"author":string,"created_at":rfc3339,"updated_at":rfc3339,"closed_at":rfc3339|null,"sprints":[id,…],"tasks":[id,…]}`.
`sprints` and `tasks` are the derived reverse indexes; `tasks` is the deduplicated union of direct
and via-sprint members.

## Sprint commands

A sprint is a time-boxed grouping of tasks, optionally inside a project. It is born `planned`, then
walks to `active`, then `completed` or `cancelled`. A sprint carries optional `start` and `end`
calendar dates and an optional `Project` pointer.

### `cc-notes sprint add TITLE`

Create a sprint. It starts `planned`. `--project` attaches it to a project up front; `--start` and
`--end` set the calendar window.

| Flag | Default | Meaning |
|------|---------|---------|
| `--body <text>` | empty | Description; `-` reads stdin |
| `--project <id>` | none | Parent project id prefix |
| `--label <label>` | none | Label; repeatable |
| `--start <YYYY-MM-DD>` | none | Start date (UTC midnight) |
| `--end <YYYY-MM-DD>` | none | End date (UTC midnight) |
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint add "Sprint 12" --project 8743887 --start 2026-06-15 --end 2026-06-29
7016a10	planned	Sprint 12
```

### `cc-notes sprint list`

List sprints, every status by default. Filter to one project with `--project`.

| Flag | Default | Meaning |
|------|---------|---------|
| `--project <id>` | none | Filter to a project id prefix |
| `--status <csv>` | all | Status filter, comma-separated (`planned,active,completed,cancelled`) |
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint list --project 8743887
7016a10	active	Sprint 12
```

### `cc-notes sprint show ID`

Show one sprint: a fixed-order header block (id, project, title, status, start_date, end_date,
labels, created, updated, started, closed, commits), the description after a blank line, each
comment as a `-- <author> <rfc3339>` block, then a `tasks` line — the derived reverse index of
tasks pointing at this sprint, as short ids.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint show 7016a10
id: 7016a10eca671870ea76ba3bd3a4480df290099a
project: 8743887
title: Sprint 12
status: active
start_date: 2026-06-15T00:00:00Z
end_date: 2026-06-29T00:00:00Z
labels: -
created: 2026-06-17T04:19:14Z
updated: 2026-06-17T04:19:27Z
started: 2026-06-17T04:19:27Z
closed: -
commits: -
tasks: 77bb68a
```

### `cc-notes sprint activate | complete | cancel ID`

Walk the sprint lifecycle: `activate` marks it `active`, `complete` marks it `completed`, `cancel`
marks it `cancelled`. All three are accepted from `planned` or `active`; once a sprint is
`completed` or `cancelled`, every verb refuses with a conflict.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

```console
$ cc-notes sprint activate 7016a10
7016a10	active	Sprint 12
```

### `cc-notes sprint edit ID`

Edit fields without a status transition. At least one flag is required. The membership and date
fields each pair a setter with a clearer; the setter and its `--no-*` partner are mutually
exclusive.

| Flag | Meaning |
|------|---------|
| `--title <text>` | New title |
| `--body <text>` | New description; `-` reads stdin |
| `--project <id>` | Set the parent project |
| `--no-project` | Clear the project |
| `--start <YYYY-MM-DD>` | Set the start date |
| `--no-start` | Clear the start date |
| `--end <YYYY-MM-DD>` | Set the end date |
| `--no-end` | Clear the end date |
| `--add-label` / `--rm-label <label>` | Add or remove a label; repeatable |
| `--json` | Emit JSON |

### `cc-notes sprint comment ID BODY`

Append a comment; `BODY` of `-` reads stdin.

| Flag | Default | Meaning |
|------|---------|---------|
| `--json` | off | Emit JSON |

### JSON sprint shape

`{"id":string,"project":string|null,"title":string,"description":string,"status":string,"start_date":rfc3339|null,"end_date":rfc3339|null,"labels":[…],"commits":[sha,…],"comments":[{"author":string,"ts":rfc3339,"body":string}],"author":string,"created_at":rfc3339,"updated_at":rfc3339,"started_at":rfc3339|null,"closed_at":rfc3339|null,"tasks":[id,…]}`.
`project` is the parent pointer (`null` when unset); `tasks` is the derived reverse index of member
tasks.

## Attaching a task to a sprint or project

Membership is set on the **task**, never on the parent. Two flags, each an id prefix, both optional
and independent:

- `--sprint <id>` points the task at a sprint.
- `--project <id>` points the task at a project directly.

Both are available at creation (`task add`) and afterward (`task edit`). On `task edit` each pairs
with a clearer (`--no-sprint`, `--no-project`); the setter and its `--no-*` partner are mutually
exclusive.

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1 \
    --sprint 7016a10 --criterion "Returns 200 on transient 503" --criterion "Backoff capped at 30s"
77bb68a	open	P1	-	Add retry backoff to the API client
$ cc-notes task edit 77bb68a --project 8743887     # also belongs to the project directly
77bb68a	open	P1	-	Add retry backoff to the API client
$ cc-notes task edit 77bb68a --no-sprint           # detach from the sprint, keep the project
77bb68a	open	P1	-	Add retry backoff to the API client
```

Membership is repo-wide, so it survives re-homing a task between branches (`task edit --branch`)
and is unaffected by the backlog. A task's `--branch`/`--backlog` scope and its `--sprint`/`--project` membership are
orthogonal axes: branch says *which line of work*, sprint and project say *which plan*.

The `taskDTO` gains `sprint`, `project`, and `criteria`, plus a derived `closed_forced` flag (see
below). The `task show --json` record for the task above:

```console
$ cc-notes task show 77bb68a --json
{"id":"77bb68a04b78ccdc2e25e3a7e67cd8a401ee184b","branch":"main","title":"Add retry backoff to the API client","description":"","type":"task","status":"open","priority":1,"assignee":null,"labels":[],"blocked_by":[],"blocks":[],"parent":null,"comments":[],"commits":[],"lease":{"holder":null,"heartbeat":null},"created_at":"2026-06-17T04:19:14Z","updated_at":"2026-06-17T04:19:45Z","started_at":null,"closed_at":null,"sprint":"7016a10eca671870ea76ba3bd3a4480df290099a","project":null,"criteria":[{"id":"3c9eddc30446ea8dc9c2b3477b90af30","text":"Returns 200 on transient 503","script":"","status":"met"},{"id":"4bb2000fc0c4d64bcdd6c0741338543b","text":"Backoff capped at 30s","script":"exit 0\n","status":"met"}],"closed_forced":false}
```

## Acceptance criteria gate `task done`

A criterion is a structured acceptance check on a task: a line of text, an optional validation
script, and a status of `pending`, `met`, or `failed`. Criteria are the one piece of this layer
that touches the canonical flow — `task done` is **gated**: it refuses to close a task while any
criterion is still `pending` or `failed`, and lists them.

`task add` requires at least one `--criterion` by default, so a new task ships with its acceptance
bar written down. The escape hatch is `--no-validation-criteria`, which creates a task with none;
it is mutually exclusive with `--criterion`.

```console
$ cc-notes task add "Rotate signing keys" --project 8743887 --backlog --no-validation-criteria
25a3e39	open	P2	-	Rotate signing keys
```

### `cc-notes task criterion …`

Manage a task's criteria. `CRIT` is a criterion id prefix (the leading hex of the criterion id,
matched case-insensitively), shown 7 chars wide by `criterion list`.

| Subcommand | Effect |
|------------|--------|
| `add TASK "TEXT" [--script FILE]` | Add a criterion; `--script` loads a validation script from a file |
| `rm TASK CRIT` | Remove a criterion |
| `met TASK CRIT` | Mark it `met` |
| `failed TASK CRIT` | Mark it `failed` |
| `pending TASK CRIT` | Mark it `pending` |
| `script TASK CRIT FILE` / `script TASK CRIT --clear` | Set or clear its validation script |
| `list TASK [--json]` | List the task's criteria (`<short7>` `<status>` `<text>`, tab-separated) |

```console
$ cc-notes task criterion list 77bb68a
3c9eddc	pending	Returns 200 on transient 503
4bb2000	pending	Backoff capped at 30s
```

### The done gate

With any criterion not `met`, `task done` refuses and exits non-zero, naming each blocker:

```console
$ cc-notes task done 77bb68a
usage: 77bb68a has 2 unmet criterion/criteria (pass --force to close anyway):
  3c9eddc [pending] Returns 200 on transient 503
  4bb2000 [pending] Backoff capped at 30s
```

Mark them met — by hand, or via `task validate` — and `done` goes through:

```console
$ cc-notes task criterion met 77bb68a 3c9eddc
77bb68a	open	P1	-	Add retry backoff to the API client
```

`task done --force` closes a task with unmet criteria anyway. The escape hatch leaves a visible
mark: a forced close sets the derived `closed_forced` flag in the JSON, so a reviewer can tell the
bar was skipped.

```console
$ cc-notes task done 53ba753 --force --json
{"id":"53ba7531e94ab3135e738647fffdb3f869f28513", … ,"status":"done","closed_at":"2026-06-17T04:20:04Z","sprint":null,"project":null,"criteria":[{"id":"389f00a849470d726f8abc1331847e54","text":"demo compiles","script":"","status":"pending"}],"closed_forced":true}
```

### JSON criterion shape

A task's `criteria` is an array of `{"id":string,"text":string,"script":string,"status":string}`,
with `status` one of `pending`, `met`, `failed` and `script` empty when none is attached. The `id`
is the full criterion nonce; commands take any unambiguous prefix.

## `cc-notes task validate` runs stored scripts — a trust boundary

A criterion can carry a validation **script**. `cc-notes task validate TASK` runs each scripted
criterion's script locally under `sh`, in the repository working tree, and records the verdict:
exit 0 marks the criterion `met`, a non-zero exit or a timeout marks it `failed`. Criteria with no
script are skipped.

This is the only place cc-notes executes stored content, and that content arrives over git sync
from other agents and remotes — so running it is a deliberate, **explicit-only** trust boundary,
never reachable from sync, list, fold, `done`, or render. Two guards bound it:

1. **Every script is printed first.** Before anything runs, each scripted criterion's text and
   script go to stderr, so you read exactly what is about to execute.
2. **Execution requires an opt-in.** Pass `--yes`, or answer the interactive `[y/N]` prompt on a
   terminal. On a non-terminal stdin without `--yes`, validate **refuses** — a piped or automated
   invocation can never run a script silently.

| Flag | Default | Meaning |
|------|---------|---------|
| `--yes` | off | Run without the interactive prompt |
| `--timeout <dur>` | `5m` | Per-script timeout; a script that overruns is `failed` |
| `--json` | off | Emit JSON |

```console
$ cc-notes task validate 77bb68a --yes
criterion 4bb2000 Backoff capped at 30s:
exit 0

4bb2000 met Backoff capped at 30s
77bb68a	open	P1	-	Add retry backoff to the API client
```

Without `--yes` and without a terminal, it stops before executing anything:

```console
$ true | cc-notes task validate 77bb68a
criterion 4bb2000 Backoff capped at 30s:
exit 0

error: refusing to run validation scripts without --yes (stdin is not a terminal)
```

Treat a script that arrived over sync as untrusted code. Read the printed scripts before you
confirm, and never wire `task validate --yes` into an unattended pipeline that pulls from a remote
you do not control.


## When to reach for a sprint or project

Default to a plain task. The grouping layer earns its place only when the group is something you
look at as a unit:

- **Just a task** — the unit of work. Claiming, leases, dependencies, the backlog, and `done` all
  work with no sprint and no project. Most work never needs more.
- **A sprint** — a time-boxed batch: a set of tasks with a start and end date that you plan, start,
  and complete together, and a `sprint show` roll-up of what is in it.
- **A project** — work spanning many sprints or many tasks over a long horizon, with one durable
  home. Attach sprints to it, or point tasks straight at it, and `project show` gives the union
  view.

Because attachment is just an upward pointer, you can add it late (point existing tasks at a sprint
you create today) or never. The planning layer bends to the work; the work does not wait on the
plan.
