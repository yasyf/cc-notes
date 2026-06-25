"""capt-hook nudges for repos that have adopted cc-notes.

These are advisory NUDGES, never gates: cc-notes complements Claude's native task
tracking, so the hooks only ever warn — they never block a tool call. Per-repo
opt-in is the pack's presence in ``.claude/hooks/packs.toml``; a repo that doesn't
want the nudges simply doesn't enable it.

Every hook is one shape — static recall → LLM precision → act — running in one of two
directions. **Surface** (pull) takes a file the agent touched, recalls the durable
records anchored to it, and a small LLM keeps the subset worth surfacing
(:func:`surface_filter`); it fails OPEN, since a broken filter must never hide
context. **Record** (push) takes a write, commit, or plan, recalls a candidate over a
cheap glob/diff, and a small LLM confirms it is durable and routes it — or routes it to
nothing; it fails CLOSED to silence. The cheap layer deliberately over-selects; the LLM
is the precision gate in both directions.

For what each cc-notes record (note/doc/log/task) is, see the README's "What the hooks
teach" table — the prompts here speak in those same terms.
"""

from __future__ import annotations

import json
import re
import shutil
from pathlib import Path
from typing import Any, NamedTuple

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
)
from captain_hook.state import fired_this_turn, record_fire
from captain_hook.types import Command
from pydantic import BaseModel

NATIVE_TASK_MIRROR_THRESHOLD = 5

# Max durable tasks the session-start floater shows before a "+K more" tail.
SESSION_TASK_CAP = 7
# Per-session fire cap for advisories that aren't once-per-session and don't self-dedup.
NUDGE_MAX_FIRES = 3
# Cap on body/diff/plan text handed to a small-model classifier.
LLM_INPUT_CAP = 6000

GIT_MERGE_PULL = r"^git\s+(?:-\S+\s+)*(?:merge|pull)\b"
GIT_COMMIT = r"^git\s+(?:-\S+\s+)*commit\b"
CC_NOTES_CLAIM = r"^cc-notes\s+task\s+(?:claim|start)\b"

# DurableInternalWrite glob vocabulary: the cheap, over-selective recall gate the
# :class:`DurableInternalWrite` condition reads before handing off to the LLM
# record-router. STRONG names look durable-internal on their name alone; WEAK names
# only qualify when the body carries an internal signal (a `- [ ]` checklist or a
# handoff/status/runbook keyword via INTERNAL_BODY_RE). PUBLISHED/SOURCE/SECRET are
# the hard exclusions — a write that matches one of those is never durable-internal
# knowledge that belongs out of the public tree.
STRONG_INTERNAL_GLOBS = ("*_VERIFICATION.md", "*HANDOFF*.md", "*STATUS*.md", "*-handoff.md", "HANDOFF.md", "STATUS.md", "NOTES.md")
WEAK_INTERNAL_GLOBS = ("TODO.md", "*-notes.md", "runbook*.md", "runbook*", "scratch*.md")
PUBLISHED_GLOBS = ("README*", "CHANGELOG*", "LICENSE*", "CONTRIBUTING*", "*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg")
PUBLISHED_DIRS = ("docs/",)
SECRET_GLOBS = (".env", ".env.*", "*.env", "*secret*", "*credential*", "*.key", "*.pem")
SOURCE_GLOBS = (
    "*.py", "*.pyi", "*.ts", "*.tsx", "*.js", "*.mjs", "*.cjs", "*.jsx",
    "*.go", "*.rs", "*.java", "*.c", "*.h", "*.cpp", "*.rb", "*.sh",
    "*.json", "*.toml", "*.yaml", "*.yml",
)

# A WEAK-named write only nudges when its body carries an internal signal: a
# top-level (optionally indented) `- [ ]` checklist line, or a handoff/status/
# runbook-flavored keyword. The leading `(?im)` makes the whole pattern
# case-insensitive and multiline.
INTERNAL_BODY_RE = r"(?im)^\s*- \[ \]|\b(handoff|hand-off|remaining|next steps|runbook|verification|status|decisions?)\b"


def run_cc_notes(evt: BaseHookEvent, *args: str) -> str | None:
    """Run ``cc-notes`` with ``args`` in the project dir, returning stdout or None.

    Every way the subprocess can fail — a missing binary, a present-but-not-executable
    binary, a non-zero exit, a timeout — falls closed to ``None`` (``throw=False``) so a
    handler stays silent rather than crashing the hook fire.
    """
    return evt.ctx.call_cli(["cc-notes", *args], timeout=10, throw=False)


