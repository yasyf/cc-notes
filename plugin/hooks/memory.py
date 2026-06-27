"""The memory mirror: an automatic side-effect that captures cc-pool memory files as notes."""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any, NamedTuple

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    HookResult,
    Input,
    PostToolUseEvent,
    Tool,
    on,
)

from .common import (
    CcNotesAvailable,
    in_cc_pool_memory,
    run_cc_notes,
    short_id,
)

MIRRORED_MEMORY_TYPES = ("feedback", "project", "reference")

# Frontmatter fenced by `---` then a markdown body.
MEMORY_FRONTMATTER = re.compile(r"\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*\r?\n?(.*)\Z", re.DOTALL)


class ParsedMemory(NamedTuple):
    """A cc-pool memory file split into the fields the mirror needs."""

    type: str
    title: str
    body: str


def parse_memory_file(path: Path) -> ParsedMemory | None:
    # Reads from disk so a Write and an Edit both see the final merged content.
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return None
    m = MEMORY_FRONTMATTER.match(text)
    if not m:
        return None
    front, body = m.group(1), m.group(2)
    return ParsedMemory(
        type=front_field(front, "type", indented=True),
        title=front_field(front, "description"),
        body=body.strip(),
    )


def front_field(front: str, key: str, *, indented: bool = False) -> str:
    # The anchored `^[ \t]*type:` can't match mid-line inside a same-suffixed sibling
    # like `node_type:`; `indented` allows the leading whitespace of a nested key.
    indent = r"[ \t]*" if indented else ""
    m = re.search(rf"(?m)^{indent}{re.escape(key)}:[ \t]*(.*)$", front)
    if not m:
        return ""
    val = m.group(1).strip()
    if len(val) >= 2 and val[0] in "\"'" and val[-1] == val[0]:
        val = val[1:-1]
    return val


def memory_notes(evt: PostToolUseEvent, slug: str) -> list[dict[str, Any]]:
    out = run_cc_notes(evt, "note", "list", "--tag", f"memory:{slug}", "--json")
    if not out or not out.strip():
        return []
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return []
    return [n for n in parsed if isinstance(n, dict)] if isinstance(parsed, list) else []


def note_id_of(out: str | None) -> str:
    if not out or not out.strip():
        return ""
    try:
        parsed = json.loads(out)
    except json.JSONDecodeError:
        return ""
    return parsed.get("id", "") if isinstance(parsed, dict) else ""


class MemoryWrite(CustomCondition):
    """Matches a write to a cc-pool agent-memory file."""

    def check(self, evt: BaseHookEvent) -> bool:
        if evt.file is None:
            return False
        p = Path(str(evt.file))
        return in_cc_pool_memory(p) and p.suffix == ".md" and p.name != "MEMORY.md"


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
def mirror_memory_to_note(evt: PostToolUseEvent) -> HookResult | None:
    """Mirror a freshly written cc-pool memory file into a durable cc-notes note."""
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
        # Fail closed to silence — a mirror that can't write must never disturb the
        # memory write that already landed.
        if out is None:
            return None
        note_id, action = note_id_of(out), "created"
    return evt.warn(
        f"Mirrored memory '{slug}' → durable cc-notes note {short_id(note_id)} ({action}), "
        f"tagged `memory` / `memory:{slug}`. Run `cc-notes sync` to share it.",
    )
