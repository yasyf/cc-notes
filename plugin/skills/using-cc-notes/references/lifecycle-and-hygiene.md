# Lifecycle and hygiene

cc-notes keeps its own logs honest. A note records when it was last confirmed true, so a fact that no longer matches the code surfaces instead of rotting unseen. A task carries a lease, so a crashed agent's claim becomes reclaimable instead of a permanent lock. No verdict is stored — the reader computes each one against a threshold at read time, so they stay deterministic across replicas. Three mechanisms keep the logs lean and trustworthy: note hygiene, task lifecycle, and the scale machinery under both.

## Note hygiene

A note is a claim about the code, and a claim decays. The verify/drift/supersede triad tracks that decay as first-class state, not convention.

### Verification and the witness

A note is born verified. When you `cc-notes note add` it against the current HEAD, each anchor records a *witness* — a snapshot of the anchored content at creation. The note asserts "this is true, and here is the proof I checked against."

```console
$ cc-notes note add "Auth tokens expire after 15 minutes" --path services/auth/login.go --label design
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

Time passes and the code moves. Once you reconfirm the note holds, refresh its witness against the current content of its anchors:

```console
$ cc-notes note verify ebba9fb
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

`note verify` records who checked and when, and re-snapshots the witness — so the next drift check compares against the content as of this verification, not the original creation.

### Drift detection

Drift is the gap between a note's witness and the current state of its anchors. When an anchored path or commit has changed since the note was last verified, the note has drifted: the fact may still be true, but nobody has confirmed it against the code as it stands now.

`cc-notes note review` computes drift by comparing each anchor's witness to live content:

```console
$ cc-notes note review --drift
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes	DRIFTED
```

The lean line gains a trailing verdict. `--drift` restricts the listing to drifted notes; without it, review surfaces every note needing attention.

### Supersession

When a decision is replaced instead of re-confirmed, record the replacement as a real edge:

```console
$ cc-notes note add "Auth tokens expire after 30 minutes" --path services/auth/login.go --label design
7a3f10c	2026-06-16	design	Auth tokens expire after 30 minutes
$ cc-notes note supersede ebba9fb --by 7a3f10c
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

The old note drops out of default listings and points at its replacement; the new note stands in for it. History is preserved — `cc-notes note show ebba9fb` still renders the old note with a `superseded_by` field, and `cc-notes note list --include-superseded` brings it back. Undo the edge with `cc-notes note supersede ebba9fb --by 7a3f10c --clear`.

Supersession is a structural edge on the note itself — nothing you maintain by hand marks a note "stale" or "superseded". The replacement is recorded once, and the default reader honors it.

### Expiration

Drift is computed; expiration is asserted. When you know a note is out-of-date but have no replacement to supersede it with, flag it by hand:

```console
$ cc-notes note expire ebba9fb --reason "tokens now live 30 minutes"
ebba9fb	2026-06-16	design	Auth tokens expire after 15 minutes
```

The note surfaces in `cc-notes note review` as `EXPIRED`, which takes precedence over every computed verdict. It stays in `cc-notes note list` — expiration is a warning flag, not a tombstone. Clear it by reconfirming the note with `cc-notes note verify`, or drop the flag alone with `cc-notes note expire ebba9fb --clear`.

### Review verdicts

`cc-notes note review` surfaces every kind of note decay. Each flagged note carries exactly one verdict, with precedence `EXPIRED > UNVERIFIED > DRIFTED > STALE`:

| Verdict | Meaning |
|---------|---------|
| `EXPIRED` | An agent flagged it out-of-date by hand with `note expire` |
| `UNVERIFIED` | Never verified since creation |
| `DRIFTED` | An anchored path or commit changed since the note was last verified |
| `STALE` | Verified, but longer ago than the staleness threshold |
| `DANGLING` | A supersede edge points at a note that has since been tombstoned |

| Flag | Default | Meaning |
|------|---------|---------|
| `--stale-after <dur>` | 90d | Age past which a verified note counts as `STALE` |
| `--drift` | off | Restrict to `DRIFTED` notes |
| `--unverified` | off | Restrict to `UNVERIFIED` notes |
| `--json` | off | Emit JSON |

The staleness threshold also comes from `cc-notes.noteStaleAfter` in git config or the `CC_NOTES_NOTE_STALE_AFTER` environment variable, defaulting to 90 days. A `DANGLING` verdict means a broken chain would otherwise silently hide a fact; the fix is to re-point or remove the edge.

The everyday loop: run `cc-notes note review` periodically, `cc-notes note verify` the notes that still hold, and `cc-notes note supersede` the ones a newer decision replaced.

## Task lifecycle hygiene

A task claim is a lease, not a permanent assignment. The lease keeps a crashed agent's grab from locking work forever, and lets a healthy holder keep its claim without ceremony.

### The lease and heartbeat model

`cc-notes task start` (or `task claim`) opens a lease on the task for the claiming actor. The lease's heartbeat is the `AuthorTime` of the latest operation by the assignee — so any write to the task by its holder refreshes the lease: an edit, a comment, a status change. A working agent that touches its task keeps the lease alive; only a silent holder lets it lapse.

For long stretches with no other write, refresh the heartbeat explicitly:

```console
$ cc-notes task renew d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
```

### Detecting and reclaiming stale leases

A lease is stale when its heartbeat is older than the TTL threshold. `cc-notes task stale` lists in-progress tasks whose lease has expired — the abandoned claims of crashed or walked-away agents:

```console
$ cc-notes task stale
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client	idle 2h13m
```

The lean line gains a trailing `idle <duration>` marker. `--idle-after <dur>` overrides the threshold for this listing.

Reclaim a stale task with `--steal`:

```console
$ cc-notes task claim d82c087 --steal
d82c087	in_progress	P1	ben <ben@example.com>	Add retry backoff to the API client
```

`--steal` only succeeds against a lease that has actually expired. A holder who renewed in time keeps the task — the fold's claim rule resolves the steal deterministically, so every replica agrees on the winner. A bare `cc-notes task claim` (no `--steal`) refuses to take an in-progress task at all; stealing is the explicit, deliberate act of taking work from a stalled agent.

### TTL configuration

The staleness threshold comes from `cc-notes.leaseTTL` in git config or the `CC_NOTES_LEASE_TTL` environment variable, defaulting to one hour.

```console
$ git config cc-notes.leaseTTL 2h
```

The TTL must exceed your sync interval. Staleness is judged against a holder's last *synced* heartbeat. When the TTL is shorter than how often agents sync, a healthy holder behind a slow sync looks stale and gets its work stolen out from under it. Pin the value per-repo in git config so every agent agrees on one threshold instead of each carrying its own environment default.

### Archiving long-closed work

Done and cancelled tasks are settled history, but they accumulate. `cc-notes task archived` lists closed tasks older than the cutoff (30 days by default):

```console
$ cc-notes task archived
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

