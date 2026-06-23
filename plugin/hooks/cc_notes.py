"""capt-hook enforcement hooks for repos that have adopted cc-notes.

These are advisory NUDGES, never gates: cc-notes *complements* Claude's native
task tracking, so the hooks only ever warn — they never block a tool call.

Every *workflow* nudge is gated behind :class:`CcNotesAvailable`, which keeps them
completely silent unless the ``cc-notes`` binary is on PATH — with one exception:
:func:`prompt_install_cc_notes`, gated on the inverse :class:`CcNotesMissing`,
fires precisely when the binary is absent so an opt-in repo isn't left with silent
nudges and no hint. Per-repo opt-in is the pack's *presence* in
``.claude/hooks/packs.toml`` — a repo that doesn't want these nudges simply
doesn't enable the pack. The read-time floaters fall closed to silence on a repo
with no cc-notes refs anyway, since :func:`run_cc_notes` returns nothing to render
there.

The teaching goal is the native-vs-durable distinction:

  * native ``TaskCreate``/``TaskUpdate`` — ephemeral, this-session-only, private
    scratchpad for decomposing the current task into in-session steps;
  * ``cc-notes task`` — durable, git-synced, GLOBAL work. The id is global; a
    task's branch is a mutable attribute (the shared backlog is the unassigned
    queue, ``Branch == ""``, visible to every agent), and ``task start`` claims
    it under a lease while moving it onto your current branch;
  * ``cc-notes note`` — durable, git-synced, repo-global decisions & facts, born
    verified against HEAD with first-class drift/verify/supersede.
"""

from __future__ import annotations

import json
import re
import shutil
import subprocess
from typing import Any

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    Input,
    PostToolUseEvent,
    Prompt,
    Tool,
    UserPromptSubmitEvent,
    Warn,
    nudge,
    on,
    session_state,
)
from captain_hook.state import fired_this_turn, record_fire
from captain_hook.types import Command
from pydantic import BaseModel

NATIVE_TASK_MIRROR_THRESHOLD = 5

# SESSION_TASK_CAP bounds how many durable tasks the session-start floater shows
# before collapsing the rest into a "+K more" tail pointing at `cc-notes status`.
SESSION_TASK_CAP = 7

GIT_MERGE_PULL = r"^git\s+(?:-\S+\s+)*(?:merge|pull)\b"
GIT_COMMIT = r"^git\s+(?:-\S+\s+)*commit\b"
CC_NOTES_CLAIM = r"^cc-notes\s+task\s+(?:claim|start)\b"

# nudge_store_handoff_as_doc pre-gate knobs: only a substantial, non-exempt
# long-form markdown write reaches the (paid) classifier. HANDOFF_MIN_CHARS is the
# floor below which a write is too small to be a handoff brief; the name and dir
# exemptions skip the obviously human-facing files an LLM call would only waste
# budget on (a README, a published changelog, a docs/ tree, …).
HANDOFF_MIN_CHARS = 600

HANDOFF_EXEMPT_NAME = re.compile(
    r"^(README|CHANGELOG|CONTRIBUTING|LICENSE|AGENTS|CLAUDE|GEMINI|"
    r"STYLEGUIDE|CODE_OF_CONDUCT|SECURITY)\.(md|markdown)$",
    re.IGNORECASE,
)

HANDOFF_EXEMPT_DIRS = ("docs/", "site/", "blog/", "content/", ".github/")


def run_cc_notes(evt: BaseHookEvent, *args: str) -> str | None:
    """Run ``cc-notes`` with ``args`` in the project dir, returning stdout or None.

    Every way the subprocess can fail — a missing binary, a present-but-not-executable
    binary, a non-zero exit, a timeout — falls closed to ``None`` so a handler stays
    silent rather than crashing the hook fire. ``OSError`` covers ``FileNotFoundError``
    and ``PermissionError``; ``SubprocessError`` covers ``CalledProcessError`` and
    ``TimeoutExpired``.
    """
    try:
        return evt.ctx.call_cli(["cc-notes", *args], timeout=10)
    except (OSError, subprocess.SubprocessError):
        return None


