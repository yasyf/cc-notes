# /// script
# requires-python = ">=3.13"
# dependencies = ["capt-hook>=4.2.0", "pydantic>=2"]
# ///
"""Direct unit tests for the cc-notes capt-hook pack's pure helpers and handlers.

The pack is the relative-import package ``hooks`` (``common``/``session``/``surface``/
``record``/``memory``/``workflow``); each symbol is imported from the module it lives in
so the modules' own ``from .common import ...`` resolves.

The inline ``tests={...}`` on each hook prove environment-stable behavior (non-matching
tools Allow, and for the workflow side-effect handlers ONLY the Allow near-misses — a
firing inline test would shell out to a real ``cc-notes sync`` under ``capt-hook test``).
These tests cover what the inline harness cannot make deterministic: the pure render and
filter helpers, both gate branches (binary present opens it, binary absent fails it
closed), and every firing handler — including the auto-sync / auto-reconcile side-effects —
end to end with a stubbed CLI and git. They mock the gate's one true external,
``shutil.which`` (now called from ``hooks.common``), rather than the condition object, so
the real ``CcNotesAvailable`` logic runs under controlled inputs. A loader regression test
drives the production import path (``discover_pack`` over the real pack dir) so the
relative-import split can't silently break.

Run with the same toolchain the inline tests use::

    uv run plugin/hooks/tests/test_cc_notes.py
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

# The pack is the relative-import package ``hooks`` (this file's grandparent dir is
# ``plugin/``, which must be on sys.path so ``from .common import ...`` resolves when
# the modules import each other). parents[2] is ``plugin/``; parents[1] is ``hooks/``.
sys.path.insert(0, str(Path(__file__).parents[2]))

import hooks.common as common
from hooks.common import (
    cap_and_render_tasks,
    entry_payload,
    filter_drifted,
    in_cc_pool_memory,
    parse_relevant,
    parse_tasks,
    record_command,
    RecordVerdict,
    render_log_line,
    render_note_lines,
    render_task_line,
    run_cc_notes,
)
from hooks.memory import (
    clamp_title,
    MAX_TITLE_BYTES,
    MemoryWrite,
    mirror_memory_to_note,
    parse_memory_file,
)
from hooks.record import (
    durable_dest,
    DurableInternalWrite,
    ephemeral_record_refs,
    EphemeralRecordReference,
    EvidenceArchive,
    evidence_payload_bytes,
    evidence_transfers,
    in_git_worktree,
    nudge_ephemeral_record_reference,
    nudge_plan_tasks,
    nudge_record_durable,
    nudge_record_evidence,
    PlanTask,
    PlanTasks,
    plan_task_commands,
    plan_text,
    transfer_operands,
    tree_bytes,
)
from hooks.session import (
    float_session_tasks,
    prompt_install_cc_notes,
)
from hooks.surface import (
    check_note_staleness,
    float_note_context,
    surface_filter,
    SurfacePick,
)
from hooks.workflow import (
    auto_reconcile,
    auto_sync,
    commit_decision,
    do_sync,
    nudge_claim,
    nudge_commit_record,
    reconcile_after_merge,
)
from captain_hook.testing.helpers import mock_event
from captain_hook.types import Action, Event

FAILURES: list[str] = []


def check(name: str, cond: bool, detail: str = "") -> None:
    status = "PASS" if cond else "FAIL"
    print(f"  {status}  {name}{(': ' + detail) if detail and not cond else ''}")
    if not cond:
        FAILURES.append(name)


def note_entry(note_id: str, *, drift: str | None = None, title: str = "t", reasons: list[str] | None = None) -> dict:
    return {"note": {"id": note_id, "title": title, "drift": drift}, "score": 1, "reasons": ["path"] if reasons is None else reasons}


def doc_entry(
    doc_id: str,
    *,
    when: str = "",
    drift: str | None = None,
    title: str = "d",
    reasons: list[str] | None = None,
    body: str = "LONG_DOC_BODY",
) -> dict:
    """A `kind == "doc"` relevance entry: the doc DTO under "doc", no "note" key.

    Carries a ``body`` the render path must never surface — the float only ever
    emits the pointer (title/when/verdict/`doc show`), never the long body.
    """
    return {
        "kind": "doc",
        "doc": {"id": doc_id, "title": title, "when": when, "drift": drift, "body": body},
        "score": 1,
        "reasons": ["dir"] if reasons is None else reasons,
    }


def log_entry(
    log_id: str,
    *,
    title: str = "l",
    reasons: list[str] | None = None,
    entries: list[dict] | None = None,
) -> dict:
    """A `kind == "log"` relevance entry: the log DTO under "log", no "note"/"doc" key.

    A log is append-only and never drifts, so it carries no ``drift`` field. The
    ``entries`` chronology must never reach the float — the pointer renders only
    the title and a ``log show`` hint, never the entry text.
    """
    return {
        "kind": "log",
        "log": {"id": log_id, "title": title, "entries": [{"text": "LOG_ENTRY_TEXT"}] if entries is None else entries},
        "score": 1,
        "reasons": ["dir"] if reasons is None else reasons,
    }


def test_parse_relevant() -> None:
    check("parse_relevant: empty string -> []", parse_relevant("") == [])
    check("parse_relevant: None -> []", parse_relevant(None) == [])
    check("parse_relevant: malformed -> []", parse_relevant("{not json") == [])
    check("parse_relevant: non-array -> []", parse_relevant('{"a": 1}') == [])
    parsed = parse_relevant('[{"note": {"id": "abc", "title": "T", "drift": null}, "score": 100, "reasons": ["path"]}]')
    check("parse_relevant: array round-trips", parsed == [{"note": {"id": "abc", "title": "T", "drift": None}, "score": 100, "reasons": ["path"]}])
    # A JSON array whose elements are ill-shaped is "malformed" from the contract's view:
    # every survivor must be a dict carrying a note dict with a non-empty string id, so the
    # render/dedup/persist helpers can index entry["note"]["id"] without crashing.
    check("parse_relevant: drops non-dict entry", parse_relevant('["x", 1, null]') == [])
    check("parse_relevant: drops entry missing note", parse_relevant('[{"score": 1, "reasons": ["path"]}]') == [])
    check("parse_relevant: drops note missing id", parse_relevant('[{"note": {"title": "T"}}]') == [])
    check("parse_relevant: drops non-string id", parse_relevant('[{"note": {"id": 5}}]') == [])
    check("parse_relevant: drops empty id", parse_relevant('[{"note": {"id": ""}}]') == [])
    check("parse_relevant: drops non-dict note", parse_relevant('[{"note": "oops"}]') == [])
    mixed = parse_relevant('[{"note": {"id": "good"}}, "junk", {"note": {}}]')
    check("parse_relevant: keeps good, drops bad in mixed array", [e["note"]["id"] for e in mixed] == ["good"], repr(mixed))


def test_parse_tasks() -> None:
    check("parse_tasks: empty -> []", parse_tasks("") == [])
    check("parse_tasks: None -> []", parse_tasks(None) == [])
    check("parse_tasks: malformed -> []", parse_tasks("nope") == [])
    parsed = parse_tasks('[{"id": "0123456789", "status": "open", "title": "x"}]')
    check("parse_tasks: array round-trips", parsed == [{"id": "0123456789", "status": "open", "title": "x"}])
    # Non-dict elements are dropped so render_task_line can call .get on every survivor.
    check("parse_tasks: drops non-dict entries", parse_tasks('["x", 1, null]') == [])
    mixed = parse_tasks('[{"id": "a", "status": "open", "title": "t"}, "junk", 7]')
    check("parse_tasks: keeps dicts, drops non-dicts in mixed array", mixed == [{"id": "a", "status": "open", "title": "t"}], repr(mixed))


def test_render_note_lines() -> None:
    check("render_note_lines: empty -> []", render_note_lines([]) == [])
    fresh = render_note_lines([note_entry("0123456abcdef", drift=None, title="Retry ceiling", reasons=["path", "dir"])])
    check("render_note_lines: fresh has no drift suffix", fresh == ["0123456 Retry ceiling (path, dir)"], repr(fresh))
    drifted = render_note_lines([note_entry("89abcdef00000", drift="STALE", title="Auth flow", reasons=["branch"])])
    check("render_note_lines: drift suffix when non-null", drifted == ["89abcde Auth flow (branch) [STALE]"], repr(drifted))
    no_reasons = render_note_lines([note_entry("0000000aaaa", drift=None, title="X", reasons=[])])
    check("render_note_lines: no reasons -> no parens", no_reasons == ["0000000 X"], repr(no_reasons))


def test_render_doc_lines() -> None:
    """A kind=="doc" entry renders title + when + verdict + `doc show`, never the body."""
    fresh = render_note_lines([doc_entry("abc1234def0", when="resuming the auth cutover", drift=None, title="Auth handoff", reasons=["dir"])])
    check(
        "render: fresh doc renders when + doc show, no verdict",
        fresh == ["abc1234 Auth handoff — when: resuming the auth cutover (dir) — cc-notes doc show abc1234"],
        repr(fresh),
    )
    stale = render_note_lines([doc_entry("def5678aaa0", when="before editing the parser", drift="STALE", title="Parser notes", reasons=["path"])])
    check(
        "render: out-of-date doc carries lowercased [stale] verdict",
        stale == ["def5678 Parser notes — when: before editing the parser [stale] (path) — cc-notes doc show def5678"],
        repr(stale),
    )
    check("render: doc line never leaks the body", all("LONG_DOC_BODY" not in line for line in fresh + stale), repr(fresh + stale))
    # A mixed list dispatches per entry kind: the note renders the note line, the doc the doc line.
    mixed = render_note_lines(
        [note_entry("0123456abcdef", drift=None, title="Retry ceiling", reasons=["path"]), doc_entry("99aa00bb11c", when="when X", title="Doc", reasons=["dir"])]
    )
    check(
        "render: mixed note+doc dispatch by kind",
        mixed == ["0123456 Retry ceiling (path)", "99aa00b Doc — when: when X (dir) — cc-notes doc show 99aa00b"],
        repr(mixed),
    )


def test_render_log_lines() -> None:
    """A kind=="log" entry renders short id + title + reasons + `log show`, no drift, no when."""
    line = render_log_line(log_entry("abc1234def0", title="Auth rollout", reasons=["dir"]))
    check(
        "render_log_line: id + title + reasons + log show",
        line == "abc1234 Auth rollout (dir) — cc-notes log show abc1234",
        repr(line),
    )
    no_reasons = render_log_line(log_entry("0000000aaaa", title="Incident", reasons=[]))
    check("render_log_line: no reasons -> no parens", no_reasons == "0000000 Incident — cc-notes log show 0000000", repr(no_reasons))
    # Dispatch: render_note_lines routes a kind=="log" entry through render_log_line, never the note path.
    routed = render_note_lines([log_entry("def5678aaa0", title="Rollout log", reasons=["path"])])
    check(
        "render_log_line: render_note_lines dispatches log by kind",
        routed == ["def5678 Rollout log (path) — cc-notes log show def5678"],
        repr(routed),
    )
    # A log never carries a drift verdict, so the line never gains a `[...]` suffix.
    check("render_log_line: no drift suffix", "[" not in line, repr(line))
    # The chronology stays in cc-notes — only the pointer floats, never the entry text.
    check("render_log_line: never leaks entry text", "LOG_ENTRY_TEXT" not in routed[0], repr(routed))
    # A mixed list dispatches per kind: note, doc, and log each take their own render path.
    mixed = render_note_lines(
        [
            note_entry("0123456abcdef", drift=None, title="Retry ceiling", reasons=["path"]),
            doc_entry("99aa00bb11c", when="when X", title="Doc", reasons=["dir"]),
            log_entry("11bb22cc33d", title="Log", reasons=["branch"]),
        ]
    )
    check(
        "render_log_line: mixed note+doc+log dispatch by kind",
        mixed
        == [
            "0123456 Retry ceiling (path)",
            "99aa00b Doc — when: when X (dir) — cc-notes doc show 99aa00b",
            "11bb22c Log (branch) — cc-notes log show 11bb22c",
        ],
        repr(mixed),
    )


def test_filter_drifted() -> None:
    entries = [
        note_entry("aaa1111", drift=None),
        note_entry("bbb2222", drift="DRIFTED"),
        note_entry("ccc3333", drift="DANGLING"),
        note_entry("ddd4444", drift=None),
    ]
    kept = filter_drifted(entries)
    check("filter_drifted: keeps only non-null drift", [e["note"]["id"] for e in kept] == ["bbb2222", "ccc3333"], repr(kept))
    check("filter_drifted: empty -> []", filter_drifted([]) == [])
    check("filter_drifted: all fresh -> []", filter_drifted([note_entry("x", drift=None)]) == [])
    # Docs carry drift under doc.drift with no "note" key — the kind-dispatched filter must
    # keep a drifted/expired doc, not drop it for lacking a note payload.
    mixed = filter_drifted([note_entry("nnn0000", drift=None), doc_entry("ddd0001", drift="EXPIRED"), doc_entry("ddd0002", drift=None)])
    check("filter_drifted: keeps drifted doc, drops fresh doc", [entry_payload(e)["id"] for e in mixed] == ["ddd0001"], repr(mixed))


def test_render_task_line() -> None:
    check(
        "render_task_line: unassigned",
        render_task_line({"id": "104c728ea14", "status": "open", "title": "test task"}) == "104c728 open test task",
    )
    check(
        "render_task_line: assignee suffix",
        render_task_line({"id": "104c728ea14", "status": "in_progress", "title": "T", "assignee": "alice"})
        == "104c728 in_progress T @alice",
    )


def test_cap_and_render_tasks() -> None:
    check("cap_and_render_tasks: empty -> []", cap_and_render_tasks([], 7) == [])
    tasks = [{"id": f"{i:07d}xyz", "status": "open", "title": f"t{i}"} for i in range(10)]
    capped = cap_and_render_tasks(tasks, 7)
    check("cap_and_render_tasks: caps to 7 + tail", len(capped) == 8 and capped[-1] == "+3 more — run `cc-notes status`", repr(capped[-1]))
    check("cap_and_render_tasks: renders first 7", capped[0] == "0000000 open t0", repr(capped[0]))
    exact = cap_and_render_tasks(tasks[:7], 7)
    check("cap_and_render_tasks: exactly cap -> no tail", len(exact) == 7 and not exact[-1].startswith("+"), repr(exact))
    # cap+1 is the off-by-one boundary: 7 rendered + a "+1 more" tail (8 lines total).
    over_by_one = cap_and_render_tasks(tasks[:8], 7)
    check(
        "cap_and_render_tasks: cap+1 -> 7 lines + '+1 more' tail",
        len(over_by_one) == 8 and over_by_one[-1] == "+1 more — run `cc-notes status`",
        repr(over_by_one),
    )
    under = cap_and_render_tasks(tasks[:3], 7)
    check("cap_and_render_tasks: under cap -> no tail", len(under) == 3 and not under[-1].startswith("+"))


def test_durable_internal_write_condition() -> None:
    """DurableInternalWrite.check fires on durable-internal writes, stays silent on the rest.

    The condition is pure over the event (no PATH/CLI), so it is unit-tested
    directly via mock_event. The false-positive matrix is non-negotiable:
    published docs, source, secrets, and images MUST stay silent; the
    GOOGLE_OAUTH_VERIFICATION.md / memory/ / signal-bearing writes MUST fire.
    """
    cond = DurableInternalWrite()

    def fires(label: str, *, tool: str = "Write", file=None, content=None, command=None, expected: bool) -> None:
        evt = mock_event("PostToolUse", tool=tool, file=file, content=content, command=command)
        check(f"durable-internal: {label}", cond.check(evt) == expected, f"file={file!r} content={content!r}")

    # Positives ----------------------------------------------------------------
    fires("STRONG *_VERIFICATION.md fires", file="GOOGLE_OAUTH_VERIFICATION.md", content="# x\n## Status\n", expected=True)
    fires("STRONG nested HANDOFF.md fires (basename match)", file="work/sub/HANDOFF.md", content="brief", expected=True)
    fires("memory/ .md fires", file="memory/google-oauth-verification.md", content="status notes", expected=True)
    fires("memory/ any-extension fires", file="memory/scratch.py", content="x = 1\n", expected=True)
    fires("nested src/memory/ fires (under prefix)", file="src/memory/x.md", content="anything", expected=True)
    fires("WEAK *-notes.md + checklist body fires", file="auth-notes.md", content="next steps:\n- [ ] rotate\n", expected=True)
    fires("WEAK *-notes.md + keyword body fires", file="deploy-notes.md", content="This is a handoff for the next agent.\n", expected=True)
    fires("WEAK runbook* fires with signal", file="runbook-deploy.md", content="## Status\nremaining work\n", expected=True)
    fires("WEAK TODO.md + checklist fires", file="TODO.md", content="- [ ] ship it\n", expected=True)

    # Negatives ----------------------------------------------------------------
    fires("WEAK name, no body signal stays silent", file="auth-notes.md", content="just a heading\n", expected=False)
    fires("WEAK name, empty body stays silent", file="auth-notes.md", content="", expected=False)
    fires("published README.md silent", file="README.md", content="# Readme\n", expected=False)
    fires("published CHANGELOG.md silent", file="CHANGELOG.md", content="# Changelog\n## Status\n- [ ] x\n", expected=False)
    fires("published LICENSE.md silent", file="LICENSE.md", content="MIT\n", expected=False)
    fires("published CONTRIBUTING.md silent", file="CONTRIBUTING.md", content="## Status\nHandoff\n", expected=False)
    fires("published docs/ tree silent", file="docs/guide.md", content="# Guide\n## Status\n- [ ] x\n", expected=False)
    fires("source .ts silent", file="src/foo.ts", content="export const x = 1\n", expected=False)
    fires("source .go silent", file="main.go", content="package main\n", expected=False)
    fires("source .toml silent", file="pyproject.toml", content="[project]\nname='x'\n", expected=False)
    fires("source .yaml silent", file="config.yaml", content="status: handoff\n", expected=False)
    fires("secret .env silent", file=".env", content="API_KEY=secret\n", expected=False)
    fires("secret .env.local silent", file=".env.local", content="API_KEY=secret\n", expected=False)
    fires("secret *secret*.md silent", file="app-secret.md", content="## Status\nHandoff\n- [ ] x\n", expected=False)
    fires("secret *.key silent", file="private.key", content="-----BEGIN-----\n", expected=False)
    fires("secret *credential*.md silent", file="db-credential.md", content="## Status\nHandoff\n", expected=False)
    fires("image .png silent", file="screenshot.png", content="binary", expected=False)
    fires("no-file Bash event silent", tool="Bash", command="ls", expected=False)
    # cc-pool memory tree is the mirror's domain — the router gate excludes it, even a
    # STRONG-named slug, so a memory write hits only mirror_memory_to_note.
    fires(".cc-pool STRONG-named slug excluded (guard beats STRONG)", file="/u/.cc-pool/a/projects/-p/memory/HANDOFF.md", content="## Status\n- [ ] x\n", expected=False)
    fires(".cc-pool MEMORY.md index excluded", file="/u/.cc-pool/a/projects/-p/memory/MEMORY.md", content="# Index\n", expected=False)
    fires(".cc-pool user-memory excluded", file="/u/.cc-pool/a/projects/-p/memory/who-i-am.md", content="next steps:\n- [ ] x\n", expected=False)


def test_evidence_archive_condition() -> None:
    """EvidenceArchive fires on evidence/run-output landing in a durable tree, stays silent elsewhere.

    The historic misses (Bash not Write; non-.md payload; under docs/) each get a positive with a
    repo-relative dest (the run's cwd is a git worktree). The narrowness matrix (temp-to-temp,
    testdata fixtures, .git internals, same-parent copies, build-output dirs, rsync value flags,
    tightened dump-dir segments, .md docs, plugin source, temp writes) is non-negotiable. The
    other-repo-with-.git and no-repo-at-all destination cases need real repos, proven in
    test_evidence_dest_requires_worktree.
    """
    cond = EvidenceArchive()

    def fires(label: str, *, tool: str = "Bash", command=None, file=None, content=None, expected: bool) -> None:
        evt = mock_event("PostToolUse", tool=tool, command=command, file=file, content=content)
        check(f"evidence-archive: {label}", cond.check(evt) == expected, f"command={command!r} file={file!r}")

    # Positives ----------------------------------------------------------------
    fires(
        "fusekit cp -R of run output into the repo's docs/ fires",
        command="mkdir -p docs/reports/assets/vm-repro && "
        "cp -R /tmp/fusekit-vm/results/run-42 docs/reports/assets/vm-repro/phase2-forced-unmount",
        expected=True,
    )
    fires("mv .panic into docs/ fires", command="mv crash-4821.panic docs/reports/crash-4821.panic", expected=True)
    fires("rsync from /var/log fires", command="rsync -av /var/log/fusekit/ evidence/latest/", expected=True)
    fires("cp from a results/ segment fires", command="cp results/soak/out.txt docs/assets/out.txt", expected=True)
    fires("cp into a crashes/ dir fires", command="cp artifact.bin docs/crashes/artifact.bin", expected=True)
    fires("Write of .log under docs/ fires", tool="Write", file="docs/reports/soak.log", content="ok\n", expected=True)
    fires("Write of .panic at repo root fires", tool="Write", file="boot.panic", content="panic: x\n", expected=True)

    # Negatives ----------------------------------------------------------------
    fires("cp within /tmp silent", command="cp /tmp/run/out.log /tmp/keep/out.log", expected=False)
    fires("cp into a scratchpad segment silent", command="cp results/out.log /private/tmp/c-1/scratchpad/out.log", expected=False)
    fires("fixture into testdata/ silent", command="cp fixtures/batch.json internal/lfs/testdata/batch.json", expected=False)
    fires("mv within .git silent", command="mv .git/objects/tmp_pack .git/objects/pack/p1.pack", expected=False)
    fires("plain doc copy silent", command="cp README.md docs/index.md", expected=False)
    fires("same-parent copy inside the repo silent", command="cp -R docs/assets docs/assets-v2", expected=False)
    # Finding 1: bulk (-R) / multi-source alone is no longer a standalone qualifier.
    fires("absolute bulk cp -R with no run-output signal silent", command="cp -R /Users/y/runs-archive docs/assets/runs", expected=False)
    fires("multi-source absolute mv with no run-output signal silent", command="mv /out/a.bin /out/b.bin docs/assets/", expected=False)
    # Finding 2: rsync value-flag tokens are consumed, not read as sources.
    fires("rsync --exclude glob is a flag value silent", command="rsync -av --exclude '*.log' src/ docs/mirror/", expected=False)
    fires("rsync --exclude results value silent", command="rsync -av --exclude results src/ docs/mirror/", expected=False)
    # Finding 4: build-output dirs are exempt destinations.
    fires("cp of a build into bin/ silent", command="cp /tmp/built bin/app", expected=False)
    fires("rsync of a build into dist/ silent", command="rsync -a /var/tmp/build/ dist/", expected=False)
    # Finding 5: tightened dump-dir segments and same-parent renames.
    fires("cp of a .go under a 'crash' package dir silent", command="cp internal/crash/handler.go internal/crash2/handler.go", expected=False)
    fires("same-parent log rotation rename silent", command="mv app.log app.log.1", expected=False)
    fires("go build silent (not a transfer)", command="go build -o bin/cc-notes ./cmd/cc-notes", expected=False)
    fires("rsync to a remote host silent", command="rsync -av results/ backup:archive/", expected=False)
    fires("dest under $TMPDIR silent", command="cp results/out.log $TMPDIR/out.log", expected=False)
    fires("Write .md under docs/ silent (writing-docs owns it)", tool="Write", file="docs/guide.md", content="# G\n", expected=False)
    fires("Write .log into /tmp silent", tool="Write", file="/tmp/debug.log", content="x\n", expected=False)
    fires("Write of the pack's own source silent", tool="Write", file="plugin/hooks/record.py", content="X = '.log'\n", expected=False)
    fires("no-command no-file Bash event silent", command=None, expected=False)


def test_evidence_transfers_parsing() -> None:
    """The transfer parser walks compound commands, skips flags, consumes rsync value flags, applies the rules."""
    from cc_transcript.command import Command, CommandLine

    compound = evidence_transfers(CommandLine.parse("mkdir -p docs/x && cp -R /tmp/r/results/run-1 docs/x/run-1"))
    check("transfers: compound picks the cp leg", compound == ["docs/x/run-1"], repr(compound))
    check("transfers: flags are not paths", evidence_transfers(CommandLine.parse("cp -v /tmp/a.log docs/a.log")) == ["docs/a.log"])
    check("transfers: single-path cp ignored", evidence_transfers(CommandLine.parse("cp -R lone-arg")) == [])
    # Finding 1: a multi-source absolute mv is NOT run-output on bulkness alone -> silent.
    check("transfers: multi-source absolute mv no longer bulk-fires", evidence_transfers(CommandLine.parse("mv /out/a.bin /out/b.bin evidence/")) == [], repr(evidence_transfers(CommandLine.parse("mv /out/a.bin /out/b.bin evidence/"))))
    check("transfers: multi-source relative mv is not", evidence_transfers(CommandLine.parse("mv a.bin b.bin evidence/")) == [])
    check("transfers: remote rsync dest exempt", evidence_transfers(CommandLine.parse("rsync -av results/ backup:archive/")) == [])
    # Finding 2: rsync value-flag tokens are consumed, not counted as operands.
    check("transfer_operands: cp keeps every non-flag token", transfer_operands(Command.parse("cp -R /tmp/a /b/")) == ["/tmp/a", "/b/"], repr(transfer_operands(Command.parse("cp -R /tmp/a /b/"))))
    check("transfer_operands: rsync consumes --exclude's value token", transfer_operands(Command.parse("rsync -av --exclude '*.log' src/ dest/")) == ["src/", "dest/"], repr(transfer_operands(Command.parse("rsync -av --exclude '*.log' src/ dest/"))))
    check("transfers: rsync exclude glob is not read as a source", evidence_transfers(CommandLine.parse("rsync -av --exclude '*.log' src/ docs/x/")) == [], repr(evidence_transfers(CommandLine.parse("rsync -av --exclude '*.log' src/ docs/x/"))))
    check("transfers: rsync exclude 'results' value is not read as run-output", evidence_transfers(CommandLine.parse("rsync -av --exclude results src/ docs/x/")) == [], repr(evidence_transfers(CommandLine.parse("rsync -av --exclude results src/ docs/x/"))))
    check("transfers: rsync still sees the real /tmp/results source past a consumed flag", evidence_transfers(CommandLine.parse("rsync -av --exclude '*.tmp' /tmp/run/results/ docs/x/")) == ["docs/x/"], repr(evidence_transfers(CommandLine.parse("rsync -av --exclude '*.tmp' /tmp/run/results/ docs/x/"))))


def test_evidence_router_tool_gate(monkeypatch) -> None:
    """A Read of an evidence file passes the pure condition but the Tool gate keeps the router silent."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    from captain_hook.conditions import matches_conditions

    read_evt = mock_event("PostToolUse", tool="Read", file="docs/reports/soak.log")
    check("evidence gate: Read never matches", not matches_conditions(_spec_for(nudge_record_evidence), read_evt))
    write_evt = mock_event("PostToolUse", tool="Write", file="docs/reports/soak.log", content="x\n")
    check("evidence gate: Write matches", matches_conditions(_spec_for(nudge_record_evidence), write_evt))


