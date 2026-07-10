# Runbooks: repeatable procedures with tracked runs

A runbook stores a procedure you expect to execute again — a release checklist, a deploy
sequence, an incident response — as ordered steps, and records every execution as a
first-class run: who ran it, when, and what happened to each step. Tasks and notes are
still the whole product; a runbook is the optional layer's answer to "we do this dance
every time, and nobody remembers whether step 4 happened."

Like every cc-notes entity, a runbook is an event-log CRDT on a `refs/cc-notes/runbooks/*`
ref — synced by the same refspec, folded deterministically, invisible in checkouts. Two
agents on different branches can run the same runbook concurrently; both runs survive the
merge.

## Runbook or doc?

The split is execution. A **doc** describes: it holds guidance you keep fresh, drifts when
the code moves, and carries a "read this when…" trigger. A **runbook** is executed: its
steps are the work itself, and the record of each run — per-step outcomes, runner,
timestamps — is the point. A rule of thumb: if the file you are about to write has
numbered steps you expect someone to perform again, it is a runbook; if it explains how
something works or what was decided, it is a doc. A runbook has no freshness lifecycle
(no verify, no drift, no supersede) — you edit its steps in place and retire it with
`runbook archive` when the procedure stops being real.

| | Doc | Runbook |
|---|---|---|
| Body | Prose guidance | Ordered steps, each with an optional command |
| Lifecycle | Verified, drifts, superseded | `active` → `archived` |
| Execution | Not tracked | Every run recorded, step by step |
| Reach for it when | The next agent must *understand* something | An agent must *perform* something, again |

## Authoring

Create the runbook with its first steps in one call — `--step` repeats and keeps flag
order:

```console
$ cc-notes runbook add "Deploy hotfix" --step "drain traffic" --step "deploy" --step "verify health"
2b808c6	active	Deploy hotfix
```

Attach a canonical command to a step, and place later steps precisely. Steps carry
positions, so an insert or a move leaves every other step where it was, and concurrent
edits elsewhere in the list merge cleanly:

```console
$ cc-notes runbook step add 2b808c6 "warm caches" --after 6af6dff --command "./scripts/warm.sh"
2b808c6	active	Deploy hotfix
$ cc-notes runbook step move 2b808c6 03c14b6 --last
2b808c6	active	Deploy hotfix
```

`step edit` rewrites a step's text or command (`--no-command` clears it); `step rm`
deletes one — past runs keep their recorded outcomes for it as history. `runbook show`
renders the numbered procedure plus the most recent runs; `step list` prints one line per
step with its short id, position index, text, and command.

## Executing

Start a run, record each step as you go, finish. Cite the task the run serves with
`--task` — a loose reference on the run, nothing changes on the task:

```console
$ cc-notes runbook run start 2b808c6 --task d82c087
2b808c6	active	Deploy hotfix
$ cc-notes runbook run done 2b808c6 6ec7607 --note "connections drained in 40s"
2b808c6	active	Deploy hotfix
$ cc-notes runbook run skip 2b808c6 f396114 --note "caches already warm"
2b808c6	active	Deploy hotfix
$ cc-notes runbook run finish 2b808c6
2b808c6	active	Deploy hotfix
```

The outcome vocabulary is `done`, `failed`, or `skipped`; a step with no recorded outcome
is pending — there is no reset verb, so correct a wrong mark by re-recording the right
one (that works even after `finish`, targeted with `--run`). `finish` closes the run
`succeeded` unless a step recorded `failed` (then `failed`), or force `--failed` /
`--abandoned` explicitly.

With `--run` omitted, the run-marking verbs target the sole running run. Zero running runs
is a conflict (exit 4); more than one is ambiguous (exit 5) — pass `--run <prefix>` to
pick. Concurrent runs are legitimate, not an error: two agents executing the same
procedure each hold their own run.

`run show` reads a run back step by step, in procedure order, with notes; `run list`
summarizes every run with a `<done+skipped>/<total>` progress field.

cc-notes never executes a step's command — the command is the procedure's record of what
to run, and the agent (or human) runs it through their own shell and judgment, then
records the outcome. That is deliberate: the only cc-notes surface that executes stored
content is `task validate`, behind its own confirmation gate.

## Where runbooks surface

- `cc-notes show <prefix>` and `cc-notes history <id>` resolve runbooks like every kind.
- The MCP server exposes the authoring-and-execution loop as tools: `runbook_add`,
  `runbook_list`, `runbook_show`, `runbook_step_add`, `runbook_run_start`,
  `runbook_run_done`, `runbook_run_skip`, `runbook_run_fail`, `runbook_run_finish`.
  Definition micro-editing (`step rm`/`edit`/`move`, `edit`, `archive`) stays on the CLI.
- On a mounted `.notes` tree, runbooks render read-only at
  `.notes/runbooks/<short-id>-<slug>.md` — readable procedure prose; edits go through the
  CLI.
- `cc-notes viz` draws runbooks on the timeline with run-start and run-finish markers.

The full flag tables live in [cli-reference.md](cli-reference.md); the JSON shapes are
under "JSON runbook shape" there.