def parse_relevant(out: str | None) -> list[dict[str, Any]]:
    """Parse ``cc-notes relevant --json`` stdout into its list of ranked entries.

    Returns an empty list for ``None``, blank output, malformed JSON, or any
    shape that is not a JSON array — the callers treat "nothing parsed" and
    "nothing relevant" identically. The JSON is a tagged union: a note (or legacy
    untagged) entry carries a ``note`` dict, a ``kind == "doc"`` entry carries a
    ``doc`` dict. Entries whose kind-appropriate payload is not a dict with a
    non-empty string ``id`` are dropped, so the downstream render/dedup/persist
    helpers can index the payload id without guarding every ill-shaped element.
    """
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []
    return [e for e in parsed if _well_shaped_entry(e)]


def entry_kind(entry: dict[str, Any]) -> str:
    """Return a relevance entry's kind tag, defaulting legacy (untagged) entries to "note".

    ``cc-notes relevant --json`` tags every entry with a top-level ``kind`` of
    ``"note"`` or ``"doc"``. Entries written before the tag existed carry no
    ``kind`` and are notes, so anything that isn't ``"doc"`` reads as a note.
    """
    return "doc" if entry.get("kind") == "doc" else "note"


def entry_payload(entry: dict[str, Any]) -> dict[str, Any]:
    """Return the inner DTO an entry carries: its ``doc`` object for docs, else ``note``.

    A doc entry omits the ``note`` key and a note entry omits ``doc``, so callers
    index the right object by kind. Falls to ``{}`` for an absent or non-dict
    payload, mirroring the ``.get(..., {})`` the render helpers relied on.
    """
    payload = entry.get(entry_kind(entry))
    return payload if isinstance(payload, dict) else {}


def _well_shaped_entry(entry: Any) -> bool:
    """Report whether ``entry`` is a relevance entry with an indexable payload id.

    A note entry (and any legacy untagged entry) must carry a ``note`` dict with a
    non-empty string ``id``; a ``kind == "doc"`` entry must carry such a ``doc``
    dict. Either way the render/dedup/persist helpers can index the payload's
    ``id`` without guarding every ill-shaped element.
    """
    if not isinstance(entry, dict):
        return False
    payload = entry.get(entry_kind(entry))
    return isinstance(payload, dict) and isinstance(payload.get("id"), str) and bool(payload["id"])


def parse_tasks(out: str | None) -> list[dict[str, Any]]:
    """Parse ``cc-notes task list --json`` stdout into its flat list of task DTOs.

    Returns an empty list for ``None``, blank output, malformed JSON, or any
    non-array shape. Non-dict array elements are dropped so ``render_task_line``
    can call ``.get`` on every survivor.
    """
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    if not isinstance(parsed, list):
        return []
    return [t for t in parsed if isinstance(t, dict)]


def short_id(full: str) -> str:
    """Return the 7-char short id cc-notes renders, the first 7 hex chars of an id."""
    return full[:7]


def render_note_lines(entries: list[dict[str, Any]]) -> list[str]:
    """Render relevance entries as one-line pointers, dispatched per entry kind.

    Notes render ``<short> <title> (reasons) [DRIFT]``; docs render their title,
    the ``when`` "read this when…" trigger, the freshness verdict, and a
    ``doc show`` hint — never the doc body. See :func:`render_note_line` and
    :func:`render_doc_line`.
    """
    return [render_doc_line(e) if entry_kind(e) == "doc" else render_note_line(e) for e in entries]


def render_note_line(entry: dict[str, Any]) -> str:
    """Render one note entry as ``<short> <title> (reasons) [DRIFT]``.

    The drift suffix is appended only when the note's ``drift`` verdict is
    non-null; a fresh note (null/absent drift) carries no suffix.
    """
    note = entry.get("note", {})
    reasons = ", ".join(entry.get("reasons", []))
    line = f"{short_id(note.get('id', ''))} {note.get('title', '')}"
    if reasons:
        line += f" ({reasons})"
    if drift := note.get("drift"):
        line += f" [{drift}]"
    return line