Archived tasks stay out of default listings and out of `--all` views — they reappear only when you pass `--include-archived` to those commands, or list them directly with `task archived`. `--closed-before <when>` sets the cutoff, taking a Go duration (relative) or an RFC3339 timestamp (absolute). Nothing is deleted; archiving is a reader filter, so the records remain in the log and in history.

### Staleness is computed, never stored

The fold never writes a "stale" flag. The lease heartbeat is plain data — a timestamp on the latest op — and the reader compares it to the TTL at read time. That is what keeps the verdict deterministic: two replicas with the same log and the same threshold reach the same conclusion, regardless of when each runs the query. Storing staleness would make it depend on *when* it was written; computing it makes it depend only on the log.

## Scale and maintenance

Event logs grow without bound — the cost of never clobbering a write. Three mechanisms keep reads fast and the namespace lean, all invisible to day-to-day use.

### The local fold cache

Folding an entity means replaying its whole op-log into a snapshot. To skip re-folding from scratch on every read, cc-notes caches folded snapshots under the git common dir at `cc-notes/folds/` (so `.git/cc-notes/folds/` in a plain repo, shared across linked worktrees). The cache is local and never synced — a pure read accelerator derived entirely from the ODB, so a missing or stale cache is always safe to discard and rebuild. It carries no authority; the log is the truth.

`cc-notes gc` (no flags) tidies this cache: it drops entries whose tip is no longer the current tip of any entity ref — the orphans left by appends, compaction, and merges. It touches no remote.

```console
$ cc-notes gc
tidied: 7
```

### Op-log compaction

As an entity accumulates operations, its log gets long. `cc-notes compact <id>` collapses an entity's op-log into a single checkpoint, so future folds seed from the checkpoint instead of replaying every op:

```console
$ cc-notes compact d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
```

Compaction preserves the entity's id and its full folded state — a re-encoding of the same history, not a loss of it. The old objects stay in the ODB; compaction changes how the log is read, not what it means.

### Logical GC vs physical prune

There are two distinct notions of garbage collection, and they are not the same operation.

Logical GC is free and always on: it is the default reader filters. Reconciled, superseded, archived, and tombstoned entities drop out of normal listings without being removed from the ODB. Nothing is destroyed — the entity is hidden by the reader and recoverable through `--all`, `--include-superseded`, or `--include-archived`. This is the GC you want by default.

Physical prune is the opt-in, best-effort exception. `cc-notes gc --prune-remote` deletes tombstoned note refs locally and on the remote outright, via `git push --delete`:

```console
$ cc-notes gc --prune-remote
pruned: 2
tidied: 3
```

Prune is non-convergent, which is why it is opt-in and never part of normal sync. Sync converges by union-merging — it can only ever add. A delete is the one operation that does not converge: a stale clone that never saw the prune still holds the ref and re-advertises it on its next push, resurrecting what you deleted. Physical prune reclaims space only after every replica has already dropped the ref, so reach for it deliberately, not as routine hygiene. The logical filters give you a lean namespace without the convergence hazard.

### Why none of it is stored

The through-line across hygiene and scale: every staleness and drift verdict is computed by the reader against a threshold, never written into the log. Drift compares a witness to live content; lease staleness compares a heartbeat to the TTL; archival compares a close time to a cutoff. Each is a function of the log plus a threshold, evaluated at read time — so the same log yields the same verdict on every replica, and changing a threshold never rewrites history. The fold stays a pure, deterministic replay; judgment lives in the reader.