def parse_relevant(out: str | None) -> list[dict[str, Any]]:
    """Parse ``cc-notes relevant --json`` stdout into its list of ranked entries.

    Returns an empty list for ``None``, blank output, malformed JSON, or any
    shape that is not a JSON array — the callers treat "nothing parsed" and
    "nothing relevant" identically. The JSON is a tagged union: a note (or legacy
    untagged) entry carries a ``note`` dict, a ``kind == "doc"`` entry carries a
    ``doc`` dict, and a ``kind == "log"`` entry carries a ``log`` dict. Entries
    whose kind-appropriate payload is not a dict with a non-empty string ``id`` are
    dropped, so the downstream render/dedup/persist helpers can index the payload
    id without guarding every ill-shaped element.
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
    ``"note"``, ``"doc"``, or ``"log"``. ``"doc"`` and ``"log"`` are passed
    through verbatim; entries written before the tag existed carry no ``kind`` and
    are notes, so anything else reads as a note. The payload helpers
    (:func:`entry_payload`/:func:`_well_shaped_entry`) index by this tag, so once a
    kind is recognized here its DTO is picked up under the matching key.
    """
    kind = entry.get("kind")
    return kind if kind in ("doc", "log") else "note"


def entry_payload(entry: dict[str, Any]) -> dict[str, Any]:
    """Return the inner DTO an entry carries, keyed by kind: ``doc``, ``log``, else ``note``.

    Each entry nests its DTO under the key matching its kind and omits the others,
    so callers index the right object by :func:`entry_kind`. Falls to ``{}`` for an
    absent or non-dict payload, mirroring the ``.get(..., {})`` the render helpers
    relied on.
    """
    payload = entry.get(entry_kind(entry))
    return payload if isinstance(payload, dict) else {}


def _well_shaped_entry(entry: Any) -> bool:
    """Report whether ``entry`` is a relevance entry with an indexable payload id.

    A note entry (and any legacy untagged entry) must carry a ``note`` dict with a
    non-empty string ``id``; a ``kind == "doc"`` entry must carry such a ``doc``
    dict, and a ``kind == "log"`` entry such a ``log`` dict. Either way the
    render/dedup/persist helpers can index the payload's ``id`` without guarding
    every ill-shaped element.
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
    ``doc show`` hint — never the doc body; logs render their title, reasons, and a
    ``log show`` hint with no drift suffix (an append-only record never drifts).
    See :func:`render_note_line`, :func:`render_doc_line`, and
    :func:`render_log_line`.
    """
    dispatch = {"doc": render_doc_line, "log": render_log_line}
    return [dispatch.get(entry_kind(e), render_note_line)(e) for e in entries]


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


def render_log_line(entry: dict[str, Any]) -> str:
    """Render one log entry as a pointer — short id, title, match reasons, ``log show``.

    A log is anchored and floated like a doc, but it is an append-only journal with
    no freshness lifecycle: it never drifts, so the line carries no drift suffix and
    no ``when`` trigger. The entries stay in cc-notes — only the pointer floats, with
    a ``cc-notes log show <short>`` hint to read the full chronology.
    """
    log = entry.get("log", {})
    short = short_id(log.get("id", ""))
    line = f"{short} {log.get('title', '')}"
    if reasons := ", ".join(entry.get("reasons", [])):
        line += f" ({reasons})"
    line += f" — cc-notes log show {short}"
    return line