def test_evidence_router_fires(monkeypatch, tmp_path) -> None:
    """The Bash firing path warns with the log+attach recipe, the sync-only transfer rule, and the push hole."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = mock_event(
        "PostToolUse",
        tool="Bash",
        command="cp -R /tmp/fusekit-vm/results/run-42 docs/reports/assets/vm-repro/phase2",
        session_dir=tmp_path,
    )
    result = nudge_record_evidence(evt)
    check("evidence router: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("evidence router: cites log add", "cc-notes log add" in result.message, result.message)
        check("evidence router: cites log append --attach", 'log append <id> -m "<verdict>" --attach <file>' in result.message, result.message)
        check("evidence router: names the sync-only transfer", "only `cc-notes sync` uploads" in result.message, result.message)
        check("evidence router: names the plain git push hole", "`git push` moves refs without it" in result.message, result.message)
        check("evidence router: no tripwire wording for an unstatable dest", "LFS attachment is one flag" not in result.message, result.message)


def test_evidence_router_size_tripwire(monkeypatch, tmp_path) -> None:
    """A single Write landing >1MB of evidence strengthens the wording; a small one keeps the plain nudge."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    big = mock_event("PostToolUse", tool="Write", file="docs/reports/soak.log", content="x" * ((1 << 20) + 1), session_dir=tmp_path)
    result = nudge_record_evidence(big)
    check("tripwire: big write warns", result is not None, repr(result))
    if result and result.message:
        check("tripwire: strengthened wording", "git history is forever; an LFS attachment is one flag" in result.message, result.message)
    small = mock_event("PostToolUse", tool="Write", file="docs/reports/tiny.log", content="one line\n", session_dir=tmp_path)
    result2 = nudge_record_evidence(small)
    check(
        "tripwire: small write warns without the strengthened wording",
        result2 is not None and "LFS attachment is one flag" not in (result2.message or ""),
        repr(result2),
    )


