# /// script
# requires-python = ">=3.13"
# dependencies = ["capt-hook>=3.18.0", "pydantic>=2"]
# ///
"""Direct unit tests for the cc-notes capt-hook pack's pure helpers and handlers.

The inline ``tests={...}`` on each hook prove environment-stable behavior
(non-matching tools Allow). These tests cover what the inline harness cannot make
deterministic: the pure render and filter helpers, both gate branches (binary
present opens it, binary absent fails it closed), and a firing handler end to end
with stubbed CLI output. They mock the gate's one true external, ``shutil.which``,
rather than the condition object, so the real ``CcNotesAvailable`` logic runs
under controlled inputs.

Run with the same toolchain the inline tests use::

    uv run plugin/hooks/tests/test_cc_notes.py
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

import cc_notes
from cc_notes import (
    DurableInternalWrite,
    FloatedNotes,
    HandoffVerdict,
    MemoryWrite,
    StaleChecked,
    cap_and_render_tasks,
    check_note_staleness,
    dedup_against_ids,
    entry_payload,
    filter_drifted,
    float_note_context,
    float_session_tasks,
    mirror_memory_to_note,
    nudge_store_handoff_as_doc,
    parse_memory_file,
    parse_relevant,
    parse_tasks,
    prompt_install_cc_notes,
    render_log_line,
    render_note_lines,
    render_task_line,
    run_cc_notes,
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


def test_dedup_against_ids() -> None:
    entries = [note_entry("aaa1111"), note_entry("bbb2222"), note_entry("ccc3333")]
    kept = dedup_against_ids(entries, ["bbb2222"])
    check("dedup_against_ids: drops seen, keeps order", [e["note"]["id"] for e in kept] == ["aaa1111", "ccc3333"], repr(kept))
    check("dedup_against_ids: empty seen keeps all", dedup_against_ids(entries, []) == entries)
    check("dedup_against_ids: all seen -> []", dedup_against_ids(entries, ["aaa1111", "bbb2222", "ccc3333"]) == [])


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


def stub_llm(verdict: HandoffVerdict):
    """Build a call_llm stub returning a fixed HandoffVerdict for any prompt.

    Mirrors stub_cli: the test monkeypatches it onto ``evt.ctx.call_llm``. The
    handler passes ``response_model=HandoffVerdict`` and the real backend parses
    the reply into that model, so the stub returns an already-built instance.
    """

    def _call(template, *args, **kwargs):
        return verdict

    return _call


def _llm_must_not_run(template, *args, **kwargs):
    """A call_llm stub that fails loudly — proves the pre-gate skipped the paid call."""
    raise AssertionError("call_llm was reached for a pre-gated write")


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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: None)

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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")

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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    branch = [{"id": f"branch{i:02d}aaa", "status": "in_progress", "title": f"b{i}", "assignee": "me"} for i in range(3)]
    backlog = [{"id": f"backlog{i:02d}b", "status": "open", "title": f"k{i}"} for i in range(6)]
    mapping = {
        ("task", "list", "--json"): cc_notes.json.dumps(branch),
        ("task", "list", "--backlog", "--json"): cc_notes.json.dumps(backlog),
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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
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

    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: None)
    check("install nudge: gate opens when binary absent", matches_conditions(_spec_for(prompt_install_cc_notes), evt))

    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = cc_notes.json.dumps([note_entry("deadbeef000", drift=None, title="Schema", reasons=["dir"])])
    mapping = {("relevant", "internal/store/store.go", "--json"): payload}

    evt = mock_event("PostToolUse", tool="Read", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_cli", stub_cli(mapping))
    first = float_note_context(evt)
    check("note floater: first read warns", first is not None and first.action is Action.warn, repr(first))
    check("note floater: persisted id", evt.ctx.session.load(FloatedNotes).ids == ["deadbeef000"])

    evt2 = mock_event("PostToolUse", tool="Read", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("note floater: second read deduped -> None", float_note_context(evt2) is None)


def test_check_note_staleness_drift_only(monkeypatch, tmp_path) -> None:
    """Only drifted notes prompt reconciliation; fresh ones are ignored; dedup holds."""
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = cc_notes.json.dumps(
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
    check("staleness: persisted only drifted id", evt.ctx.session.load(StaleChecked).ids == ["stale000bbb"])

    evt2 = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("staleness: re-edit deduped -> None", check_note_staleness(evt2) is None)


def test_check_note_staleness_all_fresh_silent(monkeypatch, tmp_path) -> None:
    """An edit near only-fresh notes prompts nothing."""
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = cc_notes.json.dumps([note_entry("fresh000aaa", drift=None)])
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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = cc_notes.json.dumps(
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
    check("staleness doc: persisted by doc id", evt.ctx.session.load(StaleChecked).ids == ["drifteddoc01"], repr(evt.ctx.session.load(StaleChecked).ids))

    evt2 = mock_event("PostToolUse", tool="Edit", file="internal/store/store.go", session_dir=tmp_path)
    monkeypatch.setattr(evt2.ctx, "call_cli", stub_cli(mapping))
    check("staleness doc: re-edit deduped -> None", check_note_staleness(evt2) is None)


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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
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


def test_handoff_nudge_fires_on_internal(monkeypatch, tmp_path) -> None:
    """A long internal-handoff .md classified is_handoff=True warns toward `cc-notes doc add`.

    The cheap pre-gate passes (long, non-exempt .md) and the stubbed classifier
    returns a handoff verdict, so the handler seeds the suggested command from the
    verdict's title/when/area.
    """
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Write", file="HANDOFF.md", content=HANDOFF_BODY, session_dir=tmp_path)
    verdict = HandoffVerdict(is_handoff=True, title="Auth cutover handoff", when="resuming the auth cutover", area="internal/api")
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(verdict))

    result = nudge_store_handoff_as_doc(evt)
    check("handoff nudge: warns on internal handoff", result is not None and result.action is Action.warn, repr(result))
    if result and result.message:
        check("handoff nudge: names `cc-notes doc add`", "cc-notes doc add" in result.message, result.message)
        check("handoff nudge: carries --when", "--when" in result.message, result.message)
        check("handoff nudge: uses classifier title", '"Auth cutover handoff"' in result.message, result.message)
        check("handoff nudge: uses classifier when text", '--when "resuming the auth cutover"' in result.message, result.message)
        check("handoff nudge: uses classifier dir", "--dir internal/api" in result.message, result.message)
        check("handoff nudge: explains auto-surfacing", "cc-notes relevant" in result.message, result.message)


def test_handoff_nudge_silent_on_public(monkeypatch, tmp_path) -> None:
    """A long .md classified is_handoff=False (genuinely public docs) stays silent."""
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Write", file="GUIDE.md", content=HANDOFF_BODY, session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_llm", stub_llm(HandoffVerdict(is_handoff=False)))
    check("handoff nudge: silent on public doc -> None", nudge_store_handoff_as_doc(evt) is None)


def test_handoff_nudge_exempt_path_skips_llm(monkeypatch, tmp_path) -> None:
    """An exempt name (README.md) is pre-gated out — call_llm is NEVER reached.

    The stub raises if called, so a passing test proves the paid classifier never
    runs for an obviously-public file even when its body is long.
    """
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    evt = mock_event("PostToolUse", tool="Write", file="README.md", content=HANDOFF_BODY, session_dir=tmp_path)
    monkeypatch.setattr(evt.ctx, "call_llm", _llm_must_not_run)
    try:
        check("handoff nudge: exempt README.md -> None (LLM never called)", nudge_store_handoff_as_doc(evt) is None)
    except AssertionError as raised:
        check("handoff nudge: exempt README.md -> None (LLM never called)", False, f"call_llm ran: {raised}")


def test_float_note_context_floats_doc(monkeypatch, tmp_path) -> None:
    """A kind=="doc" entry from `relevant` floats its when/verdict pointer and persists by doc id."""
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = cc_notes.json.dumps(
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
    check("doc float: persists by doc id", evt.ctx.session.load(FloatedNotes).ids == ["d0cd0c00111"], repr(evt.ctx.session.load(FloatedNotes).ids))


def test_float_note_context_floats_log(monkeypatch, tmp_path) -> None:
    """A kind=="log" entry from `relevant` floats its `log show` pointer and persists by log id.

    A log is surfaced on read exactly like a doc, but it is an append-only journal:
    it renders only its title and a ``log show`` hint, never the entry chronology,
    and never a drift verdict (a log can't drift).
    """
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: "/usr/bin/cc-notes")
    payload = cc_notes.json.dumps([log_entry("105f00ba9c1", title="Auth rollout", reasons=["dir"])])
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
    check("log float: persists by log id", evt.ctx.session.load(FloatedNotes).ids == ["105f00ba9c1"], repr(evt.ctx.session.load(FloatedNotes).ids))


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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _n: "/usr/bin/cc-notes")
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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    mem = write_memory(tmp_path, "retry-ceiling", "feedback", "Retry ceiling", "v2 body")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    existing = cc_notes.json.dumps([{"id": "abc1234def0", "title": "Retry ceiling", "body": "v1 body", "tags": ["memory", "memory:retry-ceiling"]}])
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
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    mem = write_memory(tmp_path, "retry-ceiling", "feedback", "Retry ceiling", "same body")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    existing = cc_notes.json.dumps([{"id": "abc1234def0", "title": "Retry ceiling", "body": "same body"}])
    call, calls = mirror_cli(list_payload=existing)
    monkeypatch.setattr(evt.ctx, "call_cli", call)

    check("mirror skip: silent when unchanged", mirror_memory_to_note(evt) is None)
    check("mirror skip: only a list lookup issued", [tuple(c[1:3]) for c in calls] == [("note", "list")], repr(calls))


def test_mirror_skips_user_type(monkeypatch, tmp_path) -> None:
    """A user (who-you-are) memory is repo-irrelevant — no note, and not even a lookup."""
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _n: "/usr/bin/cc-notes")
    mem = write_memory(tmp_path, "who-i-am", "user", "Yasyf prefers Go", "Some user fact.")
    evt = mock_event("PostToolUse", tool="Write", file=str(mem), session_dir=tmp_path)
    call, calls = mirror_cli()
    monkeypatch.setattr(evt.ctx, "call_cli", call)

    check("mirror user-skip: silent", mirror_memory_to_note(evt) is None)
    check("mirror user-skip: issues no cc-notes calls at all", calls == [], repr(calls))


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