def filter_drifted(entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Keep only relevance entries whose kind-dispatched payload has a non-null ``drift``.

    Indexes each entry by kind (:func:`entry_payload`) so a drifted/expired doc
    (drift under ``doc.drift``) is kept alongside a drifted note, not silently
    dropped for lacking a ``note`` key. A log carries no ``drift`` field at all —
    an append-only record never claims current truth, so it can never drift — so a
    log is correctly excluded from the staleness nudge here.
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


def in_cc_pool_memory(path: Path) -> bool:
    """True for any file under a cc-pool agent-memory dir (``.cc-pool/…/memory/``).

    The mirror (:func:`mirror_memory_to_note`) is the sole owner of this tree, so the
    advisory record-router excludes it — a memory write is captured by the mirror, never
    nudged. Deliberately broader than :class:`MemoryWrite`: it covers ``MEMORY.md``,
    ``user``-type memories, and any extension, because the *whole* tree is the mirror's
    domain, even the files the mirror chooses not to mirror.
    """
    return ".cc-pool" in path.parts and path.parent.name == "memory"


MIRRORED_MEMORY_TYPES = ("feedback", "project", "reference")

# A cc-pool memory file is YAML frontmatter (name/description/metadata) fenced by
# `---` lines, then a markdown body. The mirror reads the body verbatim plus two
# frontmatter scalars: metadata.type (decides which memories mirror) and the
# description (the note title).
MEMORY_FRONTMATTER = re.compile(r"\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*\r?\n?(.*)\Z", re.DOTALL)


class ParsedMemory(NamedTuple):
    """A cc-pool memory file split into the fields the mirror needs."""

    type: str
    title: str
    body: str


def parse_memory_file(path: Path) -> ParsedMemory | None:
    """Parse a memory file's ``metadata.type``, ``description`` title, and body, or None.

    Reads from disk so a Write and an Edit both yield the final merged content.
    Returns None when the file is unreadable or carries no ``---`` frontmatter — the
    caller treats that as "nothing to mirror".
    """
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return None
    m = MEMORY_FRONTMATTER.match(text)
    if not m:
        return None
    front, body = m.group(1), m.group(2)
    return ParsedMemory(
        type=_front_field(front, "type", indented=True),
        title=_front_field(front, "description"),
        body=body.strip(),
    )


def _front_field(front: str, key: str, *, indented: bool = False) -> str:
    """Extract a frontmatter scalar by key, unwrapping a single layer of quotes.

    ``indented`` allows leading whitespace so a nested key (``metadata.type``) is
    found; the anchored ``^[ \\t]*type:`` still cannot match mid-line inside a
    same-suffixed sibling like ``node_type:``.
    """
    indent = r"[ \t]*" if indented else ""
    m = re.search(rf"(?m)^{indent}{re.escape(key)}:[ \t]*(.*)$", front)
    if not m:
        return ""
    val = m.group(1).strip()
    if len(val) >= 2 and val[0] in "\"'" and val[-1] == val[0]:
        val = val[1:-1]
    return val


def memory_notes(evt: BaseHookEvent, slug: str) -> list[dict[str, Any]]:
    """Return existing notes carrying the ``memory:<slug>`` mirror key.

    Empty for no match, unreadable output, or any non-array shape — the caller
    reads "none found" as "create a new note".
    """
    out = run_cc_notes(evt, "note", "list", "--tag", f"memory:{slug}", "--json")
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    return [n for n in parsed if isinstance(n, dict)] if isinstance(parsed, list) else []


def note_id_of(out: str | None) -> str:
    """Pull the ``id`` from a ``cc-notes note add --json`` reply, or "" when absent."""
    if not out or not out.strip():
        return ""
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return ""
    return parsed.get("id", "") if isinstance(parsed, dict) else ""


class CcNotesAvailable(CustomCondition):
    """Matches whenever the ``cc-notes`` binary resolves on PATH.

    Gating on the binary alone is deliberate: enabling the pack in a repo (its
    presence in ``.claude/hooks/packs.toml``) *is* the per-repo opt-in, so a
    fresh repo with no ``refs/cc-notes/*`` yet still gets the adoption nudges
    that prompt the first ``cc-notes`` write. There is no chicken-and-egg ref
    probe to satisfy first. The Surface floaters shell out to
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


class DurableInternalWrite(CustomCondition):
    """Matches a write of durable INTERNAL knowledge that belongs out of the public tree.

    The cheap, over-selective recall gate in front of the LLM :func:`nudge_record_durable`:
    it flags the loose status/handoff/notes/runbook/TODO file an agent would otherwise
    commit to a public branch — the kind cc-notes keeps as git objects on
    ``refs/cc-notes/*`` instead — and the router's LLM then confirms and routes it. A
    generic ``memory/`` write of any extension counts (that tree is the durable-fact
    home), a STRONG-named ``.md`` counts on its name, a WEAK-named ``.md`` counts only
    when its body carries an internal signal (:data:`INTERNAL_BODY_RE`). The cc-pool
    memory tree (:func:`in_cc_pool_memory`) is excluded — the mirror owns it. Published
    docs (README/CHANGELOG/…, ``docs/``, images), source files, and anything secret-shaped
    are hard-excluded — those are never durable-internal knowledge, and secrets must never
    be pushed into refs that sync to the remote.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        file = evt.file
        if file is None:
            return False
        # The mirror owns the cc-pool memory tree (handled deterministically by
        # metadata.type), so the LLM record-router never fires there — guard first so
        # a memory slug literally named "handoff" can't leak into the STRONG branch.
        if in_cc_pool_memory(Path(str(file))):
            return False
        # A `memory/` write of ANY extension is durable-internal — caught before
        # the non-.md bail below — unless it is secret-shaped (refs sync remotely).
        if file.under("memory/") and not file.matches(*SECRET_GLOBS):
            return True
        if file.suffix.lower() != ".md":
            return False
        if file.matches(*SECRET_GLOBS):
            return False
        if file.matches(*PUBLISHED_GLOBS) or file.under(*PUBLISHED_DIRS):
            return False
        if file.matches(*SOURCE_GLOBS):
            return False
        if file.matches(*STRONG_INTERNAL_GLOBS):
            return True
        if file.matches(*WEAK_INTERNAL_GLOBS):
            return bool(evt.content) and bool(re.search(INTERNAL_BODY_RE, evt.content))
        return False


class MemoryWrite(CustomCondition):
    """Matches a write to a cc-pool agent-memory file.

    True for a ``<slug>.md`` directly under a ``memory/`` dir somewhere inside a
    ``.cc-pool`` tree, excluding the ``MEMORY.md`` index. This is the cheap path
    gate in front of :func:`mirror_memory_to_note` — it short-circuits before any
    disk read for the overwhelming majority of writes that aren't memories.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        if evt.file is None:
            return False
        p = Path(str(evt.file))
        return in_cc_pool_memory(p) and p.suffix == ".md" and p.name != "MEMORY.md"


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


SURFACE_FILTER_SYSTEM = (
    "You are a precision filter on the recall side. A cheap ranker has surfaced durable cc-notes "
    "records (notes, docs, logs) anchored to a file the agent just touched. The ranker over-selects "
    "on purpose; your job is to keep the ones worth putting in front of the agent right now and drop "
    "only the clearly irrelevant.\n"
    "\n"
    "Bias hard toward surfacing: a missing piece of durable context costs far more than one extra "
    "line the agent skims past. Drop a record only when its title and match reasons make it plainly "
    "unrelated to this file. When in doubt, keep it.\n"
    "\n"
    "Return the ids to surface, as a subset of the candidate ids given."
)


class SurfacePick(BaseModel):
    """The surface filter's verdict: which candidate record ids are worth surfacing now.

    ``ids`` defaults empty; the caller intersects it with the candidate set, so a
    degenerate parse surfaces nothing rather than inventing ids. The handlers fail
    OPEN around the call — an error surfaces every candidate — so an empty list only
    ever means the model deliberately dropped them all.
    """

    ids: list[str] = []


def unseen_entries(evt: BaseHookEvent, entries: list[dict[str, Any]], *, scope: str) -> list[dict[str, Any]]:
    """Fresh entries (not yet surfaced under scope), marking every candidate before the LLM filters."""
    fresh = set(evt.ctx.s.unseen([entry_payload(e)["id"] for e in entries], scope=scope))
    return [e for e in entries if entry_payload(e)["id"] in fresh]


def surface_filter(evt: PostToolUseEvent, fresh: list[dict[str, Any]], *, touched: str) -> list[dict[str, Any]]:
    """Pick which freshly-recalled records to surface, biased toward surfacing.

    A lone candidate (or none) surfaces directly — there is nothing to choose, so no
    model call is paid. For two or more, a small non-agentic LLM keeps the worthwhile
    subset; the caller has already marked every ``fresh`` id judged, so each record is
    filtered at most once per session. Fails OPEN: any classifier error returns the
    full ``fresh`` set, because a broken filter must never swallow durable context.
    """
    if len(fresh) <= 1:
        return fresh
    lines = {entry_payload(e)["id"]: render_note_lines([e])[0] for e in fresh}
    prompt = (
        Prompt()
        .system(SURFACE_FILTER_SYSTEM)
        .context("touched-file", str(evt.file))
        .context("how-touched", touched)
        .context("candidates", "\n".join(f"{eid}\t{line}" for eid, line in lines.items()))
        .ask("Which candidate ids are worth surfacing now? Keep all but the clearly irrelevant.")
    )
    try:
        pick = evt.ctx.call_llm(prompt, response_model=SurfacePick, model="small", agent=False, transcript=False)
    except Exception:
        # Fail OPEN: a recall-side filter that errors must show everything, never hide
        # context by breaking — the inverse of the record side's fail-closed silence.
        return fresh
    chosen = set(pick.ids) & set(lines)
    return [e for e in fresh if entry_payload(e)["id"] in chosen]


@on(
    Event.PostToolUse,
    only_if=[Tool("Read"), CcNotesAvailable()],
    tests={
        # A non-Read tool never matches the Tool gate. The firing path needs a stubbed
        # CLI, so it lives in tests/test_cc_notes.py.
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def float_note_context(evt: PostToolUseEvent) -> Any:
    """Surface the notes, docs, and logs relevant to a freshly read file, once per id per session.

    Recall runs ``cc-notes relevant <path> --json``; the precision step
    (:func:`surface_filter`) keeps the worthwhile subset. :func:`unseen_entries` marks
    every fresh id judged before filtering, so a record is weighed at most once a session.
    Silent when recall empties or the filter keeps nothing.
    """
    if not evt.file:
        return None
    entries = parse_relevant(run_cc_notes(evt, "relevant", str(evt.file), "--json"))
    fresh = unseen_entries(evt, entries, scope="floated")
    if not fresh:
        return None
    picked = surface_filter(evt, fresh, touched="read")
    if not picked:
        return None
    return evt.warn(
        f"You read {evt.file} — durable cc-notes records you should know "
        "(git-synced context, never in the working tree):",
        *render_note_lines(picked),
    )


@on(
    Event.PostToolUse,
    only_if=[Tool("Edit|Write|MultiEdit"), CcNotesAvailable()],
    tests={
        # A Read never matches the Edit|Write|MultiEdit gate. The firing path needs a
        # stubbed CLI, so it lives in tests/test_cc_notes.py.
        Input(tool="Read", file="m.py"): Allow(),
    },
)
def check_note_staleness(evt: PostToolUseEvent) -> Any:
    """Surface drifted records anchored to a path an edit just touched, for reconciliation.

    Recall runs ``cc-notes relevant <path> --attached --worktree --json``;
    :func:`filter_drifted` keeps only entries with a non-null drift verdict (a log never
    drifts), then :func:`surface_filter` keeps the subset worth a look. :func:`unseen_entries`
    marks every fresh id judged before filtering, so a record is weighed at most once a
    session — under a distinct ``stale`` scope, so a read-time float never suppresses the
    edit-time warning for the same id. Docs render pointer-only, never the body.
    """
    if not evt.file:
        return None
    entries = parse_relevant(run_cc_notes(evt, "relevant", str(evt.file), "--attached", "--worktree", "--json"))
    drifted = filter_drifted(entries)
    fresh = unseen_entries(evt, drifted, scope="stale")
    if not fresh:
        return None
    picked = surface_filter(evt, fresh, touched="edited")
    if not picked:
        return None
    return evt.warn(
        f"You edited {evt.file} — durable cc-notes records anchored here look out of date. "
        "Reconcile each against its kind — `verify <id>` to re-confirm it against HEAD, `edit <id>` "
        "to revise it, `supersede <old> --by <new>` to replace it, or `expire <id>` to flag it "
        "out-of-date: for a note use `cc-notes note verify/edit/supersede/expire`, "
        "for a doc use `cc-notes doc verify/edit/supersede/expire`.",
        *render_note_lines(picked),
    )


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit|MultiEdit"), MemoryWrite(), CcNotesAvailable()],
    tests={
        # Each case is silent without a real file or CLI (wrong tool, non-memory path,
        # the MEMORY.md index, or a memory path with no file on disk). The create/update/
        # skip litmus needs an on-disk file + stubbed CLI, so it lives in tests/test_cc_notes.py.
        Input(tool="Read", file="/n/.cc-pool/p/memory/x.md"): Allow(),
        Input(tool="Write", file="internal/store/store.go", content="x = 1\n"): Allow(),
        Input(tool="Write", file="/n/.cc-pool/p/memory/MEMORY.md", content="# Memory Index\n"): Allow(),
        Input(tool="Write", file="/n/.cc-pool/p/memory/x.md", content="---\ntype: feedback\n---\nbody\n"): Allow(),
    },
)
def mirror_memory_to_note(evt: PostToolUseEvent) -> Any:
    """Mirror a freshly written cc-pool memory file into a durable cc-notes note.

    The memory write goes through untouched — this is a PostToolUse side-effect that
    additionally captures repo-relevant memories (``feedback``/``project``/
    ``reference``; ``user`` who-you-are memories are skipped) as git-synced,
    drift-checked notes. The note is keyed by a stable ``memory:<slug>`` tag: the
    first write creates it, later writes edit the same note in place (skipping the
    edit when title and body are unchanged), so a memory and its note stay
    one-to-one. Every cc-notes failure falls closed to silence — a mirror that can't
    write must never disturb the memory write that already landed.
    """
    parsed = parse_memory_file(Path(str(evt.file)))
    if parsed is None or parsed.type not in MIRRORED_MEMORY_TYPES:
        return None
    slug = evt.file.stem
    title = parsed.title or slug.replace("-", " ")
    existing = memory_notes(evt, slug)
    if existing:
        note = existing[0]
        if note.get("title", "") == title and note.get("body", "") == parsed.body:
            return None
        if run_cc_notes(evt, "note", "edit", note.get("id", ""), f"--title={title}", f"--body={parsed.body}") is None:
            return None
        note_id, action = note.get("id", ""), "updated"
    else:
        out = run_cc_notes(
            evt,
            "note",
            "add",
            "--json",
            f"--body={parsed.body}",
            "--tag",
            "memory",
            "--tag",
            f"memory:{slug}",
            "--tag",
            f"memory-type:{parsed.type}",
            "--",
            title,
        )
        if out is None:
            return None
        note_id, action = note_id_of(out), "created"
    return evt.warn(
        f"Mirrored memory '{slug}' → durable cc-notes note {short_id(note_id)} ({action}), "
        f"tagged `memory` / `memory:{slug}`. Run `cc-notes sync` to share it.",
    )


RECORD_ROUTER_SYSTEM = (
    "You are a precision filter. A cheap static rule has already flagged a file an agent just "
    "wrote as POSSIBLY durable internal knowledge — content that belongs in cc-notes (git objects "
    "on refs/cc-notes/*, synced with the repo but never in the working tree) rather than as a loose "
    "file in the public tree. The static rule over-selects on purpose; your job is to confirm the "
    "write is genuinely durable internal knowledge and, when it is, route it to the right cc-notes "
    "record.\n"
    "\n"
    "Set record=false when the file is genuinely human-facing or published project documentation "
    "that belongs in the repo tree — a README, a user guide, a tutorial, API reference, a released "
    "changelog, a blog post, release notes, or a spec written for people — or when it is throwaway "
    "scratch with no durable value. When it could plausibly be either, answer record=false. Only a "
    "clear case records.\n"
    "\n"
    "When record=true, choose exactly one kind:\n"
    "- note: a single durable fact or decision — one verifiable claim about the code (e.g. 'retry "
    "backoff caps at 30s because the server drops connections past it').\n"
    "- doc: living, long-form guidance for the next agent that you keep fresh — a handoff brief, a "
    "runbook, design rationale for an in-flight change, an investigation write-up. A doc is "
    "re-verified, drifts when the code moves, and carries a 'read this when…' trigger.\n"
    "- log: an immutable, append-only chronology — an incident timeline, a rollout log, a debugging "
    "session. Its value is the running record itself; entries are never edited and it has no "
    "freshness lifecycle.\n"
    "- task: actionable work still to be done — a TODO or checklist of follow-ups.\n"
    "\n"
    "doc vs log is the subtle call: choose doc when the content is guidance you would keep current, "
    "log when it is a dated record of what happened that you would only ever append to.\n"
    "\n"
    "When record=true also return: title — a short title; when — for a doc, the free-text 'read "
    "this when…' trigger (leave empty for other kinds); area — the repo directory the record is "
    "about (e.g. internal/api), or '.' if unclear; reasoning — one line explaining the call."
)


class RecordVerdict(BaseModel):
    """The router's verdict: whether a freshly written file is durable cc-notes content, as which kind.

    ``record`` defaults False so a degenerate or empty model parse fails closed to
    silence. ``kind`` is one of :data:`RECORD_KINDS`; the remaining fields seed the
    suggested ``cc-notes <kind> add`` command and are only meaningful when ``record``
    is true.
    """

    record: bool = False
    kind: str = ""
    title: str = ""
    when: str = ""
    area: str = ""
    reasoning: str = ""


RECORD_KINDS = ("note", "doc", "log", "task")


def record_command(kind: str, title: str, when: str, area: str) -> list[str]:
    """The cc-notes command line(s) that record content under the router's chosen kind.

    A log takes no body at creation — ``log add`` opens the journal and ``log append``
    grows it — so it renders as two lines; the others are a single ``add`` piping the
    file in through ``--body -``.
    """
    dir_flag = f" --dir {area}" if area and area != "." else ""
    if kind == "doc":
        return [f'cc-notes doc add "{title}" --when "{when}"{dir_flag} --body -']
    if kind == "log":
        return [
            f'cc-notes log add "{title}"{dir_flag}',
            "cc-notes log append <id>   # then add the chronology one entry at a time",
        ]
    if kind == "task":
        return [f'cc-notes task add "{title}"   # add --backlog if it is shared work']
    return [f'cc-notes note add "{title}"{dir_flag} --body -']


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit|MultiEdit"), DurableInternalWrite(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        # Each case is silent under the default call_llm stub (record=False): a rejected
        # path never reaches the LLM, a matched path's stubbed verdict doesn't record. The
        # firing / kind-routing split needs a record=True stub, in tests/test_cc_notes.py.
        Input(tool="Write", file="HANDOFF.md", content="## Status\nHandoff\n## Remaining\n- [ ] x\n"): Allow(),
        Input(tool="Write", file="README.md", content="# Readme\nsome prose\n"): Allow(),
        Input(tool="Write", file="src/foo.ts", content="export const x = 1\n"): Allow(),
        Input(tool="Write", file=".env", content="API_KEY=secret\n"): Allow(),
        Input(tool="Write", file="/n/.cc-pool/p/memory/x.md", content="---\ntype: feedback\n---\nbody\n"): Allow(),
        Input(tool="Read", file="HANDOFF.md"): Allow(),
    },
)
def nudge_record_durable(evt: PostToolUseEvent) -> Any:
    """Record-route a write the static gate flagged as possibly durable internal knowledge.

    The cheap, over-selective :class:`DurableInternalWrite` condition is the recall gate;
    this handler is the precision step — it asks the LLM whether the write is genuinely
    durable cc-notes content and, if so, which record: note (a fact), doc (living
    guidance), log (an append-only chronology), or task (actionable work). On a positive
    verdict it warns with the exact ``cc-notes <kind> add`` to run. One nudge per turn
    (``fired_this_turn``); fails closed to silence on any classifier error — a nudge,
    never a gate. The cc-pool memory tree is excluded upstream by the gate, so the mirror
    owns those writes alone.
    """
    if fired_this_turn(evt):
        return None
    prompt = (
        Prompt()
        .system(RECORD_ROUTER_SYSTEM)
        .context("path", str(evt.file))
        .context("content", (evt.content or "")[:LLM_INPUT_CAP])
        .ask("Does this belong in cc-notes, and if so as which record (note/doc/log/task)?")
    )
    try:
        verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    except Exception:
        # Fail closed: a classifier error (network, timeout, bad parse) must never crash a
        # nudge fire — the pack only ever warns, it never blocks.
        return None
    if not verdict.record or verdict.kind not in RECORD_KINDS:
        return None
    record_fire(evt)
    title = verdict.title or (evt.file.stem if evt.file else "untitled")
    return evt.warn(
        f"{evt.file} reads like durable {verdict.kind} content for cc-notes, not a loose file in "
        f"the working tree ({verdict.reasoning}). Record it, then delete the loose file:",
        *record_command(verdict.kind, title, verdict.when, verdict.area),
        "(Don't put secrets in cc-notes — the refs sync to the remote.)",
    )


PLAN_TASKS_SYSTEM = (
    "An agent just had a plan approved. Extract only the work items that are DURABLE — work that "
    "outlives this session, or that another agent might pick up or coordinate on — and worth tracking "
    "as a cc-notes task. Skip the moment-to-moment implementation steps the agent does right now and "
    "checks off as it goes; those belong in the private native todo list, not cc-notes.\n"
    "\n"
    "Prefer a few high-value items over a long list, and return an empty list when the plan is "
    "throwaway or entirely in-session mechanics. For each item set title (a short imperative) and "
    "shared=true when any agent could pick it up — it belongs on the shared backlog — rather than "
    "being tied to this agent's current branch."
)

# The canonical native-vs-durable teaching, in the same terms as the README table and SKILL.md.
PLAN_TEACH = (
    "Plan approved. Native TaskCreate/TaskUpdate is your private scratchpad — it vanishes at session "
    "end. Durable work that outlives the session or coordinates agents goes in `cc-notes task`: "
    "`--backlog` for shared work any agent can claim, plain `cc-notes task add` for your branch. "
    "(A decision or durable fact is a `cc-notes note add`; living guidance for the next agent, with a "
    "`--when` read-trigger, is a `cc-notes doc add`; an append-only chronology whose entries are never "
    "edited is a `cc-notes log add`.)"
)


class PlanTask(BaseModel):
    """One durable work item the plan router lifts out of an approved plan."""

    title: str = ""
    shared: bool = False


class PlanTasks(BaseModel):
    """The plan router's verdict: the few durable work items worth a cc-notes task.

    Defaults to an empty list so a degenerate parse or a throwaway plan suggests
    nothing — the deterministic teach still stands on its own.
    """

    tasks: list[PlanTask] = []


def plan_text(evt: PostToolUseEvent) -> str | None:
    """The approved plan's text — the plan file when readable, else the inline plan, else None.

    The ExitPlanMode tool input carries both ``planFilePath`` and an inline ``plan``;
    the file is authoritative, so it is preferred and the inline copy is the fallback.
    Returns None when neither yields text, and the caller then fires the teach without
    any extracted tasks.
    """
    ti = evt._tool_input
    path = ti.get("planFilePath")
    if isinstance(path, str) and path:
        try:
            text = Path(path).read_text(encoding="utf-8").strip()
        except OSError:
            text = ""
        if text:
            return text
    inline = ti.get("plan")
    return inline.strip() if isinstance(inline, str) and inline.strip() else None


def plan_task_commands(evt: PostToolUseEvent, text: str | None) -> list[str]:
    """The ``cc-notes task add`` lines for the durable items a small LLM lifts from the plan.

    Returns [] when there is no plan text, the model finds nothing durable, or it
    errors — the teach carries the nudge on its own in every case. Caps at five so a
    sprawling plan can't produce a wall of commands.
    """
    if not text:
        return []
    prompt = (
        Prompt()
        .system(PLAN_TASKS_SYSTEM)
        .context("plan", text[:LLM_INPUT_CAP])
        .ask("Which few items from this plan are durable work worth a cc-notes task? None if it is all in-session steps.")
    )
    try:
        extracted = evt.ctx.call_llm(prompt, response_model=PlanTasks, model="small", agent=False, transcript=False)
    except Exception:
        return []
    commands = []
    for task in extracted.tasks[:5]:
        title = task.title.strip()
        if title:
            commands.append(f'cc-notes task add "{title}"' + (" --backlog" if task.shared else ""))
    return commands


@on(
    Event.PostToolUse,
    only_if=[Tool("ExitPlanMode"), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="ExitPlanMode"): Warn(pattern="cc-notes task add"),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def nudge_plan_tasks(evt: PostToolUseEvent) -> Any:
    """On plan approval, teach the native-vs-durable line and route the plan's durable items to tasks.

    The teach is deterministic and always fires; the extracted ``cc-notes task add``
    lines are the LLM precision step (:func:`plan_task_commands`). Deduped per
    ``planFilePath`` so re-approving the same plan stays silent while a genuinely new plan
    fires again; a plan with no path isn't deduped and ``max_fires`` caps the repeats.
    Fails safe: a classifier error drops only the extracted tasks, never the teach.
    """
    text = plan_text(evt)
    path = evt._tool_input.get("planFilePath")
    if isinstance(path, str) and path and not evt.ctx.s.once(path, scope="plan"):
        return None
    lines = [PLAN_TEACH]
    if commands := plan_task_commands(evt, text):
        lines.append("These items from your plan look like durable work — capture them:")
        lines.extend(commands)
    return evt.warn(*lines)


COMMIT_DECISION_SYSTEM = (
    "An agent just landed a git commit. Decide whether the change embodies a durable DECISION worth "
    "capturing as a cc-notes record — a design choice, a tradeoff, a non-obvious rationale a future "
    "agent would want explained — as opposed to routine, self-explanatory work (a rename, a "
    "dependency bump, a formatting pass, a mechanical fix) that the diff and message already cover.\n"
    "\n"
    "Set record=false for routine or self-explanatory commits — that is most commits. Only a commit "
    "that encodes a decision worth preserving records. When record=true choose the kind: note for a "
    "single durable fact or decision (one verifiable claim), doc for longer living rationale a future "
    "agent should read before touching this area. Return title (short), when (for a doc, the 'read "
    "this when…' trigger; empty for a note), area (the repo directory, or '.'), reasoning (one line)."
)


def commit_decision(evt: PostToolUseEvent) -> list[str]:
    """The 'capture this decision' lines for the HEAD commit, or [] when none is warranted.

    Pulls the HEAD commit's bounded diff via ``evt.ctx.diff(commit="HEAD")`` and asks a
    small non-agentic LLM whether it encodes a durable decision; on a note/doc verdict it
    returns the framing line plus the exact ``cc-notes <kind> add``. An empty diff, a git
    error (including a diff timeout, which the primitive's git fallback does not swallow),
    or any classifier error returns [] — the deterministic link/sync reminder stands on its
    own, so a missing suggestion never matters. Only note/doc are routed here: a commit
    captures a decision, not a chronology or a task.
    """
    try:
        diff = evt.ctx.diff(commit="HEAD")
        if not diff:
            return []
        prompt = (
            Prompt()
            .system(COMMIT_DECISION_SYSTEM)
            .context("commit", diff)
            .ask("Does this commit encode a durable decision worth a cc-notes note or doc?")
        )
        verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    except Exception:
        return []
    if not verdict.record or verdict.kind not in ("note", "doc"):
        return []
    title = verdict.title or "the decision behind this commit"
    return [
        f"This commit encodes a durable {verdict.kind} ({verdict.reasoning}) — capture it:",
        *record_command(verdict.kind, title, verdict.when, verdict.area),
    ]


@on(
    Event.PostToolUse,
    only_if=[Command(GIT_COMMIT), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="git commit -m 'add retry ceiling'"): Warn(pattern="cc-task:"),
        Input(command="git commit --amend"): Warn(pattern="cc-notes sync"),
        Input(command="git status"): Allow(),
    },
)
def nudge_commit_record(evt: PostToolUseEvent) -> Any:
    """After a commit, remind to link + sync, and route any durable decision to a note/doc.

    The link/sync reminder is deterministic workflow coordination and always fires; the
    decision suggestion is the LLM precision step (:func:`commit_decision`). Deduped per
    HEAD sha so each commit is judged once — an amend (new sha) gets a fresh look, a no-op
    re-fire on the same HEAD stays silent; a sha-less git failure still fires the reminder.
    Fails safe: a git or classifier error drops only the suggestion, never the reminder.
    """
    try:
        sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    except Exception:
        sha = ""
    if sha and not evt.ctx.s.once(sha, scope="commit"):
        return None
    return evt.warn(
        "Commit landed. Link it to its task with a `cc-task: <id>` trailer (queryable via "
        "`git log --grep`, `cc-notes blame <sha>`, and `cc-notes history <id>`), then "
        "`cc-notes sync` to share your refs.",
        *commit_decision(evt),
    )


nudge(
    "A merged branch's still-open tasks stay on that branch until you carry them "
    "over. Run `cc-notes reconcile --into <target>` to set them onto the target, "
    "then `cc-notes sync` to converge with the remote. Both are idempotent. "
    "(jj merges never fire git hooks — reconcile is the explicit step.)",
    only_if=[Command(GIT_MERGE_PULL), CcNotesAvailable()],
    events=Event.PostToolUse,
    max_fires=NUDGE_MAX_FIRES,
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
    max_fires=NUDGE_MAX_FIRES,
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
    max_fires=NUDGE_MAX_FIRES,
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