def render_doc_line(entry: dict[str, Any]) -> str:
    """Render one doc entry as a pointer — title, ``when`` trigger, verdict, ``doc show``.

    Surfaces the short id, title, the verbatim ``when`` "read this when…" trigger,
    the freshness verdict in lowercased brackets when the doc is not fresh
    (``[stale]``/``[expired]``/``[drifted]``/``[unverified]``), the match reasons,
    and a ``cc-notes doc show <short>`` hint. Never renders ``doc.body`` — the long
    body stays in cc-notes, only its pointer floats.
    """
    doc = entry.get("doc", {})
    short = short_id(doc.get("id", ""))
    line = f"{short} {doc.get('title', '')}"
    if when := doc.get("when"):
        line += f" — when: {when}"
    if drift := doc.get("drift"):
        line += f" [{str(drift).lower()}]"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    line += f" — cc-notes doc show {short}"
    return line


def dedup_against_ids(entries: list[dict[str, Any]], seen: list[str]) -> list[dict[str, Any]]:
    """Drop relevance entries whose payload id is already in ``seen``, preserving order.

    Indexes each entry by kind (``doc`` or ``note``) so a floated doc dedups
    against its own id, not a missing ``note`` key.
    """
    seen_set = set(seen)
    return [e for e in entries if entry_payload(e).get("id") not in seen_set]


