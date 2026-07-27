# cc-notes memory evaluation — question set design

52 questions over real cc-notes history, authored 2026-07-26 against the live corpus: 792 entities across 11 repos (94 in monorepo — 343 fold ops, all written 2026-07-23..26 — plus cc-context 162, cc-notes 133, cc-skills 128, captain-hook 113, cc-transcript 76, daemonkit 73, and four small repos). Every gold entity id was resolved live via `cc-notes -R <repo> show <id> --json`; every supersession, MarkStale event, anchor drift, and branch fact cited was verified in the fold history or the git working tree, not invented. Nothing in this set is synthetic.

The set exists to let the proposed system fail. The literature pattern it is built against: specialized memory systems losing to naive baselines (full-context no-memory beating Mem0 on Mem0's own benchmark; Letta's plain filesystem agent beating Mem0 on LoCoMo; Zep's corrected 58.44%). So the set deliberately includes questions a dumb baseline answers, and scores the expensive categories — supersession, abstention, staleness — separately, so a headline average cannot hide a system that only wins where wins are cheap.

## Distribution

| Category | n | What it scores |
|---|---|---|
| factual | 13 | Single-record lookup. The vanilla-RAG-is-competitive control group. |
| temporal | 11 | Real supersession chains. Gold is the successor; returning the superseded record (`must_not_retrieve`) is a distinct, scoreable failure. |
| multihop | 9 | Two+ records joined by initiative, commit, project, or wiki-link. |
| cross_branch | 7 | Facts anchored on one branch needed while working another (refs/cc-notes/* is one flat namespace). Includes one anti-over-suppression counter-test (q052). |
| abstention | 7 | Plausible questions the corpus provably cannot answer. `expect_abstain: true`; each carries the magnetic wrong-answer record in `must_not_retrieve`. |
| staleness | 5 | Records whose anchor drifted (renamed crate dir), whose pointer is declared ephemeral, or which carry a real MarkStale with a redirect-bearing reason. |

Axes (MemBench, arXiv:2506.21605): effectiveness 44, efficiency 4, capacity 4. The imbalance is honest — a static Q/A set mostly probes effectiveness. The 4 efficiency questions are ones whose scoring should include tokens-retrieved (a needle in a 20-entry log, a runbook step, two one-paragraph operational facts): run them with a retrieval budget and report tokens-to-answer alongside accuracy. The 4 capacity questions require enumeration across 8+ records, collapse of real duplicate pairs (two `CLAUDE.src.md`-era notes; a four-note supersession fan-in), or an all-branches existence sweep. For a fuller efficiency/capacity read, additionally run the whole set at fleet scope (all 11 repos in context) vs repo scope and compare cost.

## Field semantics

- `gold_entity_ids` — records whose content supports the gold answer.
- `must_not_retrieve` — on temporal/staleness rows: superseded or stale records; presenting their content as current is the failure. On abstention rows: the trap records whose content, presented as an answer, constitutes injection (retrieving them to prove absence is fine). q046 (runbook specs) intentionally lists the same ids in both fields: they are gold only as the source of the `stale_reason` redirect, and a failure if served as live guidance.
- q019 (MockServer hermeticity) is a temporal question with no `must_not_retrieve`: the supersession happened inside one entity's fold history (the create-time body asserts the flag suffices; the current fold state says the opposite for CONNECT traffic). Scorers must check answer polarity, not retrieved ids.

## Real supersession material used

Monorepo: codex fast mode (`cebda6e576…` → `dfbf0424a9…`, both verified the same day, 7 minutes apart — the superseded note is longer and lexically richer for the query); sccache write model (`b13d12a410…` → `ff8693ad65…`, the superseded note reads like authoritative deployed infra). Fleet: semble → native engine (cc-context), synckit "root cause FINAL" → corrected two-failure analysis (daemonkit), subagent-wake gap → wake-works (cc-skills), codex-ask bash-era → v3 lifecycle doc (cc-skills), payload-lowering-open → landed (cc-transcript), jj-diff-bug → fixed-in-v0.6.1 (cc-context), and the four-into-one guides-pipeline fan-in (cc-context). MarkStale events: `adaab9813c…` (cc-context — whose title asserts the stale state and whose stale_reason carries the truth: a title-weighted retriever inverts the answer) and the two runbook phase specs (cc-notes). Anchor drift: `3c8594a924…` anchored to `rust/crates/sandsql_core`, renamed to hyphenated form in the D-stack; `tools/vcr` anchors that exist on no branch at all; `tools/pr-reviewer` missing on dev but live on `yasyf/pr-reviewer-ts`.

## What a plain BM25 baseline should already answer — the honest paragraph

Roughly half this set is winnable by BM25 over flattened current-state records, and that is by design. All 13 factual questions share heavy vocabulary with exactly one record ("stack smashing detected", "worktreeConfig", "attemptToProxyIfNoMatchingExpectation") — q001–q013 are the control group, and a proposed system that only matches BM25 there has justified nothing. Most multihop questions are also softer than they look: the joined records share an initiative vocabulary ("sccache", "escape-hatch", "E10"), so top-k BM25 with k≥5 likely retrieves all hops; only q016 (commit-sha → task, since 40-char shas are poor lexical keys), q017 (8-record enumeration where partial recall is silent), and q024/q025 (joins through wiki-link titles) genuinely stress structure. Cross-branch questions are trivially won by any baseline that ignores branches entirely — including full-context dump — which is precisely the point: they measure whether a system that adds branch scoping leaks the right facts across it, and q052 punishes the opposite reflex (withholding anchored records because the default branch lacks the path). Where BM25 should lose, and where the whole proposal must earn its keep, is the other half: all 11 temporal questions are adversarial to lexical ranking — the superseded record is systematically the better lexical match (it is longer, more assertive, "FINAL"-titled, or four copies deep), so BM25's expected score on temporal is near zero without supersession awareness; the 7 abstention questions punish any retrieve-then-answer pipeline that lacks an abstain action (the 17.5%-vs-0.0% injection finding is the reason this subset exists); and the 5 staleness questions require reading flags and reasons, not bodies. If the proposed system beats the baseline only on factual and multihop, it is overhead; if it cannot separate itself on temporal + abstention + staleness (23 of 52 questions), the memory system is not worth shipping.

## Scoring protocol

Score each question on: (a) answer correctness against `gold_answer` (LLM-judge with the gold as reference); (b) retrieval precision — any id in `must_not_retrieve` whose content shapes the answer is a hard failure, scored distinctly from mere wrongness; (c) abstention — on `expect_abstain` rows, any substantive answer is a false-positive injection; (d) on efficiency-tagged rows, tokens retrieved. Report per-category, never a single average: the temporal/abstention/staleness block is the ship/no-ship signal. Run every baseline (BM25, full-context dump, plain filesystem agent) on the identical protocol — the full-context dump baseline must be run, because on this corpus (~1–2 MB of records fleet-wide) it fits in a frontier context window and the literature says it will be embarrassingly strong.

## Files

- `/tmp/ccn-exec/P5/questions.json` — the machine-readable set (version 1, 52 questions).
- `/tmp/ccn-exec/P5/raw/` — the mined corpus snapshots backing every gold answer (per-repo `show_all.json`, monorepo `show/all.json` + `show/history.json`, `idmap.json` prefix→id resolution).