def test_evidence_payload_bytes(tmp_path) -> None:
    """tree_bytes stats what actually landed; the Bash payload resolves the destination against cwd."""
    single = tmp_path / "one.log"
    single.write_bytes(b"x" * 1234)
    check("tree_bytes: single file size", tree_bytes(single) == 1234)
    run_dir = tmp_path / "run"
    (run_dir / "sub").mkdir(parents=True)
    (run_dir / "a.log").write_bytes(b"a" * 100)
    (run_dir / "sub" / "b.panic").write_bytes(b"b" * 200)
    check("tree_bytes: directory sums recursively", tree_bytes(run_dir) == 300)
    check("tree_bytes: missing path is 0", tree_bytes(tmp_path / "nope") == 0)

    # The Bash payload resolves the relative dest against cwd; durable_dest now requires that
    # tree to be a git worktree, so init one (mirrors how real evidence lands in a repo).
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    dest = tmp_path / "vm-repro"
    dest.mkdir()
    (dest / "boot.log").write_bytes(b"z" * ((1 << 20) + 1))
    evt = mock_event("PostToolUse", tool="Bash", command="cp -R /tmp/run/results vm-repro")
    old_cwd = os.getcwd()
    os.chdir(tmp_path)
    try:
        check("payload bytes: Bash sums the landed relative dest", evidence_payload_bytes(evt) > 1 << 20)
    finally:
        os.chdir(old_cwd)


def test_in_git_worktree_expands_home(tmp_path) -> None:
    """in_git_worktree expands ~ before walking, so a ~-rooted dest isn't read as a cwd-relative literal.

    Run under a git-init'd cwd: were ``.expanduser()`` dropped, ``~/Downloads/x`` would resolve to
    ``<cwd>/~/Downloads/x`` and walk up into this repo's .git (True), diverging from the real-home
    resolution (no repo at $HOME here, so False). Equality proves the expansion is applied.
    """
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    old = os.getcwd()
    try:
        os.chdir(tmp_path)
        expanded = in_git_worktree(os.path.expanduser("~/Downloads/x"))
        check("tilde: ~ resolves to real home, not a cwd-relative literal", in_git_worktree("~/Downloads/x") == expanded, "expanduser not applied?")
    finally:
        os.chdir(old)


def test_evidence_dest_requires_worktree(tmp_path) -> None:
    """durable_dest counts a dest only inside a git worktree: another repo's .git still catches, a non-repo dest is silent (finding 3).

    The two runs differ only in whether the destination tree is a git worktree — same run-output
    source, same relative dest. Inside a repo (the fusekit archetype landing in ANOTHER repo's
    docs/) it fires; in a directory outside any repo (`cp /tmp/build/x /usr/local/bin` shape) it
    is silent.
    """
    import shutil
    import tempfile
    from cc_transcript.command import CommandLine

    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)  # the "other repo" — has .git
    loose = Path(tempfile.mkdtemp())  # a tree outside any repo
    fusekit = "cp -R /tmp/fusekit-vm/results/run-42 docs/reports/assets/vm-repro/phase2"
    old = os.getcwd()
    try:
        os.chdir(tmp_path)
        fired = evidence_transfers(CommandLine.parse(fusekit))
        check("worktree: run-output cp into a repo's docs/ fires", fired == ["docs/reports/assets/vm-repro/phase2"], repr(fired))
        check("worktree: durable_dest True inside a repo", durable_dest("docs/reports/run-1"))
        check("worktree: in_git_worktree True inside a repo", in_git_worktree("docs/reports/run-1"))
        os.chdir(loose)
        silent = evidence_transfers(CommandLine.parse(fusekit))
        check("worktree: identical cp outside any repo is silent", silent == [], repr(silent))
        check("worktree: durable_dest False outside any repo", not durable_dest("docs/reports/run-1"))
        check("worktree: in_git_worktree False outside any repo", not in_git_worktree("docs/reports/run-1"))
    finally:
        os.chdir(old)
        shutil.rmtree(loose, ignore_errors=True)


def test_evidence_bulk_not_standalone_trigger(tmp_path) -> None:
    """Bulk (-R)/multi-source alone no longer qualifies; only a run-output-ish source does (finding 1).

    Run inside a git worktree so the dest is durable and the source rule is isolated: a bulk or
    multi-source copy of a plain (absolute) source tree is silent, while the same shape from a
    /tmp results dir still fires.
    """
    from cc_transcript.command import CommandLine

    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    old = os.getcwd()
    try:
        os.chdir(tmp_path)
        bulk_src = "cp -R /abs/pkg/internal/store internal/store.bak"
        multi_src = "cp /abs/a.go /abs/b.go pkg/"
        run_out = "cp -R /tmp/run/results/run-1 internal/archive"
        check("bulk: -R of a plain source tree is silent", evidence_transfers(CommandLine.parse(bulk_src)) == [], repr(evidence_transfers(CommandLine.parse(bulk_src))))
        check("bulk: multi-source absolute copy is silent", evidence_transfers(CommandLine.parse(multi_src)) == [], repr(evidence_transfers(CommandLine.parse(multi_src))))
        check("bulk: -R from a /tmp results source still fires", evidence_transfers(CommandLine.parse(run_out)) == ["internal/archive"], repr(evidence_transfers(CommandLine.parse(run_out))))
    finally:
        os.chdir(old)


def test_ephemeral_record_reference_condition() -> None:
    """EphemeralRecordReference fires on a record command that leans on a purge-bound path, silent elsewhere.

    The smell is a note/doc/log record whose title or body text points at a /tmp, /var, or
    session-scratchpad path; an ``--attach`` value (both forms) is the durable fix, not the smell,
    and a non-record subcommand or a non-cc-notes command stays silent.
    """
    cond = EphemeralRecordReference()

    def fires(label: str, command: str, *, expected: bool) -> None:
        evt = mock_event("PostToolUse", tool="Bash", command=command)
        check(f"ephemeral-cond: {label}", cond.check(evt) == expected, f"command={command!r}")

    # Positives ----------------------------------------------------------------
    fires("scratchpad in a doc title fires", 'cc-notes doc add "Handoff — see session scratchpad h.md" --when w', expected=True)
    fires("/tmp/ in a note body fires", 'cc-notes note add "Fact" --body "detail in /tmp/x.md"', expected=True)
    fires("/private/var/ in a log append entry fires", 'cc-notes log append abc -m "ran, output in /private/var/folders/x/out.log"', expected=True)
    fires("/private/tmp/ in a --body= value fires (equals form)", "cc-notes note add Fact --body=/private/tmp/c-1/scratch.md", expected=True)

    # Negatives ----------------------------------------------------------------
    fires("--attach two-token value is not a smell", "cc-notes log append abc --attach /tmp/out.log", expected=False)
    fires("--attach=equals value is not a smell", "cc-notes log append abc --attach=/tmp/out.log", expected=False)
    fires("--tag scratchpad value is skipped, inline body is clean", 'cc-notes note add "Fact" --body "content inline" --tag scratchpad', expected=False)
    fires("--branch eng/var/cleanup value is skipped, inline body is clean", 'cc-notes note add "Fact" --body "inline" --branch eng/var/cleanup', expected=False)
    fires("doc show of an ephemeral arg is not a record write", "cc-notes doc show /tmp/whatever", expected=False)
    fires("non-cc-notes command mentioning /tmp is silent", "cat /tmp/scratch.md", expected=False)


def test_ephemeral_record_refs_parsing() -> None:
    """ephemeral_record_refs collects marker-bearing tokens of record legs, skips --attach values, walks compounds."""
    from cc_transcript.command import CommandLine

    compound = ephemeral_record_refs(CommandLine.parse('mkdir -p /tmp/x && cc-notes doc add "see scratchpad note.md" --when w'))
    check("refs: compound picks the cc-notes leg's title, ignores the mkdir /tmp arg", compound == ["see scratchpad note.md"], repr(compound))
    body = ephemeral_record_refs(CommandLine.parse('cc-notes note add "Fact" --body "detail in /tmp/x.md"'))
    check("refs: collects an ephemeral --body value", body == ["detail in /tmp/x.md"], repr(body))
    both = ephemeral_record_refs(CommandLine.parse('cc-notes doc add "in /tmp/a" --body "in /private/var/b/"'))
    check("refs: collects title and body in order", both == ["in /tmp/a", "in /private/var/b/"], repr(both))
    attach2 = ephemeral_record_refs(CommandLine.parse("cc-notes log append abc --attach /tmp/out.log"))
    check("refs: --attach two-token value skipped", attach2 == [], repr(attach2))
    attach_eq = ephemeral_record_refs(CommandLine.parse("cc-notes log append abc --attach=/tmp/out.log"))
    check("refs: --attach=equals value skipped", attach_eq == [], repr(attach_eq))
    mixed = ephemeral_record_refs(CommandLine.parse('cc-notes log append abc "see scratchpad" --attach /tmp/out.log'))
    check("refs: skips only the attach value, keeps a scratchpad title", mixed == ["see scratchpad"], repr(mixed))
    non_record = ephemeral_record_refs(CommandLine.parse("cc-notes doc show /tmp/whatever"))
    check("refs: a non-record subcommand yields nothing", non_record == [], repr(non_record))
    tag_skip = ephemeral_record_refs(CommandLine.parse('cc-notes note add "Fact" --body "content inline" --tag scratchpad'))
    check("refs: --tag scratchpad value is skipped, clean body kept out", tag_skip == [], repr(tag_skip))
    branch_skip = ephemeral_record_refs(CommandLine.parse('cc-notes note add "Fact" --body "inline" --branch eng/var/cleanup'))
    check("refs: --branch eng/var/cleanup value is skipped", branch_skip == [], repr(branch_skip))
    body_eq = ephemeral_record_refs(CommandLine.parse("cc-notes note add Fact --body=/private/tmp/c-1/scratch.md"))
    check("refs: collects an ephemeral --body=equals value", body_eq == ["/private/tmp/c-1/scratch.md"], repr(body_eq))


