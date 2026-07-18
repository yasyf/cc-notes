# Investigations

An investigation is the durable record of one debugging arc: something looked broken, theories
were tested, and the record closed on a verdict. The primitive exists because the arc used to be
crammed into a note — a "suspected X" body wholesale-edited into "RESOLVED: actually Y", the
original theory destroyed, the status shouted in the title, the evidence cited but never
attached. An investigation makes each of those moves structural instead.

## The anatomy

Four parts, each with its own mutation rule:

- **Premise** — the falsifiable suspicion the record opened on ("hangs began after 3d55ae2e;
  suspect the pool rewrite"). Set once at `open`, immutable forever. A premise that turns out
  wrong is not an embarrassment to erase; it is the arc's starting point, and `exonerate`
  closing the record against it is a first-class verdict.
- **Timeline** — append-only evidence entries, timestamped and authored, with git-lfs
  attachments for the artifacts that would otherwise be cited-by-id and later pruned (goroutine
  dumps, CI logs, repro archives). Transition verbs write their evidence here too, in the same
  commit as the status flip, so the chronology reads as one interleaved story.
- **Findings** — the hypotheses under test, each with a disposition: `open` (still suspect),
  `cleared` (ruled out), or `confirmed` (this was it). A disposition requires `--why` — the
  evidence travels with the ruling. One list serves both flavors of record: a debugging
  investigation's suspects, and a review pass's N findings with N dispositions.
- **Status** — the verdict, typed: `open → root_caused → fixed → confirmed`, with two other
  terminals, `exonerated` (the premise was falsified) and `abandoned` (walked away, no
  verdict). The status column travels with every list, search, and relevant line, which is why
  a title never needs to say RESOLVED — and the CLI warns when one tries.

## The status machine

`root-cause` records the mechanism and moves to `root_caused`. `fix` records at least one
fixing commit and moves to `fixed`. `confirm` records the proof — CI green since, no
recurrence — and closes the arc. `exonerate` falsifies the premise from `open` or
`root_caused`; `abandon` closes without a verdict from any non-terminal state; `reopen`
returns any terminal record to `open` with the regression as its required reason. The
pre-terminal edges also walk backward (`fixed → root_caused`, `fixed → open`, `root_caused →
open`) for the mid-flight reversal — the defining feature of real investigations, where the
first confident theory gets unwound by a bisect.

Legality is enforced when the verb builds its ops, best-effort against the snapshot it loaded.
Two agents racing transitions on different machines converge through the CRDT fold — the
last-write-wins status stands, and the loser's evidence entry survives in the timeline.

## Investigation vs log vs note

A **log** is a chronology with no verdict coming — a rollout log, a migration diary. If the
record opens on a suspicion and will close on a verdict, it is an investigation; "debugging
session" journals belong here, not in a log. A **note** is the distilled present-tense fact; an
investigation is the path that produced it. When a verdict yields a durable invariant ("the
unconditional send cannot deadlock because…"), graduate it into a note or doc — which carries
the verify/drift lifecycle an investigation deliberately lacks — and link it as a follow-up.
The investigation itself never drifts: a chronicle of what happened never claims to be current
truth.

Incidents and postmortems are investigations wearing labels. The premise is the incident
statement, the timeline is the incident chronology with dashboards attached, contributing
factors are findings, remediation commits land via `fix`, and `confirm` closes the loop when
the mitigation is verified. Label with `incident` and a severity and filter with
`investigation list --label incident`.

## The flow

Open the moment the suspicion is falsifiable — a red CI run under triage, a bug hunt, an
anomaly:

```console
$ cc-notes investigation open "TestPool deadlock on CI" "Hangs began after 3d55ae2e; suspect the pool rewrite." --finding "commit 3d55ae2e (pool rewrite)" --path internal/pool
a1b2c3d	open	TestPool deadlock on CI
```

Append evidence as each triage step lands — never retro-written at the end of the session:

```console
$ cc-notes investigation append a1b2 "Bisect: hang reproduces at 3d55ae2e~4 too." --attach /tmp/goroutine-stacks.txt
$ cc-notes investigation finding clear a1b2 f3a --why "bisect reproduces 4 commits earlier"
$ cc-notes investigation root-cause a1b2 "Unbuffered results chan + early return on ctx cancel leaks a blocked send."
$ cc-notes investigation fix a1b2 --commit 5e3c9ce4
$ cc-notes investigation confirm a1b2 "20 green CI runs on main since 5e3c9ce4; no recurrence."
```

A recurrence of the same cause reopens the same record (`reopen`, reason required). A new
suspicion — even an adjacent one — opens a new investigation citing the old one's id, rather
than accreting follow-up incidents into one growing body.

Multi-agent forensics maps onto one record: the orchestrator opens it, each evidence lane's
findings land as one `append` each, and the synthesis lands as `root-cause`/`fix`/`confirm`.
The durable record then exists the moment the session ends, instead of living only in the
transcript.

## Sharp edges

- The premise has no edit path. Typo'd premise? `rm` the record and reopen it properly — better
  a fresh id than a rewritable suspicion.
- `fix` resolves commit shas strictly against the local object database; fetch before recording
  a commit that only exists on the remote.
- An `open` matching a live non-terminal investigation (same title, premise, labels, anchors,
  attachments) returns the existing record with a warning instead of creating a duplicate; an
  `open` carrying `--finding` always roots fresh.
- `relevant` boosts a non-terminal investigation anchored to the path you are editing — "this
  code is under active investigation" is the single most useful thing the tool can tell you, so
  keep anchors current as the arc narrows.