def filter_drifted(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Keep only relevance entries whose kind-dispatched payload has a non-null ``drift``.

    Indexes each entry by kind (:func:`entry_payload`) so a drifted/expired doc
    (drift under ``doc.drift``) is kept alongside a drifted note, not silently
    dropped for lacking a ``note`` key.
    """
    return [e for e in entries if entry_payload(e).get("drift")]


def render_task_line(task: dict[str, Any]) -> str:
    """Render one task DTO as ``<short> <status> <title>`` plus ``@assignee`` when set."""
    line = f"{short_id(task.get('id', ''))} {task.get('status', '')} {task.get('title', '')}"
    if assignee := task.get("assignee"):
        line += f" @{assignee}"
    return line


def cap_and_render_tasks(tasks: list[dict[str, Any]], cap: int) -> list[str]:
    """Render up to ``cap`` task lines, with a ``+K more`` tail when truncated.

    Returns an empty list for no tasks. When ``len(tasks) > cap`` the first
    ``cap`` are rendered and a final ``+K more — run `cc-notes status``` line
    accounts for the remainder.
    """
    if not tasks:
        return []
    lines = [render_task_line(t) for t in tasks[:cap]]
    if (extra := len(tasks) - cap) > 0:
        lines.append(f"+{extra} more — run `cc-notes status`")
    return lines


class CcNotesAvailable(CustomCondition):
    """Matches whenever the ``cc-notes`` binary resolves on PATH.

    Gating on the binary alone is deliberate: enabling the pack in a repo (its
    presence in ``.claude/hooks/packs.toml``) *is* the per-repo opt-in, so a
    fresh repo with no ``refs/cc-notes/*`` yet still gets the adoption nudges
    that prompt the first ``cc-notes`` write. There is no chicken-and-egg ref
    probe to satisfy first. The read-time floaters (nudges 1-3) shell out to
    ``cc-notes`` through :func:`run_cc_notes`, which returns nothing to render on
    an empty repo, so they fall closed to silence on their own.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        return shutil.which("cc-notes") is not None


class CcNotesMissing(CustomCondition):
    """Matches whenever the ``cc-notes`` binary does NOT resolve on PATH.

    The exact inverse of :class:`CcNotesAvailable`. A wired pack with no binary
    on PATH is the silent dead-end this nudge breaks: every workflow nudge gates
    closed and nothing signals that cc-notes is in play here. It is the visible
    fallback when the plugin's SessionStart auto-installer could not produce a
    binary (offline, locked-down env). Gate on binary absence alone — no ref
    probe — matching CcNotesAvailable's binary-only philosophy.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        return shutil.which("cc-notes") is None


class ManyNativeTasks(CustomCondition):
    """Matches when the session is carrying enough open native tasks to look durable.

    A large, growing native task list is the drift signal: some of those items are
    almost certainly cross-session or cross-agent work that belongs in cc-notes.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        return len(evt.tasks.open) >= NATIVE_TASK_MIRROR_THRESHOLD


@session_state
class FloatedNotes(BaseModel):
    """Per-session record of note ids already floated as Read-time context."""

    ids: list[str] = []


@session_state
class StaleChecked(BaseModel):
    """Per-session record of note ids already surfaced as edit-time staleness prompts."""

    ids: list[str] = []


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
    max_fires=1,
)
def float_session_tasks(evt: UserPromptSubmitEvent) -> Any:
    """Float this session's durable tasks once, at the first prompt.

    Lists the current branch's open/in-progress tasks, topping up from the
    shared backlog while room remains under :data:`SESSION_TASK_CAP`, and
    renders the combined top ``SESSION_TASK_CAP`` as orientation pointing at
    ``cc-notes status``. Silent when cc-notes is absent or there are no tasks.
    """
    branch_tasks = parse_tasks(run_cc_notes(evt, "task", "list", "--json"))
    tasks = list(branch_tasks)
    if len(tasks) < SESSION_TASK_CAP:
        tasks.extend(parse_tasks(run_cc_notes(evt, "task", "list", "--backlog", "--json")))
    lines = cap_and_render_tasks(tasks, SESSION_TASK_CAP)
    if not lines:
        return None
    return evt.warn(
        "Durable cc-notes tasks in play — run `cc-notes status` to orient "
        "(shared backlog, your branch's tasks, who holds what, notes needing review):",
        *lines,
    )


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesMissing()],
    max_fires=1,
)
def prompt_install_cc_notes(evt: UserPromptSubmitEvent) -> Any:
    """Once per session, surface that the cc-notes binary is missing and how to install it.

    The pack is wired here (its presence in packs.toml is the opt-in) but the
    binary is off PATH, so every other nudge gates closed and the plugin's
    SessionStart auto-installer evidently did not land one. Name the two install
    paths once at the first prompt rather than failing silent.
    """
    return evt.warn(
        "cc-notes hooks are enabled in this repo but the `cc-notes` binary isn't on "
        "PATH, so every cc-notes nudge stays silent (the plugin's auto-install didn't "
        "land one). Install it to enable them:",
        "brew install yasyf/tap/cc-notes",
        "# or: curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh",
    )


@on(
    Event.PostToolUse,
    only_if=[Tool("Read"), CcNotesAvailable()],
    tests={
        # A non-Read tool never matches the Tool gate — deterministic silence
        # regardless of cc-notes state. The firing path (Read floats relevant
        # notes) shells out to `cc-notes relevant`, so its assertion lives in
        # tests/test_cc_notes.py with a stubbed CLI, not here.
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def float_note_context(evt: PostToolUseEvent) -> Any:
    """Float notes and docs relevant to a freshly read file, once per id per session.

    Runs ``cc-notes relevant <path> --json``, drops ids already floated this
    session, and on anything new persists the union and warns with each entry's
    pointer — a note's title/reasons/drift, or a doc's title, ``when`` trigger,
    and freshness verdict (never its body). Silent when nothing remains or the
    command empties.
    """
    if not evt.file:
        return None
    entries = parse_relevant(run_cc_notes(evt, "relevant", str(evt.file), "--json"))
    floated = evt.ctx.session.load(FloatedNotes)
    fresh = dedup_against_ids(entries, floated.ids)
    if not fresh:
        return None
    floated.ids = floated.ids + [entry_payload(e)["id"] for e in fresh]
    evt.ctx.session[FloatedNotes].set(floated)
    return evt.warn(
        f"Notes and docs relevant to {evt.file} (durable, git-synced cc-notes context):",
        *render_note_lines(fresh),
    )


@on(
    Event.PostToolUse,
    only_if=[Tool("Edit|Write|MultiEdit"), CcNotesAvailable()],
    tests={
        # A Read never matches the Edit|Write|MultiEdit gate — deterministic
        # silence regardless of cc-notes state. The firing path (a drifted note
        # on the edited path) shells out to `cc-notes relevant`, so its assertion
        # lives in tests/test_cc_notes.py with a stubbed CLI, not here.
        Input(tool="Read", file="m.py"): Allow(),
    },
)
def check_note_staleness(evt: PostToolUseEvent) -> Any:
    """Prompt reconciliation when an edit touches a path anchored by a drifted note or doc.

    Runs ``cc-notes relevant <path> --attached --worktree --json``, keeps only
    entries whose kind-dispatched drift verdict is non-null (notes and
    drifted/expired docs alike), drops ids already surfaced this session, and on
    anything new persists the union and warns naming the file and the
    verify/edit/supersede/expire next steps for the matching kind. Docs render
    through :func:`render_doc_line` (pointer only, never the body). Silent
    otherwise.
    """
    if not evt.file:
        return None
    entries = parse_relevant(run_cc_notes(evt, "relevant", str(evt.file), "--attached", "--worktree", "--json"))
    drifted = filter_drifted(entries)
    checked = evt.ctx.session.load(StaleChecked)
    fresh = dedup_against_ids(drifted, checked.ids)
    if not fresh:
        return None
    checked.ids = checked.ids + [entry_payload(e)["id"] for e in fresh]
    evt.ctx.session[StaleChecked].set(checked)
    return evt.warn(
        f"You edited {evt.file}, which a note or doc flags as needing attention. Reconcile each "
        "against its kind — `verify <id>` to re-confirm it against HEAD, `edit <id>` to revise it, "
        "`supersede <old> --by <new>` to replace it, or `expire <id>` to flag it out-of-date: "
        "for a note use `cc-notes note verify/edit/supersede/expire`, "
        "for a doc use `cc-notes doc verify/edit/supersede/expire`.",
        *render_note_lines(fresh),
    )


HANDOFF_CLASSIFIER_SYSTEM = (
    "You classify a markdown file an agent just wrote in a code repository. Decide "
    "whether it is an INTERNAL AGENT-HANDOFF doc: long-form context written for the "
    "NEXT agent (or your future self) to read before touching this code. Handoffs "
    "look like a state-of-play brief, a \"read this before you change X\" guide, "
    "migration notes, design rationale for an in-flight change, a resume-here "
    "handoff, or an investigation write-up. These belong in cc-notes as a durable, "
    "drift-checked doc that future agents are surfaced automatically.\n"
    "\n"
    "It is NOT a handoff when the file is genuinely human-facing or published "
    "project documentation that belongs in the repo tree: a README, a user guide, a "
    "tutorial, API reference, a released changelog, a blog post, release notes, or a "
    "spec written for people. When the file could plausibly be either, answer "
    "is_handoff=false — only flag a clear internal handoff.\n"
    "\n"
    "When is_handoff is true, also return: title — a short title for the doc; when — "
    "a free-text \"read this when…\" trigger naming the future situation in which an "
    "agent should reach for it; area — the repo directory the doc is about (e.g. "
    "internal/api), or \".\" if unclear; reasoning — one line explaining the call."
)


class HandoffVerdict(BaseModel):
    """The classifier's verdict on whether a freshly written ``.md`` is an agent handoff.

    ``is_handoff`` defaults False so a degenerate or empty model parse fails closed
    to silence. The remaining fields seed the suggested ``cc-notes doc add`` command
    and are only meaningful when ``is_handoff`` is true.
    """

    is_handoff: bool = False
    title: str = ""
    when: str = ""
    area: str = ""
    reasoning: str = ""


def _doc_pre_gated(evt: PostToolUseEvent) -> str | None:
    """Return the markdown body worth classifying, or None to skip the LLM entirely.

    The cheap, paid-call-free filter in front of :func:`nudge_store_handoff_as_doc`:
    only a substantial, non-exempt long-form ``.md``/``.markdown`` write that has not
    already nudged this turn survives. Everything else returns None so the classifier
    is never called — that pre-gate is what keeps the LLM cost off the vast majority
    of markdown writes.
    """
    if not (evt.file and evt.file_matches("*.md", "*.markdown")):
        return None
    if HANDOFF_EXEMPT_NAME.match(evt.file.name):
        return None
    if evt.file.under(*HANDOFF_EXEMPT_DIRS):
        return None
    body = evt.content or ""
    if len(body) < HANDOFF_MIN_CHARS:
        return None
    if fired_this_turn(evt):
        return None
    return body


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit"), CcNotesAvailable()],
    max_fires=2,
    tests={
        # Every case is deterministic silence WITHOUT a stubbed classifier: the
        # cheap pre-gate returns None first (exempt name, exempt dir, wrong suffix,
        # or too short) or the Tool gate rejects a non-Write/Edit tool, so the LLM
        # is never reached. The firing / public / exempt-skips-LLM split needs a
        # stubbed call_llm and lives in tests/test_cc_notes.py.
        Input(tool="Write", file="README.md", content="# Readme\n" + "lorem ipsum " * 120): Allow(),
        Input(tool="Write", file="CHANGELOG.md", content="# Changelog\n" + "entry text " * 120): Allow(),
        Input(tool="Write", file="AGENTS.md", content="# Agents\n" + "guidance line " * 120): Allow(),
        Input(tool="Write", file="docs/guide.md", content="# Guide\n" + "prose body " * 120): Allow(),
        Input(tool="Write", file="internal/store/store.py", content="x = 1\n" * 400): Allow(),
        Input(tool="Write", file="HANDOFF.md", content="too short to be a handoff"): Allow(),
        Input(tool="Read", file="HANDOFF.md"): Allow(),
    },
)
def nudge_store_handoff_as_doc(evt: PostToolUseEvent) -> Any:
    """Nudge storing a long-form internal-handoff ``.md`` as a cc-notes doc.

    A cheap static pre-gate (:func:`_doc_pre_gated`) drops everything that is
    obviously not an agent handoff before any paid call. The survivor's body is
    classified by a small stateless LLM call (``agent=False, transcript=False``)
    biased toward "not a handoff"; only a clear handoff warns, naming the
    ``cc-notes doc add … --when …`` that would store it where future agents are
    surfaced it automatically. Fails closed to silence on any classifier error —
    this is a nudge, never a gate.
    """
    body = _doc_pre_gated(evt)
    if body is None:
        return None
    prompt = (
        Prompt()
        .system(HANDOFF_CLASSIFIER_SYSTEM)
        .context("path", str(evt.file))
        .context("markdown", body[:4000])
        .ask("Is this markdown an internal agent-handoff doc that belongs in cc-notes?")
    )
    try:
        verdict = evt.ctx.call_llm(prompt, response_model=HandoffVerdict, agent=False, transcript=False)
    except Exception:
        # Fail closed: a classifier error (network, timeout, bad parse) must never
        # crash a nudge fire — the pack only ever warns, it never blocks.
        return None
    if not verdict.is_handoff:
        return None
    record_fire(evt)
    add_cmd = (
        f'cc-notes doc add "{verdict.title or evt.file.stem}" '
        f'--when "{verdict.when}" --dir {verdict.area or "."} --body -'
    )
    return evt.warn(
        f"{evt.file} reads like an internal handoff written for the next agent, not "
        "human-facing documentation. Store it as a durable cc-notes doc instead of a "
        "loose file that nothing reopens — docs are ranked by `cc-notes relevant`, "
        "floated to future agents on read, and drift-checked against HEAD:",
        add_cmd,
        '(Pipe the markdown into `--body -`; `--when` is the "read this when…" '
        "trigger that decides when a future agent is shown it.)",
    )


nudge(
    "Plan approved. Native TaskCreate/TaskUpdate is your private, this-session "
    "scratchpad. Durable shared work goes in `cc-notes task add --backlog` (the "
    "global queue every agent can see and claim) — or plain `cc-notes task add` "
    "for work specific to your current branch. Capture decisions and durable "
    "facts as `cc-notes note add`. Long-form handoffs or internal context for the "
    "next agent go in `cc-notes doc add` (not a loose .md) — docs surface "
    "automatically through `cc-notes relevant`.",
    only_if=[Tool("ExitPlanMode"), CcNotesAvailable()],
    events=Event.PostToolUse,
    tests={
        Input(tool="ExitPlanMode"): Warn(pattern="cc-notes task add"),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)


nudge(
    "Commit landed. Add a `cc-task: <id>` trailer to link it to its task "
    "(queryable with `git log --grep` and `cc-notes blame <sha>`). Capture any "
    "durable decision behind it as `cc-notes note add \"...\" --tag design` (born "
    "verified against HEAD), and a long-form handoff or internal brief for a future "
    "agent as `cc-notes doc add` (not a loose .md), then `cc-notes sync` to share "
    "your refs.",
    only_if=[Command(GIT_COMMIT), CcNotesAvailable()],
    events=Event.PostToolUse,
    tests={
        Input(command="git commit -m 'add retry ceiling'"): Warn(pattern="cc-task:"),
        Input(command="git commit --amend"): Warn(pattern="cc-notes sync"),
        Input(command="git status"): Allow(),
    },
)


nudge(
    "A merged branch's still-open tasks stay on that branch until you carry them "
    "over. Run `cc-notes reconcile --into <target>` to set them onto the target, "
    "then `cc-notes sync` to converge with the remote. Both are idempotent. "
    "(jj merges never fire git hooks — reconcile is the explicit step.)",
    only_if=[Command(GIT_MERGE_PULL), CcNotesAvailable()],
    events=Event.PostToolUse,
    max_fires=3,
    tests={
        Input(command="git merge feature"): Warn(pattern="reconcile"),
        Input(command="git pull origin main"): Warn(pattern="reconcile"),
        Input(command="git pull"): Warn(pattern="reconcile"),
        Input(command="git status"): Allow(),
        Input(command="git log --no-merges"): Allow(),
    },
)


nudge(
    "You hold a lease now. Run `cc-notes sync` so other agents see the claim, "
    "`cc-notes task renew <id>` on long silent stretches, and `cc-notes task done "
    "<id>` when finished. A crashed hold whose lease expired is reclaimable with "
    "`cc-notes task claim <id> --steal`.",
    only_if=[Command(CC_NOTES_CLAIM), CcNotesAvailable()],
    events=Event.PostToolUse,
    max_fires=2,
    tests={
        Input(command="cc-notes task start d82c087"): Warn(pattern="renew"),
        Input(command="cc-notes task claim 08118da --steal"): Warn(pattern="renew"),
        Input(command="cc-notes task list"): Allow(),
    },
)


nudge(
    "Your native task list is getting large. Native tasks vanish at session end "
    "and are private to this agent — mirror any that are durable or cross-agent "
    "into `cc-notes task add` (`--backlog` if it's shared work anyone can claim). "
    "Keep the purely in-session steps as native todos.",
    only_if=[Tool("TaskCreate"), ManyNativeTasks(), CcNotesAvailable()],
    events=Event.PostToolUse,
    max_fires=2,
    tests={
        Input(
            tool="TaskCreate",
            tasks=[{"id": str(i), "subject": f"t{i}", "status": "pending"} for i in range(NATIVE_TASK_MIRROR_THRESHOLD)],
        ): Warn(pattern="cc-notes task add"),
        Input(
            tool="TaskCreate",
            tasks=[{"id": "1", "subject": "t1", "status": "in_progress"}],
        ): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