def test_ephemeral_record_reference_fires(monkeypatch, tmp_path) -> None:
    """The firing handler warns and teaches both --body - and --attach as the durable fix."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = mock_event(
        "PostToolUse",
        tool="Bash",
        command='cc-notes doc add "Handoff — full detail in session scratchpad steering-handoff.md" --when w',
        session_dir=tmp_path,
    )
    result = nudge_ephemeral_record_reference(evt)
    check("ephemeral nudge: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("ephemeral nudge: teaches --attach", "--attach" in result.message, result.message)
        check("ephemeral nudge: teaches --body", "--body" in result.message, result.message)
        check("ephemeral nudge: names a purge-bound path", "purge-bound" in result.message, result.message)


def test_in_cc_pool_memory() -> None:
    """The shared predicate that makes the mirror (A) and the record-router (B) disjoint."""
    check("cc-pool memory slug matches", in_cc_pool_memory(Path("/u/.cc-pool/a/projects/-p/memory/x.md")))
    check("cc-pool MEMORY.md is in the tree", in_cc_pool_memory(Path("/u/.cc-pool/a/projects/-p/memory/MEMORY.md")))
    check("generic repo memory/ is NOT cc-pool", not in_cc_pool_memory(Path("repo/memory/x.md")))
    check("non-memory dir under .cc-pool excluded", not in_cc_pool_memory(Path("/u/.cc-pool/a/projects/-p/notes/x.md")))


def test_record_command_per_kind() -> None:
    """Each kind renders the right cc-notes command; log is two lines (add + append), no --when."""
    check("note: add --body -", record_command("note", "Retry cap", "", "internal/api") == ['cc-notes note add "Retry cap" --dir internal/api --body -'], repr(record_command("note", "Retry cap", "", "internal/api")))
    check("note: '.' area drops --dir", "--dir" not in record_command("note", "T", "", ".")[0])
    check("doc: carries --when", '--when "read me when X"' in record_command("doc", "T", "read me when X", ".")[0])
    log_lines = record_command("log", "Outage timeline", "", "ops")
    check("log: two lines, add then append", len(log_lines) == 2 and log_lines[0].startswith('cc-notes log add "Outage timeline"') and "log append" in log_lines[1], repr(log_lines))
    check("log: no --when", all("--when" not in ln for ln in log_lines))
    check("task: task add", record_command("task", "Do it", "", ".")[0].startswith('cc-notes task add "Do it"'))


def stub_cli(mapping: dict[tuple[str, ...], str]):
    """Build a call_cli stub mapping ``cc-notes`` arg tuples to canned payloads.

    A key not in ``mapping`` surfaces the gap: with ``throw`` (the real
    ``call_cli`` default) it raises ``FileNotFoundError`` so a handler that runs
    an unexpected command isn't silently passed; with ``throw=False`` (how
    ``run_cc_notes`` invokes it) it returns ``None``, mirroring the real
    fail-closed contract. The gate no longer shells out — it reads
    ``shutil.which`` only — so no ``git`` probe is stubbed here.
    """

    def _call(args, *, input=None, timeout=30, env=None, throw=True):
        key = tuple(args[1:])
        if key in mapping:
            return mapping[key]
        if not throw:
            return None
        raise FileNotFoundError(args[0])

    return _call


def stub_llm(verdict: object):
    """Build a call_llm stub returning a fixed response-model instance for any prompt.

    Mirrors stub_cli: the test monkeypatches it onto ``evt.ctx.call_llm``. Each handler
    passes ``response_model=<Model>`` and the real backend parses the reply into that
    model, so the stub just returns an already-built instance — a RecordVerdict for the
    record routers, a PlanTasks for the plan handler, a SurfacePick for the filter.
    """

    def _call(template, *args, **kwargs):
        return verdict

    return _call


def stub_git(mapping: dict[tuple[str, ...], str | None]):
    """Build an ``evt.ctx.git`` stub mapping git argv tuples to canned stdout.

    A tuple not in ``mapping`` returns None, mirroring the real ``git`` helper's
    fail-closed contract (it returns None when the command errors). The branch-name
    probe ``("rev-parse", "--abbrev-ref", "HEAD")`` that auto_reconcile reads is just
    another tuple key — pass it in ``mapping`` to drive the reconcile path.
    """

    def _git(*args: str):
        return mapping.get(tuple(args))

    return _git


def recording_cli(
    mapping: dict[tuple[str, ...], str | None] | None = None,
    *,
    raises: dict[tuple[str, ...], BaseException] | None = None,
):
    """A recording ``evt.ctx.call_cli`` stub for the workflow side-effect handlers.

    Records every argv it sees so a test can assert the exact invocation count and the
    ORDER in which ``cc-notes sync`` / ``cc-notes reconcile`` ran. Dispatch precedence
    per ``cc-notes`` arg tuple (the ``args[1:]`` key, like ``stub_cli``):

    - a key in ``raises`` re-raises that exception (the real ``call_cli`` surfacing a
      ``CalledProcessError`` / ``TimeoutExpired`` / ``FileNotFoundError`` from the
      subprocess) — this is how ``do_sync``'s stderr-inspection branches are exercised;
    - a key in ``mapping`` returns its canned stdout;
    - otherwise the throw-vs-None contract of ``stub_cli`` (``run_cc_notes`` passes
      ``throw=False`` and wants None; a bare ``call_cli`` wants the raise).

    Returns ``(call, calls)`` where ``calls`` is the live argv-tuple list, newest last.
    """
    mapping = mapping or {}
    raises = raises or {}
    calls: list[tuple[str, ...]] = []

    def _call(args, *, input=None, timeout=30, env=None, throw=True):
        key = tuple(args[1:])
        calls.append(tuple(args))
        if key in raises:
            raise raises[key]
        if key in mapping:
            return mapping[key]
        if not throw:
            return None
        raise FileNotFoundError(args[0])

    return _call, calls


def _calls_of(calls: list[tuple[str, ...]], *suffix: str) -> list[int]:
    """Indices into ``calls`` whose argv == ``("cc-notes", *suffix)`` — for order asserts."""
    target = ("cc-notes", *suffix)
    return [i for i, c in enumerate(calls) if c == target]


def _llm_boom(*args, **kwargs):
    """A call_llm stub that raises — proves the router falls closed to silence."""
    raise RuntimeError("classifier unavailable")


# A long-form internal handoff: written for the next agent, not human-facing docs.
HANDOFF_BODY = (
    "# Auth cutover handoff\n\n"
    "Status: half-done. The old session middleware still runs in parallel with the "
    "new token flow. Before you touch internal/api/auth.go, read this.\n\n"
    "## What's done\n- New JWT verifier wired into the gateway.\n"
    "## What's left\n- Delete the legacy cookie path once the dual-write window "
    "closes.\n- Reconcile the two refresh-token tables.\n\n"
    "## Gotchas\nThe migration script is NOT idempotent yet; re-running double-writes "
    "the refresh tokens. Run it exactly once.\n" + "More resume context for the next agent. " * 20
)


# gated_handlers maps each gated read-time handler to a tool its own Tool
# condition matches (UserPromptSubmit carries no tool). Picking the right tool
# isolates the gate: when which() opens the gate, matches_conditions can only
# fail on the gate itself, not on a tool mismatch.
gated_handlers = [
    (float_session_tasks, Event.UserPromptSubmit, None),
    (float_note_context, Event.PostToolUse, "Read"),
    (check_note_staleness, Event.PostToolUse, "Edit"),
]


def _gate_event(ev_type: Event, tool: str | None):
    return mock_event(
        ev_type.name,
        tool=tool,
        file="internal/store/store.go",
        prompt="start work",
    )


def test_gate_silent_when_cc_notes_absent(monkeypatch) -> None:
    """With cc-notes off PATH, CcNotesAvailable fails closed and every handler is silent."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: None)

    from captain_hook.conditions import matches_conditions

    for handler, ev_type, tool in gated_handlers:
        evt = _gate_event(ev_type, tool)
        gated = matches_conditions(_spec_for(handler), evt)
        check(f"gate-absent: {handler.__name__} condition fails closed", not gated)


def test_gate_open_when_cc_notes_present(monkeypatch) -> None:
    """With cc-notes on PATH the gate opens even in a repo with NO refs/cc-notes/*.

    The refs requirement is gone, so check() reads shutil.which only. To keep this
    a real regression test, we force evt.ctx.git to return None — a refs-free probe.
    The binary-only gate ignores git entirely, so it still opens; but if the dropped
    refs probe were restored, it would read that None, fail closed, and fail this
    test. (Without this stub the probe would run real git in the ambient cwd — the
    cc-notes repo itself carries refs/cc-notes/*, so a restored gate would pass too
    and the test would prove nothing.)
    """
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")

    from captain_hook.conditions import matches_conditions

    for handler, ev_type, tool in gated_handlers:
        evt = _gate_event(ev_type, tool)
        monkeypatch.setattr(evt.ctx, "git", lambda *_a, **_k: None)
        gated = matches_conditions(_spec_for(handler), evt)
        check(f"gate-present: {handler.__name__} condition opens with no refs", gated)


def _spec_for(handler):
    """Return the registered hook spec whose handler is ``handler``."""
    from captain_hook.app import _state

    for entry in _state.hooks:
        if entry.handler is handler:
            return entry.spec
    raise AssertionError(f"no registered hook for {handler.__name__}")


