# cc-notes memory evaluation — question set design

112 questions over real cc-notes history. The original 52 were authored 2026-07-26 against the live corpus; 60 more were authored 2026-07-29 against the **frozen snapshot** described below, after a power analysis put the 80 %-power MDE at n=35 (0.0615 NDCG) well above the effect the harness was being asked to resolve. Every gold entity id and every `must_not_retrieve` id resolves against that snapshot — `kgsnap -stamp` enforces it, and the stamp binds the question-file digest to the per-repo manifest digest, so either drifting voids the validation and `kgeval`/`kgrank` refuse to run. Nothing in this set is synthetic.

The set exists to let the proposed system fail. The literature pattern it is built against: specialized memory systems losing to naive baselines (full-context no-memory beating Mem0 on Mem0's own benchmark; Letta's plain filesystem agent beating Mem0 on LoCoMo; Zep's corrected 58.44%). So the set deliberately includes questions a dumb baseline answers, and scores the expensive categories — supersession, abstention, staleness — separately, so a headline average cannot hide a system that only wins where wins are cheap.

## The frozen corpus

The harness reads a `kgsnap` snapshot, never live `refs/cc-notes/*`: root `/Users/yasyf/.cc-notes/eval-snapshots/2026-07-29`, one directory per repo holding `corpus.jsonl` / `nodes.jsonl` / `edges.jsonl` / `events.jsonl` / `staleness.jsonl`, a `manifest.json` (captured_at, repo HEAD, graph source digest, pinned staleness policy, per-part sha256s) and the `validated.json` stamp. Frozen counts: monorepo 146, cc-context 166, cc-notes 139, cc-skills 130, cc-transcript 76, daemonkit 99 — 756 entities. The 2026-07-26 live enumeration (94-entity monorepo, 792 fleet-wide) is retired; two of the drift defects it carried were records written after authoring entering every candidate pool ungraded, and q032's `must_not` ids vanishing from the live corpus entirely.

Re-adjudications made against the freeze: q018 gained the Phase 7/8 tasks (and Phase 4's cancellation); q027's gold moved to the v3 synckit diagnosis (the prior "corrected" note is itself overturned, prose-only supersession); q032's `must_not` pair was relabeled to the surviving cc-guides v3 duplicate pair; q041 and q042 gained newly-written trap records (the operator sheet, the Phase 2 handoff); q051's gold answer records that the VCR implementation landed under `scripts/vcr` while `tools/vcr` still exists on no branch. Statuses cited in gold answers were re-checked via `cc-notes show` at freeze time.

## Distribution

| Category | n | What it scores |
|---|---|---|
| factual | 30 | Single-record lookup. The vanilla-RAG-is-competitive control group. |
| temporal | 25 | Supersession — formal edges, prose-only supersession (no edge), in-record reversals, and falsified diagnoses. Gold is the current truth; returning a `must_not_retrieve` record as current is a distinct, scoreable failure. |
| multihop | 25 | Two+ records joined by initiative, commit, project, phase, or wiki-link. |
| cross_branch | 13 | Facts anchored on one branch needed while working another (refs/cc-notes/* is one flat namespace). Includes the anti-over-suppression counter-test (q052). |
| abstention | 12 | Plausible questions the corpus provably cannot answer. `expect_abstain: true`; each carries its magnetic wrong-answer records in `must_not_retrieve`. |
| staleness | 7 | Anchor drift (renamed crate dirs), pointers declared ephemeral or recovered-from-deletion, real MarkStale events with redirect-bearing reasons. |

Multihop and cross_branch were grown hardest — 16 questions to 38 combined — because that is where the graph lane's effect had concentrated, and 38 is enough n to resolve those categories on their own rather than only in aggregate. Graded (non-abstention) totals: 100 across the fleet, 62 in the monorepo slice alone.

Axes (MemBench, arXiv:2506.21605): effectiveness 98, efficiency 7, capacity 7. The efficiency rows are needle-in-a-log or successor-artifact questions whose scoring should include tokens-retrieved; the capacity rows require enumeration across 8+ records or collapse of real duplicate pairs.

## Session context

Every question carries a `session` field mirroring `rank.Session` — the branch the asking agent stands on and the paths it plausibly has open:

```json
"session": {"branch": "dev", "paths": ["pkl/mod/buildkite/Plugins.pkl"]}
```

The eval previously measured `Session{}`, a configuration the product never runs (`internal/cli` seeds a branch and paths on every real query); this field closes that gap. `kgrank` threads it into `rank.Options.Session` for its `+session` arms and reports the count in each per-repository header (`11 of 11 questions carry session context; the rest are asked from the zero session, where the session-seeded arm scores identically to session-free`). `rank.Session.Entities` is deliberately never populated — pre-seeding the gold's own id would be the one unambiguous giveaway.

**The sessions overlap the gold anchors, and the overlap is large.** They were authored from the asking situation each query narrates rather than read off the gold record, but the result is not clean. Measured against the snapshot's `anchor` edges over the 100 graded questions:

| Overlap between `session.paths` and the gold entity's anchors | Questions | Placebo | Ratio |
|---|---|---|---|
| exactly one of the gold's **file** anchors | 37 | 0.8 % | ~45x |
| exactly one of its file **or directory** anchors | 50 | 1.4 % | ~35x |
| inside a gold-anchored directory, or containing one | 57 | 4.3 % | ~13x |

The placebo column is the same test run against a *different* question's gold in the same repository, over all such pairs. Some overlap is forced — an agent editing `hermetic_tool.bzl` and asking about the toolchain rule is exactly the seeding the product gets — but not 45x worth. No category escapes it: on the widest definition it is multihop 18/25, factual 16/30, temporal 13/25, cross_branch 7/13, staleness 3/7. Five graded questions carry empty `paths` because the situation puts no file in the asker's hand, and those are the only ones structurally immune.

The consequence for anyone reading harness output: a `+session` arm's advantage on this set is partly the session pointing at the answer, so the session-free arms are the uncontaminated reading and a session-seeded win is an upper bound. Closing this properly means re-deriving the sessions from something other than the record — the transcript that produced it, or a held-out situation — not editing the paths until the number falls.

## Field semantics

- `gold_entity_ids` — records whose content supports the gold answer.
- `must_not_retrieve` — on temporal/staleness rows: superseded, overturned, or stale records; presenting their content as current is the failure. On abstention rows: the trap records whose content, presented as an answer, constitutes injection (retrieving them to prove absence is fine). q049 intentionally lists the same ids in both fields: gold only as the source of the `stale_reason` redirect, a failure if served as live guidance.
- Six temporal questions carry no `must_not_retrieve` at all — q025, q066, q070, q071, q083, q101 — because the supersession happened inside one entity's fold history: the head text asserts what a later appended decision reverses, and there is no second record to withhold. Scorers must check answer polarity, not retrieved ids.
- Prose-only supersession (q027, q053, q109 among others) is deliberate hard material: the corpus holds no `supersede` edge, so edge-following alone cannot save a recency-blind retriever.
- `session` — see above. The harness treats a question with neither branch nor paths as asked from the zero session, where the two arms score identically.

## Authoring: the anti-leakage discipline

The trap in growing a set like this is that a question written by reading a record reuses the record's vocabulary and hands BM25 the answer. The 2026-07-29 questions were authored situation-first: each query voices the moment a developer would actually ask — the symptom observed, the wrong story circulating, the task about to be attempted — in that person's words, not the record's. That part held: mean Jaccard overlap between the query's content tokens and its gold titles' is 0.088 on the 55 new graded questions against 0.136 across all 100, so the new questions are lexically *further* from their gold than the originals. Several queries deliberately repeat a **wrong** circulating diagnosis — q084 ("the story going around blames its gitignore handling"), q085 ("a post-mortem concluded the built-in Edit tool corrupts trailing whitespace"), q108 ("was that daemons and clients on skewed library pins") — so lexical affinity actively pulls toward the falsified framing. The discipline that did *not* hold is the session one; see the table above.

## What a plain BM25 baseline should already answer — the honest paragraph

Roughly a third of this set is winnable by BM25 over flattened current-state records, and that is by design: the 30 factual questions are the control group, and a proposed system that only matches BM25 there has justified nothing. Many multihop questions are softer than they look — joined records share initiative vocabulary, so top-k BM25 with k≥5 often retrieves all hops; the ones that stress structure are the commit-sha join (q017), the enumerations (q018), and the joins whose halves share almost nothing lexically (q015, q097, q106). Cross_branch questions are trivially won by any baseline that ignores branches — which is the point: they measure whether a system that adds branch scoping leaks the right facts across it, and q052 punishes blanket suppression. Where BM25 should lose and the proposal must earn its keep: the 25 temporal questions are adversarial to lexical ranking (the superseded record is systematically longer, more assertive, "FINAL"-titled, duplicated, or higher-authority-looking); the 12 abstention questions punish any retrieve-then-answer pipeline without an abstain action; the 7 staleness questions require reading flags, reasons, and anchor drift, not bodies. If the system cannot separate itself on temporal + abstention + staleness + the structural multihop/cross_branch core, the memory layer is not worth shipping.

## Scoring protocol

Score each question on: (a) answer correctness against `gold_answer` (LLM-judge with the gold as reference); (b) retrieval precision — any id in `must_not_retrieve` whose content shapes the answer is a hard failure, scored distinctly from mere wrongness; (c) abstention — on `expect_abstain` rows, any substantive answer is a false-positive injection; (d) on efficiency-tagged rows, tokens retrieved. Report per-category, never a single average: the temporal/abstention/staleness block is the ship/no-ship signal, and multihop + cross_branch (38 questions) now carry enough n to resolve the graph lane's effect on their own. Run every baseline (BM25, full-context dump, plain filesystem agent) on the identical protocol, sessions included.

Two properties of `kgrank`'s own output belong with the set rather than in it. Its per-question paired tests separate *treated* from *untreated* questions, because the graph lane is appended only when the personalized walk resolves seeds — counting a question the lane never ran on as a tie reads as "the lane was tried and changed nothing", which is a different claim. And its weight sweep selects on one fold of the questions and scores on the other (`eval.Split`, hashed on question id), because a weight swept and scored on the same set re-confirms its own choice; a repository too small to fill both folds gets a refusal instead of a headline. The sweep includes `w=0`, so the pooled held-out row is a policy estimate rather than the graph lane's effect — `tune.go` says so in full.

## Files

- `questions.json` — the machine-readable set, next to this file (version 1, 112 questions, session fields included).
- `/Users/yasyf/.cc-notes/eval-snapshots/2026-07-29/` — the frozen corpus the labels were validated against; `kgsnap -stamp -questions <this dir>/questions.json -snapshot <that dir>` re-validates and re-stamps after any edit to the question file.
- `internal/kg/rank/cmd/kgrank` — the ablation, sweep, paired tests, and cross-repository pool. `internal/kg/snapshot/cmd/kgsnap` captures and stamps; `internal/kg/eval/cmd/kgeval` runs the plain harness.
