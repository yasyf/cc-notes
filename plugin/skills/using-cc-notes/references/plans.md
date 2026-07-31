# Plans: approved plans recorded verbatim

A plan is the durable record of one approved approach: the text a human signed off on, held
word for word, with typed state tracking what happened to it. The primitive exists because the
approved plan used to evaporate the moment execution started — the text lived in a session
transcript or a `/tmp` file, the tasks that implemented it carried no pointer back, and the
next session could not tell an executed plan from an abandoned one. A plan makes each of those
failures structural instead.

Like every cc-notes entity, a plan is an event-log CRDT on a `refs/cc-notes/plans/*` ref —
synced by the same refspec, folded deterministically, invisible in checkouts.

## The anatomy

Four parts, each with its own mutation rule:

- **Body** — the plan verbatim: context, approach, pitfalls, verification. Markdown, held
  exactly as approved, never paraphrased. The body is last-writer-wins, so re-approving a
  revised draft overwrites it and the earlier text survives in `plan history` — which is how
  one record carries a plan through a review round instead of spawning a rival per round.
- **Status** — the lifecycle, typed: `draft → approved → executing → done | abandoned`, with
  `reopen` returning either terminal to `executing`. `approve` fires only from `draft`, `done`
  only from `executing`; `abandon` closes from any non-terminal status. `start` and `reopen` are
  one move into `executing` under two names, so either accepts `approved`, `done`, or
  `abandoned`. An illegal transition is a conflict (exit 4) naming the current and requested
  status.
  A plan has **no verify/drift lifecycle** — it is work-shaped, not fact-shaped, and the status
  machine is how it goes out of date, the same reason a task anchor carries no witness.
- **Tasks** — the work that implements the plan. Membership is an upward pointer on the task
  (`task add --plan`, `task edit --plan`/`--no-plan`), and `plan show` inverts those pointers
  into the roll-up; no downward list is stored, so concurrent task edits never fight over the
  plan record.
- **Outcome** — what executing the plan actually produced, distinct from the approach it
  proposed. Recorded with `--outcome` on `done` or `abandon` (in the same commit as the status
  flip) or revised later through `plan edit --outcome`.

## Plan or runbook or doc?

The split from a **runbook** is repetition: a runbook is a standing procedure you expect to
execute again and again, each execution a tracked run; a plan is one approach, executed once,
then closed on an outcome. A deploy checklist is a runbook; the migration you will perform
exactly once is a plan. The split from a **doc** is shape: a doc is living guidance you keep
fresh — verified, drifting, superseded on review; a plan is a work record whose staleness is its
status. When executing a plan yields a durable fact or standing guidance, graduate that into a
note or doc — the plan stays as the record of what was approved and what happened.

| | Doc | Runbook | Plan |
|---|---|---|---|
| Body | Prose guidance, kept fresh | Ordered steps, each with an optional command | The approved plan, verbatim |
| Lifecycle | Verified, drifts, superseded | `active` → `archived` | `draft → approved → executing → done`/`abandoned` |
| Execution | Not tracked | Every run recorded, step by step | Once, rolled up from the tasks pointing at it |
| Reach for it when | The next agent must *understand* something | An agent *performs* something, again | An approach was *approved* and its execution should be on the record |

## The flow

Record the plan at the moment it is approved — `--approved` skips the draft gate, and
`--body-file` carries the text without shell quoting (where the cc-notes capt-hook pack is
enabled, `ExitPlanMode` records the approved plan for you and revises it in place on every
later round):

```console
$ cc-notes plan add "Fix monorepo read performance" --body-file plan.md --approved --path internal/gitobj --label perf
8d2ed23	approved	Fix monorepo read performance
```

A plan revised and re-approved before work starts is still that plan. Edit the body in place:
one record stays in flight, and each earlier draft is still readable through
`cc-notes history`.

```console
$ cc-notes plan edit 8d2ed23 --body - < plan.md
8d2ed23	approved	Fix monorepo read performance
```

What mints a second plan is a change of work, not a change of text, and the title is the
signal. Same title, revised text: another draft of one approach, edited in. A changed title
means a session reused for different work, so that plan gets its own record and no edge joins
the two — they are unrelated, not successive drafts. The `ExitPlanMode` capture applies
exactly that rule, keying on the plan file and its first heading.

Point the implementing tasks at it, start executing, and close on the outcome:

```console
$ cc-notes task add "Memoize ancestor checks" --plan 8d2ed23 --criterion "status under 2.5s" --backlog
9a31718	open	P2	-	Memoize ancestor checks
$ cc-notes plan start 8d2ed23
8d2ed23	executing	Fix monorepo read performance
$ cc-notes plan done 8d2ed23 --outcome "status 19.8s -> 2.1s on the monorepo"
8d2ed23	done	Fix monorepo read performance
```

`plan show` reads the arc back whole — the header with the lifecycle stamps and the derived
`tasks:` roll-up, then the recorded text verbatim, then the outcome.

`plan supersede` covers the case neither of those does — a genuine replan once execution has
started, where the old plan's record of what was approved and how far it got has to stay
standing while a new plan takes over:

```console
$ cc-notes plan supersede 8d2ed23 --by 357f361
8d2ed23	done	Fix monorepo read performance
```

Supersession is an edge, never a status: the old plan keeps the lifecycle status it closed
with, points at its replacement, and drops out of `plan list` and `plan search` — `plan show`
still reads it back, and `--clear` undoes the edge.

## Where plans surface

- `cc-notes status` counts the in-flight set — `plans: 1 in flight` covers `draft`,
  `approved`, and `executing`.
- `cc-notes relevant <path>` ranks plans anchored near the path alongside notes, docs, logs,
  runbooks, and investigations — "this code has an approved plan in flight" — and the
  kind-agnostic `search` matches their titles, text, and outcomes.
- `cc-notes show <prefix>` and `cc-notes history <id>` resolve plans like every kind; history
  keeps every draft the body has held, each as a field delta on the commit that wrote it.
- The MCP server exposes the whole loop as tools: `plan_add`, `plan_list`, `plan_show`,
  `plan_edit`, `plan_approve`, `plan_start`, `plan_reopen`, `plan_done`, `plan_abandon`,
  `plan_comment`, `plan_supersede`, `plan_search`, `plan_rm`, plus `plan` on
  `task_add`/`task_edit`.
- `cc-notes viz` draws plans on the timeline with their lifecycle markers.

## Sharp edges

- The body is LWW, which is what makes a revision round cheap and a slip expensive: a
  deliberate re-approval and a careless `plan edit --body` both overwrite the approved text in
  the working snapshot, and `plan history` is the only way back — by hand.
- Born status is `draft` unless `--approved` says otherwise, and `done` is reachable only
  through `executing` — there is no shortcut from `approved` to `done`, because a plan that was
  never started was never executed.
- An add whose title, body, born status, labels, and anchors all match a live non-terminal plan
  returns the existing record with a warning instead of creating a twin — re-capturing the same
  approved text is idempotent.
- Superseded plans hide from `plan list` even under `--all`; keep the replacement's id from the
  supersede acknowledgement, or find the old record through `plan show` on its id or
  `cc-notes history`.

The full flag tables live in [cli-reference.md](cli-reference.md); the JSON shapes are under
"JSON plan shapes" there.