def test_float_session_tasks_fires(monkeypatch, tmp_path) -> None:
    """With the gate forced open and a stubbed task list, the floater warns with capped lines."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    branch = [{"id": f"branch{i:02d}aaa", "status": "in_progress", "title": f"b{i}", "assignee": "me"} for i in range(3)]
    backlog = [{"id": f"backlog{i:02d}b", "status": "open", "title": f"k{i}"} for i in range(6)]
    mapping = {
        ("task", "list", "--json"): json.dumps(branch),
        ("task", "list", "--backlog", "--json"): json.dumps(backlog),
    }
    evt = mock_event("UserPromptSubmit", prompt="let's start", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))

    result = float_session_tasks(evt)
    check("float fires: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("float fires: orientation line present", "cc-notes status" in result.message)
        check("float fires: caps at 7 (3 branch + 4 backlog)", "branch00aaa"[:7] in result.message and "backlog03"[:7] in result.message, result.message)
        check("float fires: +K more tail", "+2 more — run `cc-notes status`" in result.message, result.message)
        check("float fires: assignee rendered", "@me" in result.message)


def test_float_session_tasks_silent_no_tasks(monkeypatch, tmp_path) -> None:
    """Gate open but zero tasks -> the floater stays silent."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    mapping = {
        ("task", "list", "--json"): "[]",
        ("task", "list", "--backlog", "--json"): "[]",
    }
    evt = mock_event("UserPromptSubmit", prompt="hi", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    check("float silent: no tasks -> None", float_session_tasks(evt) is None)


def test_install_nudge_gate(monkeypatch, tmp_path) -> None:
    """CcNotesMissing inverts the gate: OPEN when the binary is absent, CLOSED when present."""
    from captain_hook.conditions import matches_conditions

    evt = mock_event("UserPromptSubmit", prompt="start work", session_dir=tmp_path)

    monkeypatch.setattr(common.shutil, "which", lambda _name: None)
    check("install nudge: gate opens when binary absent", matches_conditions(_spec_for(prompt_install_cc_notes), evt))

    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    check("install nudge: gate closes when binary present", not matches_conditions(_spec_for(prompt_install_cc_notes), evt))


def test_install_nudge_message(monkeypatch, tmp_path) -> None:
    """The body is unconditional (the gate lives in the decorator) and names both install paths."""
    evt = mock_event("UserPromptSubmit", prompt="start work", session_dir=tmp_path)
    result = prompt_install_cc_notes(evt)
    check("install nudge: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("install nudge: brew path present", "brew install yasyf/tap/cc-notes" in result.message, result.message)
        check("install nudge: install.sh path present", "scripts/install.sh" in result.message, result.message)
        check("install nudge: mentions PATH", "PATH" in result.message, result.message)


def test_float_note_context_dedup(monkeypatch, tmp_path) -> None:
    """First read floats the note; a second read of the same note is deduped to silence."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps([note_entry("deadbeef000", drift=None, title="Schema", reasons=["dir"])])
    mapping = {("relevant", "internal/store/store.go", "--json"): payload}

    evt = mock_event("PostToolUse", tool="Read", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    first = float_note_context(evt)
    check("note floater: first read warns", first is not None and first.action is Action.warn, repr(first))
    if first and first.message:
        check("note floater: surfaces the note", "deadbee Schema" in first.message, first.message)

    evt2 = mock_event("PostToolUse", tool="Read", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("note floater: second read deduped -> None", float_note_context(evt2) is None)


def test_check_note_staleness_drift_only(monkeypatch, tmp_path) -> None:
    """Only drifted notes prompt reconciliation; fresh ones are ignored; dedup holds."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps(
        [
            note_entry("fresh000aaa", drift=None, title="Fresh", reasons=["path"]),
            note_entry("stale000bbb", drift="STALE", title="Stale fact", reasons=["path"]),
        ]
    )
    mapping = {("relevant", "internal/store/store.go", "--attached", "--worktree", "--json"): payload}

    evt = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    result = check_note_staleness(evt)
    check("staleness: warns on drifted", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("staleness: names the file", "internal/store/store.go" in result.message)
        check("staleness: lists only drifted note", "stale00" in result.message and "fresh00" not in result.message, result.message)
        check("staleness: names note reconciliation commands", "cc-notes note verify/edit/supersede/expire" in result.message, result.message)
        check("staleness: names doc reconciliation commands", "cc-notes doc verify/edit/supersede/expire" in result.message, result.message)
        check("staleness: points at the file-edit workflow", "--checkout" in result.message and "--apply" in result.message, result.message)

    evt2 = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("staleness: re-edit deduped -> None", check_note_staleness(evt2) is None)


def test_check_note_staleness_all_fresh_silent(monkeypatch, tmp_path) -> None:
    """An edit near only-fresh notes prompts nothing."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps([note_entry("fresh000aaa", drift=None)])
    mapping = {("relevant", "internal/store/store.go", "--attached", "--worktree", "--json"): payload}
    evt = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    check("staleness: all fresh -> None", check_note_staleness(evt) is None)


def test_check_note_staleness_drifted_doc(monkeypatch, tmp_path) -> None:
    """A drifted kind=="doc" entry on the edited path warns, names doc commands, never leaks the body.

    Mirrors test_check_note_staleness_drift_only for a doc: drift lives under
    ``doc.drift`` (no ``note`` key), so the kind-dispatched filter/persist must
    keep it, render it through the doc-line path (pointer only), and surface the
    doc reconciliation commands.
    """
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps(
        [doc_entry("drifteddoc01", when="before touching the parser", drift="DRIFTED", title="Parser handoff", reasons=["path"])]
    )
    mapping = {("relevant", "internal/store/store.go", "--attached", "--worktree", "--json"): payload}

    evt = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    result = check_note_staleness(evt)
    check("staleness doc: warns on drifted doc", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("staleness doc: renders doc pointer", "Parser handoff" in result.message and "cc-notes doc show drifted" in result.message, result.message)
        check("staleness doc: lowercased verdict", "[drifted]" in result.message, result.message)
        check("staleness doc: never leaks the body", "LONG_DOC_BODY" not in result.message, result.message)
        check("staleness doc: names doc reconciliation commands", "cc-notes doc verify/edit/supersede/expire" in result.message, result.message)

    evt2 = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("staleness doc: re-edit deduped -> None", check_note_staleness(evt2) is None)


def test_check_note_staleness_multi_filters_but_judges_all(monkeypatch, tmp_path) -> None:
    """With 2+ drifted records the filter surfaces only the LLM's pick, yet marks ALL drifted judged once/session.

    Mirrors test_float_note_context_multi_filters_but_judges_all for the edit-time path:
    check_note_staleness drives the same mark-all-before-filter ordering, so it needs its
    own multi-candidate litmus. The re-edit fires a fail-OPEN LLM stub: if the unpicked
    drf0002bbb were not marked judged on the first pass, it would resurface here, so the
    silent second call is the behavioral proof that ALL drifted ids were marked.
    """
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps(
        [
            note_entry("drf0001aaa", drift="STALE", title="Keep"),
            note_entry("drf0002bbb", drift="DRIFTED", title="Drop"),
            note_entry("drf0003ccc", drift="EXPIRED", title="Keep2"),
        ]
    )
    mapping = {("relevant", "internal/store/store.go", "--attached", "--worktree", "--json"): payload}
    evt = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(SurfacePick(ids=["drf0001aaa", "drf0003ccc"])))
    result = check_note_staleness(evt)
    check("staleness multi: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("staleness multi: surfaces picked", "drf0001" in result.message and "drf0003" in result.message, result.message)
        check("staleness multi: drops unpicked", "drf0002" not in result.message, result.message)
    evt2 = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    monkeypatch.setattr(evt2.ctx, "call_llm", _llm_boom)
    check("staleness multi: re-edit fully deduped to silence (unpicked drf0002 was marked)", check_note_staleness(evt2) is None)


def test_float_and_staleness_scopes_are_isolated(monkeypatch, tmp_path) -> None:
    """A read-time float of id X must NOT suppress the edit-time staleness warning for that same X.

    The two handlers dedup under distinct scopes ("floated" vs "stale") on the SAME session
    store. If they shared a scope, floating X on read would mark it judged everywhere and the
    later edit-time drift warning for X would be wrongly swallowed. Both events share one
    session_dir so the scopes coexist; only the scope split keeps them independent.
    """
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    note_id = "sharedid0001"
    read_payload = json.dumps([note_entry(note_id, drift=None, title="Shared fact", reasons=["dir"])])
    edit_payload = json.dumps([note_entry(note_id, drift="STALE", title="Shared fact", reasons=["path"])])

    read_evt = mock_event("PostToolUse", tool="Read", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(read_evt.ctx, "call_cli", stub_cli({("relevant", "internal/store/store.go", "--json"): read_payload}))
    read_result = float_note_context(read_evt)
    check("scope isolation: read floats the shared note", read_result is not None and note_id[:7] in (read_result.message or ""), repr(read_result))

    edit_evt = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(
        edit_evt.ctx, "call_cli", stub_cli({("relevant", "internal/store/store.go", "--attached", "--worktree", "--json"): edit_payload})
    )
    edit_result = check_note_staleness(edit_evt)
    check(
        "scope isolation: a read-time float does NOT suppress the edit-time staleness warning for the same id",
        edit_result is not None and note_id[:7] in (edit_result.message or ""),
        repr(edit_result),
    )


def test_run_cc_notes_passes_throw_false(monkeypatch, tmp_path) -> None:
    """run_cc_notes invokes call_cli with throw=False, delegating fail-closed to capt-hook.

    The swallowing of every subprocess failure mode (missing binary, non-zero
    exit, timeout) now lives in ``call_cli(throw=False)``; the pack's job is only
    to pass that flag and return whatever comes back. The stub returns None on the
    throw=False contract, mirroring the real backend.
    """
    evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)

    seen: dict[str, object] = {}

    def _call(args, *, input=None, timeout=30, env=None, throw=True):
        seen["throw"] = throw
        return None

    monkeypatch.setattr(evt.ctx, "call_cli", _call)
    result = run_cc_notes(evt, "relevant", "x.go", "--json")
    check("run_cc_notes: passes throw=False", seen.get("throw") is False, repr(seen.get("throw")))
    check("run_cc_notes: returns call_cli result (None)", result is None, repr(result))


def test_handlers_silent_on_malformed_array(monkeypatch, tmp_path) -> None:
    """A JSON array of ill-shaped entries never crashes a handler — it stays silent."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    junk = '["x", 1, null, {"note": "oops"}, {"note": {"id": ""}}, {"score": 1}]'

    read_map = {("relevant", "x.go", "--json"): junk}
    edit_map = {("relevant", "x.go", "--attached", "--worktree", "--json"): junk}

    def silent_or_fail(label: str, handler, evt) -> None:
        """Record a clean FAIL (not an aborting traceback) if a handler raises on junk."""
        try:
            check(f"malformed array: {label} -> None", handler(evt) is None)
        except BaseException as raised:  # noqa: BLE001 — the defect is exactly an escaped crash
            check(f"malformed array: {label} -> None", False, f"crashed: {type(raised).__name__}: {raised}")

    read_evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)
    monkeypatch.setattr(read_evt.ctx, "call_cli", stub_cli(read_map))
    silent_or_fail("float_note_context", float_note_context, read_evt)

    edit_evt = mock_event("PostToolUse", tool="Edit", file="x.go", session_dir=tmp_path)
    monkeypatch.setattr(edit_evt.ctx, "call_cli", stub_cli(edit_map))
    silent_or_fail("check_note_staleness", check_note_staleness, edit_evt)

    # A malformed task array must not crash the session-start floater either. Two non-dict
    # backlog entries are dropped; the surviving {} renders a degenerate (blank-field) line
    # but the handler must return a result without raising.
    task_map = {
        ("task", "list", "--json"): '["junk", 5]',
        ("task", "list", "--backlog", "--json"): '[null, {}]',
    }
    prompt_evt = mock_event("UserPromptSubmit", prompt="hi", session_dir=tmp_path)
    monkeypatch.setattr(prompt_evt.ctx, "call_cli", stub_cli(task_map))
    try:
        float_session_tasks(prompt_evt)
        check("malformed array: float_session_tasks does not crash", True)
    except BaseException as raised:  # noqa: BLE001
        check("malformed array: float_session_tasks does not crash", False, f"{type(raised).__name__}: {raised}")


def test_record_router_routes_doc(monkeypatch, tmp_path) -> None:
    """A gated write the LLM marks record=True, kind=doc warns with `cc-notes doc add … --when …`."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Write", file="HANDOFF.md", content=HANDOFF_BODY, session_dir=tmp_path)
    verdict = RecordVerdict(record=True, kind="doc", title="Auth cutover", when="resuming the auth cutover", area="internal/api", reasoning="in-flight handoff")
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(verdict))

    result = nudge_record_durable(evt)
    check("router doc: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("router doc: names cc-notes doc add", "cc-notes doc add" in result.message, result.message)
        check("router doc: carries --when", '--when "resuming the auth cutover"' in result.message, result.message)
        check("router doc: uses title", '"Auth cutover"' in result.message, result.message)
        check("router doc: uses dir", "--dir internal/api" in result.message, result.message)
        check("router doc: cites reasoning", "in-flight handoff" in result.message, result.message)


def test_record_router_routes_log(monkeypatch, tmp_path) -> None:
    """kind=log renders the two-step `log add` + `log append`, and never a --when (a log never drifts)."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Write", file="incident-notes.md", content="14:02 paged\n14:10 rolled back\n", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(RecordVerdict(record=True, kind="log", title="Outage timeline", reasoning="a chronology")))
    result = nudge_record_durable(evt)
    check("router log: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("router log: add + append", "cc-notes log add" in result.message and "cc-notes log append" in result.message, result.message)
        check("router log: no --when on a log", "--when" not in result.message, result.message)


def test_record_router_silent_when_not_recorded(monkeypatch, tmp_path) -> None:
    """record=False (a static-gate false positive) stays silent — the LLM is the precision step."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Write", file="STATUS.md", content=HANDOFF_BODY, session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(RecordVerdict(record=False)))
    check("router: silent on record=False", nudge_record_durable(evt) is None)
    evt2 = mock_event("PostToolUse", tool="Write", file="STATUS.md", content=HANDOFF_BODY, session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_llm", stub_llm(RecordVerdict(record=True, kind="")))
    check("router: silent on empty/unknown kind", nudge_record_durable(evt2) is None)


def test_record_router_fails_closed_on_llm_error(monkeypatch, tmp_path) -> None:
    """A classifier error never crashes the nudge — it falls closed to silence."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Write", file="HANDOFF.md", content=HANDOFF_BODY, session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_llm", _llm_boom)
    check("router: fails closed on LLM error", nudge_record_durable(evt) is None)


def test_float_note_context_floats_doc(monkeypatch, tmp_path) -> None:
    """A kind=="doc" entry from `relevant` floats its when/verdict pointer and persists by doc id."""
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps(
        [doc_entry("d0cd0c00111", when="before touching the auth flow", drift="DRIFTED", title="Auth handoff", reasons=["dir"])]
    )
    mapping = {("relevant", "internal/api/auth.go", "--json"): payload}
    evt = mock_event("PostToolUse", tool="Read", file="internal/api/auth.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))

    result = float_note_context(evt)
    check("doc float: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("doc float: renders when trigger", "before touching the auth flow" in result.message, result.message)
        check("doc float: renders lowercased verdict", "[drifted]" in result.message, result.message)
        check("doc float: renders doc show hint", "cc-notes doc show d0cd0c0" in result.message, result.message)
        check("doc float: never leaks the body", "LONG_DOC_BODY" not in result.message, result.message)

    evt2 = mock_event("PostToolUse", tool="Read", file="internal/api/auth.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("doc float: re-read deduped by doc id -> None", float_note_context(evt2) is None)


def test_float_note_context_floats_log(monkeypatch, tmp_path) -> None:
    """A kind=="log" entry from `relevant` floats its `log show` pointer and persists by log id.

    A log is surfaced on read exactly like a doc, but it is an append-only journal:
    it renders only its title and a ``log show`` hint, never the entry chronology,
    and never a drift verdict (a log can't drift).
    """
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps([log_entry("105f00ba9c1", title="Auth rollout", reasons=["dir"])])
    mapping = {("relevant", "internal/api/auth.go", "--json"): payload}
    evt = mock_event("PostToolUse", tool="Read", file="internal/api/auth.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))

    result = float_note_context(evt)
    check("log float: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("log float: renders title", "Auth rollout" in result.message, result.message)
        check("log float: renders log show hint", "cc-notes log show 105f00b" in result.message, result.message)
        check("log float: no drift verdict", "[" not in result.message.split("Auth rollout", 1)[1], result.message)
        check("log float: never leaks entry text", "LOG_ENTRY_TEXT" not in result.message, result.message)

    evt2 = mock_event("PostToolUse", tool="Read", file="internal/api/auth.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("log float: re-read deduped by log id -> None", float_note_context(evt2) is None)


def test_surface_filter_single_skips_llm(monkeypatch, tmp_path) -> None:
    """A lone candidate surfaces directly — no model call is paid (a boom stub proves it)."""
    evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_llm", _llm_boom)
    kept = surface_filter(evt, [note_entry("only0001aaa", title="Sole")], touched="read")
    check("surface filter: single candidate bypasses LLM", [entry_payload(e)["id"] for e in kept] == ["only0001aaa"], repr(kept))


def test_surface_filter_trims_to_subset(monkeypatch, tmp_path) -> None:
    """With 2+ candidates the small LLM keeps only the ids it picks, preserving order."""
    evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)
    fresh = [note_entry("aaa0001xxx"), note_entry("bbb0002xxx"), note_entry("ccc0003xxx")]
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(SurfacePick(ids=["aaa0001xxx", "ccc0003xxx"])))
    kept = surface_filter(evt, fresh, touched="read")
    check("surface filter: keeps only picked subset", [entry_payload(e)["id"] for e in kept] == ["aaa0001xxx", "ccc0003xxx"], repr(kept))


def test_surface_filter_ignores_unknown_ids(monkeypatch, tmp_path) -> None:
    """An id the model returns that was never a candidate is ignored (intersection only)."""
    evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)
    fresh = [note_entry("aaa0001xxx"), note_entry("bbb0002xxx")]
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(SurfacePick(ids=["aaa0001xxx", "zzz9999zzz"])))
    kept = surface_filter(evt, fresh, touched="read")
    check("surface filter: drops ids not in the candidate set", [entry_payload(e)["id"] for e in kept] == ["aaa0001xxx"], repr(kept))


def test_surface_filter_fails_open(monkeypatch, tmp_path) -> None:
    """A classifier error surfaces EVERY candidate — the recall filter must never hide context."""
    evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)
    fresh = [note_entry("aaa0001xxx"), note_entry("bbb0002xxx")]
    monkeypatch.setattr(evt.ctx, "call_llm", _llm_boom)
    kept = surface_filter(evt, fresh, touched="read")
    check("surface filter: fails open to all candidates", [entry_payload(e)["id"] for e in kept] == ["aaa0001xxx", "bbb0002xxx"], repr(kept))


def test_float_note_context_multi_filters_but_judges_all(monkeypatch, tmp_path) -> None:
    """float_note_context surfaces only the LLM's pick, yet marks ALL fresh ids judged once/session.

    The re-read fires a fail-OPEN LLM stub: were the unpicked bbb0002xxx not marked judged on
    the first pass, it would resurface here, so the silent second call proves all fresh ids
    were marked before the filter ran.
    """
    monkeypatch.setattr(common.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = json.dumps(
        [note_entry("aaa0001xxx", title="Keep"), note_entry("bbb0002xxx", title="Drop"), note_entry("ccc0003xxx", title="Keep2")]
    )
    mapping = {("relevant", "x.go", "--json"): payload}
    evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(SurfacePick(ids=["aaa0001xxx", "ccc0003xxx"])))
    result = float_note_context(evt)
    check("multi float: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("multi float: surfaces picked", "aaa0001" in result.message and "ccc0003" in result.message, result.message)
        check("multi float: drops unpicked", "bbb0002" not in result.message, result.message)
    evt2 = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    monkeypatch.setattr(evt2.ctx, "call_llm", _llm_boom)
    check("multi float: re-read fully deduped to silence (unpicked bbb0002 was marked)", float_note_context(evt2) is None)


COMMIT_DIFF = (
    "commit deadsha000\n\n internal/api/client.go | 4 ++--\n 1 file changed\n\n"
    "@@ retry backoff @@\n-    cap := 10 * time.Second\n+    cap := 30 * time.Second\n"
)


def commit_event(tmp_path, monkeypatch, *, sha="deadsha000", verdict=None, diff=COMMIT_DIFF):
    """A commit event with rev-parse (git), the commit diff primitive, call_llm, and a sync CLI stubbed.

    The handler reads the sha via ``evt.ctx.git("rev-parse", "HEAD")`` for per-sha dedup, the patch
    via ``evt.ctx.diff(commit="HEAD")`` for the record-router, and then auto-syncs via
    ``evt.ctx.call_cli(["cc-notes", "sync"])`` — all stubbed. The recording ``call_cli`` answers
    ``cc-notes sync`` with success and is exposed on ``evt._sync_calls`` so a test can assert the
    sync ran (and how often).
    """
    evt = mock_event("PostToolUse", tool="Bash", command="git commit -m x", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "git", stub_git({("rev-parse", "HEAD"): sha}))
    monkeypatch.setattr(evt.ctx, "diff", lambda *a, **k: diff)
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(verdict if verdict is not None else RecordVerdict(record=False)))
    call, calls = recording_cli({("sync",): "ok"})
    monkeypatch.setattr(evt.ctx, "call_cli", call)
    evt._sync_calls = calls  # type: ignore[attr-defined]
    return evt


def test_commit_no_longer_says_run_sync(monkeypatch, tmp_path) -> None:
    """The commit reminder dropped the old 'cc-notes sync to share your refs' text: it auto-syncs instead.

    It still names the `cc-task:` trailer and, with the auto-sync stubbed to success, a real
    ``["cc-notes", "sync"]`` ran (the side-effect proof the inline harness can't make).
    """
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = commit_event(tmp_path, monkeypatch)
    result = nudge_commit_record(evt)
    check("commit: warns the reminder", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("commit: no longer says 'cc-notes sync to share your refs'", "cc-notes sync to share your refs" not in result.message, result.message)
        check("commit: names cc-task trailer", "cc-task:" in result.message, result.message)
        check("commit: confirms the auto-sync", "Synced cc-notes refs." in result.message, result.message)
        check("commit: no decision line when record=False", "capture it" not in result.message, result.message)
    check("commit: a cc-notes sync ran", _calls_of(evt._sync_calls, "sync") == [0], repr(evt._sync_calls))


def test_commit_routes_decision(monkeypatch, tmp_path) -> None:
    """A durable-decision verdict folds a `cc-notes note add` into the reminder, keeping the trailer line and the sync."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    verdict = RecordVerdict(record=True, kind="note", title="Backoff caps at 30s", area="internal/api", reasoning="server drops past 30s")
    evt = commit_event(tmp_path, monkeypatch, verdict=verdict)
    result = nudge_commit_record(evt)
    check("commit decision: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("commit decision: keeps the trailer reminder", "cc-task:" in result.message, result.message)
        check("commit decision: routes a note", "cc-notes note add" in result.message and '"Backoff caps at 30s"' in result.message, result.message)
        check("commit decision: cites reasoning", "server drops past 30s" in result.message, result.message)
        check("commit decision: still confirms the auto-sync", "Synced cc-notes refs." in result.message, result.message)
    check("commit decision: a cc-notes sync ran", _calls_of(evt._sync_calls, "sync") == [0], repr(evt._sync_calls))


def test_commit_only_routes_note_or_doc(monkeypatch, tmp_path) -> None:
    """A commit captures a decision, never a log or task — a log/task verdict drops to reminder only."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    result = nudge_commit_record(commit_event(tmp_path, monkeypatch, verdict=RecordVerdict(record=True, kind="log", title="x")))
    check("commit: log verdict yields no record line", result is not None and "capture it" not in (result.message or ""), repr(result))


def test_commit_dedup_per_sha(monkeypatch, tmp_path) -> None:
    """The same HEAD sha is judged once; a re-fire on that sha is silent, a new sha (amend) fires."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    first = commit_event(tmp_path, monkeypatch, sha="sha111")
    check("commit dedup: first fire warns", nudge_commit_record(first) is not None)
    check("commit dedup: same sha silent", nudge_commit_record(commit_event(tmp_path, monkeypatch, sha="sha111")) is None)
    check("commit dedup: a new sha fires", nudge_commit_record(commit_event(tmp_path, monkeypatch, sha="sha222")) is not None)


def test_commit_fails_safe_without_git(monkeypatch, tmp_path) -> None:
    """git unavailable (no sha, no diff) still fires the base reminder — only the suggestion drops."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Bash", command="git commit -m x", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "git", stub_git({}))  # rev-parse -> None (sha-less, dedup skipped, reminder fires)
    monkeypatch.setattr(evt.ctx, "diff", lambda *a, **k: None)  # no diff -> the classifier is never reached
    monkeypatch.setattr(evt.ctx, "call_llm", _llm_boom)  # unreachable: no diff means no classifier call
    call, _calls = recording_cli({("sync",): "ok"})
    monkeypatch.setattr(evt.ctx, "call_cli", call)
    result = nudge_commit_record(evt)
    check("commit fail-safe: still warns the reminder", result is not None and "cc-task:" in (result.message or ""), repr(result))


def test_commit_decision_llm_error_keeps_reminder(monkeypatch, tmp_path) -> None:
    """A diff is fetched but the classifier raises: the suggestion drops, the reminder stays."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = commit_event(tmp_path, monkeypatch)  # diff stub returns COMMIT_DIFF + a real sha
    monkeypatch.setattr(evt.ctx, "call_llm", _llm_boom)  # classifier raises AFTER the diff is fetched
    result = nudge_commit_record(evt)
    check("commit llm-error: still warns the reminder", result is not None and "cc-task:" in (result.message or ""), repr(result))
    check("commit llm-error: drops the decision line", result is not None and "capture it" not in (result.message or ""), repr(result))


def test_commit_fails_safe_on_git_timeout(monkeypatch, tmp_path) -> None:
    """A git timeout (which evt.ctx.git/diff don't swallow) still fires the reminder — only the suggestion drops."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")

    def boom(*_a, **_k):
        raise subprocess.TimeoutExpired(cmd="git", timeout=5)

    evt = commit_event(tmp_path, monkeypatch)  # the commit diff fetch times out (the jj-colocated git-fallback case)
    monkeypatch.setattr(evt.ctx, "diff", boom)
    result = nudge_commit_record(evt)
    check("commit diff-timeout: still warns the reminder", result is not None and "cc-task:" in (result.message or ""), repr(result))
    check("commit diff-timeout: drops the decision line", result is not None and "capture it" not in (result.message or ""), repr(result))

    evt2 = commit_event(tmp_path, monkeypatch)  # the rev-parse for per-sha dedup times out -> sha-less, reminder still fires
    monkeypatch.setattr(evt2.ctx, "git", boom)
    result2 = nudge_commit_record(evt2)
    check("commit rev-parse-timeout: still warns the reminder", result2 is not None and "cc-task:" in (result2.message or ""), repr(result2))


SAMPLE_PLAN = "# Plan\n\n## Approach\n1. Add the widget\n2. Wire it up\n\n## Tasks\n- build the gateway client\n"


def plan_event(tmp_path, monkeypatch, *, plan_path=None, inline=None, tasks=None):
    """An ExitPlanMode event with planFilePath/plan injected into tool_input and the LLM stubbed."""
    evt = mock_event("PostToolUse", tool="ExitPlanMode", session_dir=tmp_path)
    ti: dict = {}
    if plan_path is not None:
        ti["planFilePath"] = str(plan_path)
    if inline is not None:
        ti["plan"] = inline
    evt._raw["tool_input"] = ti
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(PlanTasks(tasks=tasks if tasks is not None else [])))
    return evt


def test_plan_teach_always_fires(monkeypatch, tmp_path) -> None:
    """ExitPlanMode with no readable plan still fires the native-vs-durable teach, no LLM call."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = plan_event(tmp_path, monkeypatch)
    monkeypatch.setattr(evt.ctx, "call_llm", _llm_boom)  # no plan text -> the extractor is never reached
    result = nudge_plan_tasks(evt)
    check("plan teach: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("plan teach: native-vs-durable line", "Native TaskCreate" in result.message, result.message)
        check("plan teach: names cc-notes task add", "cc-notes task add" in result.message, result.message)
        check("plan teach: no extracted header when none", "look like durable work" not in result.message, result.message)


def test_plan_extracts_tasks_from_file(monkeypatch, tmp_path) -> None:
    """A plan file is read and the LLM's durable items render as `cc-notes task add` lines."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    plan_path = tmp_path / "plan.md"
    plan_path.write_text(SAMPLE_PLAN, encoding="utf-8")
    tasks = [PlanTask(title="Build the gateway client", shared=True), PlanTask(title="Wire the widget", shared=False)]
    result = nudge_plan_tasks(plan_event(tmp_path, monkeypatch, plan_path=plan_path, tasks=tasks))
    check("plan extract: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("plan extract: shared item gets --backlog", 'cc-notes task add "Build the gateway client" --backlog' in result.message, result.message)
        check(
            "plan extract: branch item has no --backlog",
            'cc-notes task add "Wire the widget"' in result.message and 'cc-notes task add "Wire the widget" --backlog' not in result.message,
            result.message,
        )
        check("plan extract: the teach is still present", "Native TaskCreate" in result.message, result.message)


def test_plan_dedup_per_path(monkeypatch, tmp_path) -> None:
    """Re-approving the same plan file stays silent; a genuinely new plan path re-fires."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    plan_path = tmp_path / "plan.md"
    plan_path.write_text(SAMPLE_PLAN, encoding="utf-8")
    first = plan_event(tmp_path, monkeypatch, plan_path=plan_path, tasks=[PlanTask(title="X")])
    check("plan dedup: first fires", nudge_plan_tasks(first) is not None)
    second = plan_event(tmp_path, monkeypatch, plan_path=plan_path, tasks=[PlanTask(title="X")])
    check("plan dedup: same plan silent", nudge_plan_tasks(second) is None)
    other_path = tmp_path / "plan2.md"
    other_path.write_text(SAMPLE_PLAN, encoding="utf-8")
    third = plan_event(tmp_path, monkeypatch, plan_path=other_path, tasks=[PlanTask(title="X")])
    check("plan dedup: a new plan path re-fires", nudge_plan_tasks(third) is not None)


def test_plan_text_prefers_file_over_inline(tmp_path) -> None:
    """plan_text reads the authoritative file when planFilePath is present, else the inline plan."""
    plan_path = tmp_path / "p.md"
    plan_path.write_text("FROM FILE\n", encoding="utf-8")
    from_file = mock_event("PostToolUse", tool="ExitPlanMode")
    from_file._raw["tool_input"] = {"planFilePath": str(plan_path), "plan": "FROM INLINE"}
    check("plan_text: prefers the file", plan_text(from_file) == "FROM FILE")
    inline_only = mock_event("PostToolUse", tool="ExitPlanMode")
    inline_only._raw["tool_input"] = {"plan": "FROM INLINE"}
    check("plan_text: falls back to inline", plan_text(inline_only) == "FROM INLINE")
    none_evt = mock_event("PostToolUse", tool="ExitPlanMode")
    none_evt._raw["tool_input"] = {}
    check("plan_text: None when neither present", plan_text(none_evt) is None)


def test_plan_task_commands_caps_and_skips_blank(monkeypatch, tmp_path) -> None:
    """Extraction caps at five items, drops blank titles; no plan text -> []."""
    evt = mock_event("PostToolUse", tool="ExitPlanMode", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(PlanTasks(tasks=[PlanTask(title=f"t{i}") for i in range(8)])))
    check("plan cmds: caps at five", len(plan_task_commands(evt, "plan")) == 5)
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(PlanTasks(tasks=[PlanTask(title="real"), PlanTask(title="   "), PlanTask(title="real2")])))
    cmds = plan_task_commands(evt, "plan")
    check("plan cmds: drops blank titles", cmds == ['cc-notes task add "real"', 'cc-notes task add "real2"'], repr(cmds))
    check("plan cmds: empty text -> []", plan_task_commands(evt, None) == [])


def write_memory(tmp_path: Path, slug: str, mtype: str, description: str, body: str) -> Path:
    """Write a realistic cc-pool memory file on disk and return its path.

    The path has the shape the MemoryWrite gate keys on — a ``<slug>.md`` under a
    ``memory/`` dir inside a ``.cc-pool`` tree — and the frontmatter carries the
    ``node_type`` sibling that the type parse must not confuse with ``type``.
    """
    mempath = tmp_path / ".cc-pool" / "accounts" / "a" / "projects" / "-p" / "memory" / f"{slug}.md"
    mempath.parent.mkdir(parents=True, exist_ok=True)
    mempath.write_text(
        f"---\nname: {slug}\ndescription: {description}\nmetadata:\n"
        f"  node_type: memory\n  type: {mtype}\n  originSessionId: sess-1\n---\n\n{body}\n",
        encoding="utf-8",
    )
    return mempath


def mirror_cli(list_payload: str = "[]", add_payload: str = '{"id": "abc1234def0"}'):
    """A recording call_cli stub for the memory mirror, returning canned note output.

    Dispatches on the ``note <verb>`` pair: ``list`` yields ``list_payload``, ``add``
    yields ``add_payload``, ``edit`` yields ``""``. Records every argv so a test can
    assert exactly which commands the handler issued (and which it skipped).
    """
    calls: list[list[str]] = []

    def _call(args, *, input=None, timeout=30, env=None, throw=True):
        calls.append(list(args))
        verb = tuple(args[1:3])
        if verb == ("note", "list"):
            return list_payload
        if verb == ("note", "add"):
            return add_payload
        if verb == ("note", "edit"):
            return ""
        if not throw:
            return None
        raise FileNotFoundError(args[0])

    return _call, calls


def test_parse_memory_file(tmp_path) -> None:
    """Frontmatter parse pulls metadata.type (not node_type), an unquoted description, a stripped body."""
    p = tmp_path / "m.md"
    p.write_text(
        '---\nname: foo\ndescription: "Quoted: with colon"\nmetadata:\n'
        "  node_type: memory\n  type: project\n  originSessionId: s\n---\n\nBody line one.\n\nBody line two.\n",
        encoding="utf-8",
    )
    parsed = parse_memory_file(p)
    check("parse_memory: not None", parsed is not None, repr(parsed))
    if parsed:
        check("parse_memory: type from metadata, not node_type", parsed.type == "project", parsed.type)
        check("parse_memory: description unquoted", parsed.title == "Quoted: with colon", parsed.title)
        check("parse_memory: body stripped, internals kept", parsed.body == "Body line one.\n\nBody line two.", repr(parsed.body))
    check("parse_memory: missing file -> None", parse_memory_file(tmp_path / "nope.md") is None)
    nofront = tmp_path / "plain.md"
    nofront.write_text("# Just markdown\nno frontmatter\n", encoding="utf-8")
    check("parse_memory: no frontmatter -> None", parse_memory_file(nofront) is None)


def test_memory_write_condition() -> None:
    """The path gate matches a memory slug file and nothing else — index, source, or non-.cc-pool .md."""
    cond = MemoryWrite()

    def fires(path: str) -> bool:
        return cond.check(mock_event("PostToolUse", tool="Write", file=path))

    check("MemoryWrite: matches a memory slug file", fires("/u/.cc-pool/accounts/a/projects/-p/memory/my-fact.md"))
    check("MemoryWrite: skips the MEMORY.md index", not fires("/u/.cc-pool/accounts/a/projects/-p/memory/MEMORY.md"))
    check("MemoryWrite: skips a normal source file", not fires("internal/store/store.go"))
    check("MemoryWrite: skips a .md outside a memory dir", not fires("/u/.cc-pool/accounts/a/projects/-p/notes/x.md"))
    check("MemoryWrite: skips a memory dir outside .cc-pool", not fires("/u/code/memory/x.md"))


def test_mirror_creates_note(monkeypatch, tmp_path) -> None:
    """First write of a feedback memory issues one note add, keyed and typed, no edit."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    mem = write_memory(tmp_path, "retry-ceiling", "feedback", "Retry backoff caps at 30s", "The server drops past 30s.")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    call, calls = mirror_cli(list_payload="[]")
    monkeypatch.setattr(evt.ctx, "call_cli", call)

    result = mirror_memory_to_note(evt)
    check("mirror create: warns 'created'", result is not None and result.action is Action.warn and "created" in (result.message or ""), repr(result))
    adds = [c for c in calls if tuple(c[1:3]) == ("note", "add")]
    check("mirror create: issues exactly one note add", len(adds) == 1, repr(calls))
    if adds:
        a = adds[0]
        check("mirror create: keys by slug tag", "memory:retry-ceiling" in a, repr(a))
        check("mirror create: tags type", "memory-type:feedback" in a, repr(a))
        check("mirror create: carries generic memory tag", "memory" in a, repr(a))
        check("mirror create: carries the stripped body", "--body=The server drops past 30s." in a, repr(a))
        check("mirror create: title is the positional after --", a[-2] == "--" and a[-1] == "Retry backoff caps at 30s", repr(a))
    check("mirror create: issues no edit", not any(tuple(c[1:3]) == ("note", "edit") for c in calls), repr(calls))


def test_mirror_updates_note(monkeypatch, tmp_path) -> None:
    """A later write with a changed body edits the SAME note id in place, never adds."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    mem = write_memory(tmp_path, "retry-ceiling", "feedback", "Retry ceiling", "v2 body")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    existing = json.dumps([{"id": "abc1234def0", "title": "Retry ceiling", "body": "v1 body", "tags": ["memory", "memory:retry-ceiling"]}])
    call, calls = mirror_cli(list_payload=existing)
    monkeypatch.setattr(evt.ctx, "call_cli", call)

    result = mirror_memory_to_note(evt)
    check("mirror update: warns 'updated'", result is not None and "updated" in (result.message or ""), repr(result))
    edits = [c for c in calls if tuple(c[1:3]) == ("note", "edit")]
    check("mirror update: one edit on the same id", len(edits) == 1 and edits[0][3] == "abc1234def0", repr(calls))
    if edits:
        check("mirror update: carries the new body", "--body=v2 body" in edits[0], repr(edits[0]))
        check("mirror update: carries the title", "--title=Retry ceiling" in edits[0], repr(edits[0]))
    check("mirror update: issues no add", not any(tuple(c[1:3]) == ("note", "add") for c in calls), repr(calls))


def test_mirror_skips_unchanged(monkeypatch, tmp_path) -> None:
    """When the existing note's title and body already match, only the lookup runs — no edit churn."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    mem = write_memory(tmp_path, "retry-ceiling", "feedback", "Retry ceiling", "same body")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    existing = json.dumps([{"id": "abc1234def0", "title": "Retry ceiling", "body": "same body"}])
    call, calls = mirror_cli(list_payload=existing)
    monkeypatch.setattr(evt.ctx, "call_cli", call)

    check("mirror skip: silent when unchanged", mirror_memory_to_note(evt) is None)
    check("mirror skip: only a list lookup issued", [tuple(c[1:3]) for c in calls] == [("note", "list")], repr(calls))


def test_mirror_skips_user_type(monkeypatch, tmp_path) -> None:
    """A user (who-you-are) memory is repo-irrelevant — no note, and not even a lookup."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    mem = write_memory(tmp_path, "who-i-am", "user", "Yasyf prefers Go", "Some user fact.")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    call, calls = mirror_cli()
    monkeypatch.setattr(evt.ctx, "call_cli", call)

    check("mirror user-skip: silent", mirror_memory_to_note(evt) is None)
    check("mirror user-skip: issues no cc-notes calls at all", calls == [], repr(calls))


def test_clamp_title() -> None:
    """clamp_title caps a title at 256 UTF-8 bytes on a rune boundary, passing short ones through."""
    check("clamp: cap constant is 256", MAX_TITLE_BYTES == 256)
    short = "A short handle"
    check("clamp: short title unchanged", clamp_title(short) == short)
    at_cap = "x" * 256
    check("clamp: exactly 256 bytes unchanged", clamp_title(at_cap) == at_cap and len(clamp_title(at_cap).encode()) == 256)
    over = clamp_title("x" * 300)
    check("clamp: over-cap ASCII clamps to 256 bytes", over == "x" * 256 and len(over.encode()) == 256, repr(over))
    # A CJK rune is 3 UTF-8 bytes; 100 of them is 300 bytes. The cap must land on a rune
    # boundary (256 // 3 == 85 whole runes = 255 bytes), never a partial rune.
    cjk = clamp_title("包" * 100)
    check("clamp: CJK clamps on a rune boundary, no partial rune", cjk == "包" * 85 and len(cjk.encode()) == 255, f"{len(cjk.encode())} bytes, {len(cjk)} runes")


def test_mirror_clamps_long_title(monkeypatch, tmp_path) -> None:
    """A memory description over 256 bytes is clamped before shelling `note add`, so the mirror still fires.

    The Go CLI rejects an over-cap title (exit 2) and run_cc_notes fails closed, so without the
    clamp a long description would silently stop mirroring — this proves the positional title is
    capped and equals clamp_title of the parsed description.
    """
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    long_desc = "A durable handoff description long enough to blow past the cap " * 6  # ~372 bytes
    mem = write_memory(tmp_path, "long-fact", "feedback", long_desc, "body text")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    call, calls = mirror_cli(list_payload="[]")
    monkeypatch.setattr(evt.ctx, "call_cli", call)

    result = mirror_memory_to_note(evt)
    check("clamp mirror: warns 'created'", result is not None and "created" in (result.message or ""), repr(result))
    adds = [c for c in calls if tuple(c[1:3]) == ("note", "add")]
    check("clamp mirror: issues exactly one note add", len(adds) == 1, repr(calls))
    if adds:
        parsed = parse_memory_file(mem)
        title_arg = adds[0][-1]
        check("clamp mirror: title is the positional after --", adds[0][-2] == "--", repr(adds[0]))
        check("clamp mirror: positional title is <= 256 bytes", len(title_arg.encode()) <= 256, f"{len(title_arg.encode())} bytes")
        check("clamp mirror: title equals clamp_title(parsed description)", parsed is not None and title_arg == clamp_title(parsed.title), repr(title_arg))


def merge_event(tmp_path, monkeypatch, *, branch="feature/x", cli=None):
    """A `git merge` event with the branch-name probe stubbed; ``cli`` is the recording call_cli to use.

    auto_reconcile reads the current branch via ``git rev-parse --abbrev-ref HEAD``; ``branch`` seeds
    that probe (pass ``"HEAD"`` for a detached head). When ``cli`` is omitted a recording stub that
    answers ``reconcile``+``sync`` with success is installed; the recording ``calls`` list is exposed
    on ``evt._cli_calls`` for order assertions.
    """
    evt = mock_event("PostToolUse", tool="Bash", command="git merge feature/x", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "git", stub_git({("rev-parse", "--abbrev-ref", "HEAD"): branch}))
    if cli is None:
        cli, calls = recording_cli({("reconcile", "--into", branch): "ok", ("sync",): "ok"})
    else:
        calls = []
    monkeypatch.setattr(evt.ctx, "call_cli", cli)
    evt._cli_calls = calls  # type: ignore[attr-defined]
    return evt


def claim_event(tmp_path, monkeypatch, *, cli=None):
    """A `cc-notes task claim` event with a recording sync CLI; exposes ``evt._cli_calls``."""
    evt = mock_event("PostToolUse", tool="Bash", command="cc-notes task claim abc1234", session_dir=tmp_path)
    if cli is None:
        cli, calls = recording_cli({("sync",): "ok"})
    else:
        calls = []
    monkeypatch.setattr(evt.ctx, "call_cli", cli)
    evt._cli_calls = calls  # type: ignore[attr-defined]
    return evt


def _rejected(stderr: str) -> subprocess.CalledProcessError:
    return subprocess.CalledProcessError(1, ["cc-notes", "sync"], stderr=stderr)


def test_auto_sync_once_per_turn(monkeypatch, tmp_path) -> None:
    """Two side-effect handlers firing in ONE turn (shared session_dir) issue exactly ONE cc-notes sync.

    The commit handler and the claim handler both auto-sync, but ``should_autosync`` claims a single
    per-turn token, so the second handler's sync is suppressed. They share one recording call_cli so
    the assertion is a hard count on ``["cc-notes", "sync"]`` invocations across both fires.
    """
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    cli, calls = recording_cli({("sync",): "ok"})

    commit = commit_event(tmp_path, monkeypatch)
    monkeypatch.setattr(commit.ctx, "call_cli", cli)  # share the single recorder across both handlers
    commit_result = nudge_commit_record(commit)
    check("once-per-turn: commit handler fires", commit_result is not None, repr(commit_result))

    claim = claim_event(tmp_path, monkeypatch, cli=cli)
    claim_result = nudge_claim(claim)
    check("once-per-turn: claim handler fires", claim_result is not None, repr(claim_result))

    check("once-per-turn: exactly one cc-notes sync across both handlers", len(_calls_of(calls, "sync")) == 1, repr(calls))
    check("once-per-turn: only the first handler confirmed the sync", "Synced cc-notes refs." in (commit_result.message or ""), repr(commit_result))
    check("once-per-turn: the second handler did not re-confirm", "Synced cc-notes refs." not in (claim_result.message or ""), repr(claim_result))


def test_auto_sync_confirms_on_success(monkeypatch, tmp_path) -> None:
    """A successful sync makes the side-effect handler's warn confirm with 'Synced cc-notes refs.'."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = claim_event(tmp_path, monkeypatch)
    result = nudge_claim(evt)
    check("auto-sync success: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("auto-sync success: confirms the sync", "Synced cc-notes refs." in result.message, result.message)
    check("auto-sync success: a cc-notes sync ran", _calls_of(evt._cli_calls, "sync") == [0], repr(evt._cli_calls))


def test_auto_sync_silent_on_no_remote(monkeypatch, tmp_path) -> None:
    """A no-remote repo (CalledProcessError stderr 'remote not configured') is benign — no failure line."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    cli, _calls = recording_cli(raises={("sync",): _rejected("remote not configured\n")})
    evt = claim_event(tmp_path, monkeypatch, cli=cli)
    result = nudge_claim(evt)
    check("no-remote: still warns the lease teach", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("no-remote: no sync-failure line", "cc-notes sync failed" not in result.message, result.message)
        check("no-remote: no false success confirmation", "Synced cc-notes refs." not in result.message, result.message)
    check("auto-sync no-remote line is None", do_sync(claim_event(tmp_path, monkeypatch, cli=cli)) is None)


def test_auto_sync_warns_on_genuine_failure(monkeypatch, tmp_path) -> None:
    """A genuine push rejection (non-fast-forward) surfaces 'cc-notes sync failed' so the agent retries."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    cli, _calls = recording_cli(raises={("sync",): _rejected("! [rejected] non-fast-forward\n")})
    evt = claim_event(tmp_path, monkeypatch, cli=cli)
    result = nudge_claim(evt)
    check("rejected: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("rejected: surfaces the sync failure", "cc-notes sync failed" in result.message, result.message)
    line = do_sync(claim_event(tmp_path, monkeypatch, cli=cli))
    check("rejected: do_sync returns the failure line", line is not None and "cc-notes sync failed" in line, repr(line))


def test_auto_sync_silent_on_timeout(monkeypatch, tmp_path) -> None:
    """A sync that times out is silent — a transient hang must not fabricate a failure line."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    cli, _calls = recording_cli(raises={("sync",): subprocess.TimeoutExpired(cmd="cc-notes sync", timeout=15)})
    evt = claim_event(tmp_path, monkeypatch, cli=cli)
    result = nudge_claim(evt)
    check("timeout: still warns the lease teach", result is not None, repr(result))
    if result and result.message:
        check("timeout: no sync-failure line", "cc-notes sync failed" not in result.message, result.message)
    check("timeout: do_sync line is None", do_sync(claim_event(tmp_path, monkeypatch, cli=cli)) is None)


def test_auto_sync_silent_on_missing_binary(monkeypatch, tmp_path) -> None:
    """A FileNotFoundError (binary vanished mid-session) is silent — FileNotFoundError is an OSError."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    cli, _calls = recording_cli(raises={("sync",): FileNotFoundError("cc-notes")})
    evt = claim_event(tmp_path, monkeypatch, cli=cli)
    result = nudge_claim(evt)
    check("missing-binary: still warns the lease teach", result is not None, repr(result))
    if result and result.message:
        check("missing-binary: no sync-failure line", "cc-notes sync failed" not in result.message, result.message)
    check("missing-binary: do_sync line is None", do_sync(claim_event(tmp_path, monkeypatch, cli=cli)) is None)


def test_reconcile_after_merge(monkeypatch, tmp_path) -> None:
    """After a merge, reconcile carries the branch's tasks then sync pushes them — in that order."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = merge_event(tmp_path, monkeypatch, branch="feature/x")
    result = reconcile_after_merge(evt)
    check("reconcile: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check(
            "reconcile: names the branch and confirms the sync",
            result.message == "Reconciled merged tasks onto feature/x. Synced cc-notes refs.",
            repr(result.message),
        )
    recon = _calls_of(evt._cli_calls, "reconcile", "--into", "feature/x")
    sync = _calls_of(evt._cli_calls, "sync")
    check("reconcile: a reconcile ran", recon == [0], repr(evt._cli_calls))
    check("reconcile: a sync ran AFTER the reconcile", sync == [1] and sync[0] > recon[0], repr(evt._cli_calls))


def test_reconcile_surfaces_sync_failure(monkeypatch, tmp_path) -> None:
    """A merge reconciles locally but a genuine push rejection rides along — never a false 'synced'."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    cli, calls = recording_cli(
        {("reconcile", "--into", "feature/x"): "ok"},
        raises={("sync",): _rejected("! [rejected] non-fast-forward\n")},
    )
    evt = merge_event(tmp_path, monkeypatch, branch="feature/x", cli=cli)
    result = reconcile_after_merge(evt)
    check("reconcile-syncfail: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("reconcile-syncfail: confirms the reconcile", "Reconciled merged tasks onto feature/x" in result.message, result.message)
        check("reconcile-syncfail: surfaces the sync failure", "cc-notes sync failed" in result.message, result.message)
        check("reconcile-syncfail: no false sync confirmation", "Synced cc-notes refs." not in result.message, result.message)
    check("reconcile-syncfail: reconcile then sync both ran", _calls_of(calls, "reconcile", "--into", "feature/x") == [0] and _calls_of(calls, "sync") == [1], repr(calls))


def test_reconcile_no_remote_omits_sync_claim(monkeypatch, tmp_path) -> None:
    """No remote: reconcile still confirms locally, but the message makes no (false) sync claim."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    cli, calls = recording_cli(
        {("reconcile", "--into", "feature/x"): "ok"},
        raises={("sync",): _rejected("remote not configured\n")},
    )
    evt = merge_event(tmp_path, monkeypatch, branch="feature/x", cli=cli)
    result = reconcile_after_merge(evt)
    check("reconcile-noremote: warns the reconcile", result is not None, repr(result))
    if result and result.message:
        check("reconcile-noremote: confirms reconcile only", result.message == "Reconciled merged tasks onto feature/x.", repr(result.message))
    check("reconcile-noremote: a sync was attempted", _calls_of(calls, "sync") == [1], repr(calls))


def test_reconcile_detached_head_silent(monkeypatch, tmp_path) -> None:
    """A detached HEAD (rev-parse returns 'HEAD') reconciles nothing and syncs nothing — None."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = merge_event(tmp_path, monkeypatch, branch="HEAD")
    check("detached: handler returns None", reconcile_after_merge(evt) is None)
    check("detached: no reconcile ran", _calls_of(evt._cli_calls, "reconcile", "--into", "HEAD") == [], repr(evt._cli_calls))
    check("detached: no sync ran", _calls_of(evt._cli_calls, "sync") == [], repr(evt._cli_calls))


def test_reconcile_failure_silent(monkeypatch, tmp_path) -> None:
    """A reconcile that fails closed (run_cc_notes -> None) suppresses the warn and never syncs."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    # No mapping entry for reconcile -> run_cc_notes (throw=False) returns None; sync would map to "ok"
    # if it were reached, so a missing sync proves the early-return before the sync.
    cli, calls = recording_cli({("sync",): "ok"})
    evt = merge_event(tmp_path, monkeypatch, branch="feature/x", cli=cli)
    check("reconcile-fail: handler returns None", reconcile_after_merge(evt) is None)
    check("reconcile-fail: a reconcile was attempted", _calls_of(calls, "reconcile", "--into", "feature/x") == [0], repr(calls))
    check("reconcile-fail: no sync ran after the failed reconcile", _calls_of(calls, "sync") == [], repr(calls))


def test_claim_keeps_renew_teach_and_syncs(monkeypatch, tmp_path) -> None:
    """The claim nudge keeps its lease-upkeep teaching (renew, --steal) AND auto-syncs the new claim."""
    monkeypatch.setattr(common.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    evt = claim_event(tmp_path, monkeypatch)
    result = nudge_claim(evt)
    check("claim: warns", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("claim: teaches renew", "renew" in result.message, result.message)
        check("claim: teaches --steal for an expired hold", "--steal" in result.message, result.message)
        check("claim: confirms the auto-sync", "Synced cc-notes refs." in result.message, result.message)
    check("claim: a cc-notes sync ran", _calls_of(evt._cli_calls, "sync") == [0], repr(evt._cli_calls))


def test_pack_loads_under_discover_pack() -> None:
    """The PRODUCTION import path loads the relative-import pack without raising and registers handlers.

    ``discover_pack`` imports every ``*.py`` under the pack dir into a synthesized package, driving the
    same relative-import resolution the real Claude Code runtime uses. A broken split (a bad ``from
    .common import`` or a missing symbol) would surface here as a missing handler in the registry — the
    truest guard for the file split. Asserting on registered handler NAMES (not identity) is robust to
    ``discover_pack`` creating distinct module/function objects under its own package namespace.
    """
    from captain_hook.app import _state
    from captain_hook.loader import discover_pack

    pack_dir = Path(__file__).parents[1]  # plugin/hooks
    before_count = len(_state.hooks)  # the list grows by one per registered hook (count, not name-set)
    try:
        discover_pack("cc-notes", pack_dir)
    except Exception as e:  # noqa: BLE001 — a failed production import is exactly the regression we guard
        check("discover_pack: loads the pack without raising", False, f"{type(e).__name__}: {e}")
        return
    check("discover_pack: loads the pack without raising", True)
    # The split's bare ManyNativeTasks nudge() has no handler fn (declarative), so it registers
    # without a name; the named @on handlers must all appear by name.
    registered = _state.hooks[before_count:]
    names = {h.name for h in registered}
    expected = {
        "float_session_tasks",
        "prompt_install_cc_notes",
        "float_note_context",
        "check_note_staleness",
        "nudge_record_durable",
        "nudge_record_evidence",
        "nudge_ephemeral_record_reference",
        "nudge_plan_tasks",
        "mirror_memory_to_note",
        "nudge_commit_record",
        "reconcile_after_merge",
        "nudge_claim",
    }
    missing = expected - names
    check("discover_pack: every cc-notes handler registered", not missing, f"missing handlers: {sorted(missing)}; got={sorted(names)}")
    check(
        "discover_pack: it registered the full pack (12 named @on handlers + the bare many-native-tasks nudge)",
        len(registered) >= len(expected) + 1,
        f"registered {len(registered)} hooks this pass: {sorted(h.name for h in registered)}",
    )


class MonkeyPatch:
    """Minimal monkeypatch supporting setattr with automatic teardown."""

    def __init__(self) -> None:
        self._undo: list = []

    def setattr(self, target, name, value) -> None:
        self._undo.append((target, name, getattr(target, name)))
        setattr(target, name, value)

    def teardown(self) -> None:
        for target, name, old in reversed(self._undo):
            setattr(target, name, old)


def main() -> int:
    import inspect
    import tempfile

    tests = [fn for name, fn in sorted(globals().items()) if name.startswith("test_") and callable(fn)]
    for fn in tests:
        print(f"{fn.__name__}:")
        params = inspect.signature(fn).parameters
        mp = MonkeyPatch()
        kwargs = {}
        tmp = None
        if "monkeypatch" in params:
            kwargs["monkeypatch"] = mp
        if "tmp_path" in params:
            tmp = tempfile.TemporaryDirectory()
            kwargs["tmp_path"] = Path(tmp.name)
        try:
            fn(**kwargs)
        finally:
            mp.teardown()
            if tmp:
                tmp.cleanup()

    print()
    if FAILURES:
        print(f"{len(FAILURES)} helper unit test(s) FAILED: {FAILURES}")
        return 1
    print("all helper unit tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
