---
name: papercut-triage
description: >-
  Triage a repo's cc-notes papercut journal by clustering its entries on root
  cause and refreshing a ledger that holds the triage state a papercut record has
  no field for, then routing each recurring cluster to a backlog task carrying the
  fix or to the exact doc file and line that should have carried the workaround.
  Covers the row shape and key scheme, the overlap rule for a papercut reporting
  two independent failures, the re-run rule that updates a cluster instead of
  duplicating it and never clobbers a disposition, and the partition check a
  correct run passes. Triggers when an agent is asked to triage, cluster, sweep,
  or act on papercuts; decides what recurring friction to fix next; marks a
  papercut resolved or stale; or asks which papercuts are already closed.
allowed-tools: Bash(cc-notes:*), Read
---

# Papercut Triage

A papercut record carries `log_id`, `index`, `ts`, `author`, `model` and `text`.
There is no state field, so no papercut can be marked triaged, resolved or stale
in place. The cost shows up as soon as a journal outgrows a handful of entries.
Nothing is ever closed, every sweep re-reads entries an earlier sweep already
dispatched, and the only way to record that one was fixed is to file another
papercut saying so.

A cc-notes ledger supplies that state. The ledger is the deliverable; the journal
stays append-only and is never mutated.

## Procedure

**1. Read the whole journal.** `papercut_list` with `limit: 0` returns every
entry, each clipped to a preview, which is not enough to cluster on root cause.
Read the full text:

```sh
for i in $(seq 0 <max-index>); do cc-notes papercut show <log-id> $i --json; done > /tmp/pc.jsonl
```

**2. Cluster on root cause, not wording.** Two entries share a cluster when the
same change would close both. A test runner that matches no packages and a
path-scoped test command that ignores its argument read nothing alike and are one
cause: a check that exits 0 having checked nothing.

A papercut belongs to more than one cluster only when its text reports two
independent failures — one complaint naming both a broken cleanliness check and
an unrelated submit abort is a member of both clusters. Everything else gets
exactly one, so the counts stay a partition of the journal and step 5 can test
them.

**3. Refresh the ledger.** Find it with `ledger_list` filtered to labels
`papercut` and `triage`. Create it once, with `ledger_add`, only when that
returns nothing:

```sh
cc-notes ledger add "papercut triage" --label papercut --label triage \
  --column cluster --column count --column indices --column newest \
  --column state --column root_cause --column disposition --column evidence
```

Write one row per cluster with `ledger_row_set`.

**4. Route every cluster at five or more members.** Where the fix is a code
change, open a backlog task carrying it: `task_add` with `backlog: true`, label
`papercut-triage`, and a body naming the concrete asks the papercuts already
made, each with its index. Where the papercut documents a workaround a doc should
have carried, name the exact file and line to change instead. Every disposition
is one of those two. Write the task id or the `file:line` into the row's
`disposition` and set `state` to `triaged`.

Clusters below the threshold stay `open` with an empty disposition. They are
still rows, because the count is what crosses the threshold later.

**5. Falsify the run before reporting it.** The rules are in § Self-check.

**6. Re-run.** The rules are in § Re-running.

## Row shape

| field | holds |
|---|---|
| `cluster` | the cluster's title in ordinary words |
| `count` | member count |
| `indices` | member indices, comma-separated, ascending |
| `newest` | `ts` of the newest member, as `YYYY-MM-DD` |
| `state` | `open`, `triaged`, `fixed` or `stale` |
| `root_cause` | one sentence: the thing that would close every member |
| `disposition` | `task <id>`, `doc <file>:<line>`, or empty |
| `evidence` | the `papercut show` call reading the clearest member |

Two kinds of row live in one ledger, distinguished by key prefix.
`cluster:<slug>` is a cluster. `entry:<index>` is a single papercut whose
disposition is its own, which is how one entry gets marked without a cluster
around it.

## Re-running

The row key is the cluster slug, so a second run updates a cluster already in the
ledger instead of appending it. Slugs drift between runs, though, and a run that
renames a cluster opens a second row for it. Before minting a new slug, read the
existing rows and compare index sets: a new cluster sharing half or more of its
indices with an existing row **is** that row, and takes its key. Mint a slug only
when nothing overlaps.

A refresh writes `count`, `indices` and `newest` and nothing else.
`ledger_row_set` leaves fields the call omits standing, so `state`, `disposition`
and `root_cause` survive untouched; `replace` drops them and discards the triage.
A refresh that finds a cluster's count unchanged still rewrites the row, which is
free.

## Self-check

A clustering run has no answer key, so it has to falsify itself. Two tests do
that, and both run before the ledger is reported.

**The clusters partition the journal.** Sum the member counts, subtract one for
each entry deliberately placed in two clusters under step 2's overlap rule, and
the result equals the journal's entry count. A shortfall means entries were
dropped — usually the unclusterable singletons, which are rows too, one member
each. An excess the overlap list does not account for means an entry was filed
twice on a hunch, not because its text reports two independent failures. Report
both numbers, not the verdict alone.

**Every row the previous ledger held is still accounted for.** Read the prior
rows before writing any. For each one, name where it went this run: the same key,
a key it merged into under § Re-running's index-set rule, or a state that retired
it. A row that stopped appearing is a clustering bug — the entries behind it did
not go anywhere.

A cluster this run found that the previous one missed is expected; the journal
grows. Report the new ones.

## Worked example: an entry that was already fixed

The shape recurs often enough to check for first. To illustrate: a papercut says
some skill's reference file documents a subsystem the repo has since migrated
away from. The doc was rewritten days after the complaint was filed, and a later
papercut records the re-read that proves it — grepping the skill's directory for
the old subsystem's names returns nothing.

So the first entry needed no action from the day after it was filed, and the
second exists only because there was nowhere to say so. In the ledger that is one
row, not a cluster:

```sh
cc-notes ledger row set <ledger-id> --key entry:<n> \
  --field state=stale \
  --field disposition="none - superseded by papercut idx <m>, which verified the fix"
```

Check for this before routing any cluster. An entry naming a file settles in one
read.
