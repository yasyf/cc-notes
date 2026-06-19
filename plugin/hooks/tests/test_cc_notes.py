# /// script
# requires-python = ">=3.13"
# dependencies = ["capt-hook>=3.10.0", "pydantic>=2"]
# ///
"""Direct unit tests for the cc-notes capt-hook pack's pure helpers and handlers.

The inline ``tests={...}`` on each hook prove environment-stable behavior (the
gate keeps the PostToolUse floaters silent, non-matching tools Allow). These
tests cover what the inline harness cannot make deterministic: the pure render
and filter helpers, the gate-silence path when ``cc-notes`` is absent, and a
firing handler end to end with stubbed CLI output. They mock the gate's true
externals (``shutil.which`` and the ``git for-each-ref`` subprocess) rather than
the condition object, so the real ``CcNotesAdopted`` logic runs under controlled
inputs.

Run with the same toolchain the inline tests use::

    uv run plugin/hooks/tests/test_cc_notes.py
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

import cc_notes
from cc_notes import (
    FloatedNotes,
    StaleChecked,
    cap_and_render_tasks,
    check_note_staleness,
    dedup_against_ids,
    filter_drifted,
    float_note_context,
    float_session_tasks,
    parse_relevant,
    parse_tasks,
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


def stub_cli(mapping: dict[tuple[str, ...], str]):
    """Build a call_cli stub: git probe returns a ref; cc-notes args map to payloads."""

    def _call(args, *, input=None, timeout=30, env=None):
        if args[0] == "git":
            return "abc123 commit\trefs/cc-notes/notes/abc123\n"
        key = tuple(args[1:])
        if key in mapping:
            return mapping[key]
        raise FileNotFoundError(args[0])

    return _call


def test_gate_silent_when_cc_notes_absent(monkeypatch) -> None:
    """With cc-notes off PATH, CcNotesAdopted fails closed and every handler is silent."""
    monkeypatch.setattr(cc_notes.shutil, "which", lambda _name: None)

    from captain_hook.conditions import matches_conditions
    from captain_hook.app import _state

    for entry in _state.hooks:
        if entry.handler not in (float_session_tasks, float_note_context, check_note_staleness):
            continue
        ev_type = next(iter(entry.spec.events))
        evt = mock_event(
            ev_type.name,
            tool="Read" if ev_type is Event.PostToolUse else None,
            file="internal/store/store.go",
            prompt="start work",
        )
        gated = matches_conditions(entry.spec, evt)
        check(f"gate-absent: {entry.name} condition fails closed", not gated)


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
        check("staleness: verify/edit/supersede/expire guidance", all(s in result.message for s in ("note verify", "note edit", "note supersede", "note expire")))
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


def test_run_cc_notes_fails_closed(monkeypatch, tmp_path) -> None:
    """Every subprocess failure mode falls closed to None, never raising into the hook fire."""
    import subprocess

    evt = mock_event("PostToolUse", tool="Read", file="x.go", session_dir=tmp_path)

    def raiser(exc):
        def _call(args, *, input=None, timeout=30, env=None):
            raise exc

        return _call

    cases = [
        ("missing binary (FileNotFoundError)", FileNotFoundError(2, "No such file", "cc-notes")),
        ("not executable (PermissionError)", PermissionError(13, "Permission denied", "cc-notes")),
        ("generic OSError", OSError(8, "Exec format error")),
        ("non-zero exit (CalledProcessError)", subprocess.CalledProcessError(1, ["cc-notes"])),
        ("timeout (TimeoutExpired)", subprocess.TimeoutExpired(["cc-notes"], 10)),
    ]
    for label, exc in cases:
        monkeypatch.setattr(evt.ctx, "call_cli", raiser(exc))
        try:
            result = run_cc_notes(evt, "relevant", "x.go", "--json")
            check(f"run_cc_notes: {label} -> None", result is None, repr(result))
        except BaseException as raised:  # noqa: BLE001 — the whole point is nothing escapes
            check(f"run_cc_notes: {label} -> None", False, f"escaped: {type(raised).__name__}: {raised}")


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
