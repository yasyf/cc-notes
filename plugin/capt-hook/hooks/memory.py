
from __future__ import annotations

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
    clamp_title,
    in_cc_pool_memory,
    json_field,
    parse_tasks,
    run_cc_notes,
)

MIRRORED_MEMORY_TYPES = ("feedback", "project", "reference")

MEMORY_FRONTMATTER = re.compile(r"\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*\r?\n?(.*)\Z", re.DOTALL)


class ParsedMemory(NamedTuple):

    type: str
    title: str
    body: str


def parse_memory_file(path: Path) -> ParsedMemory | None:
    m = MEMORY_FRONTMATTER.match(path.read_text(encoding="utf-8"))
    if not m:
        return None
    front, body = m.group(1), m.group(2)
    return ParsedMemory(
        type=front_field(front, "type", indented=True),
        title=front_field(front, "description"),
        body=body.strip(),
    )


def front_field(front: str, key: str, *, indented: bool = False) -> str:
    indent = r"[ \t]*" if indented else ""
    m = re.search(rf"(?m)^{indent}{re.escape(key)}:[ \t]*(.*)$", front)
    if not m:
        return ""
    val = m.group(1).strip()
    if len(val) >= 2 and val[0] in "\"'" and val[-1] == val[0]:
        val = val[1:-1]
    return val


def memory_notes(evt: PostToolUseEvent, slug: str) -> list[dict[str, Any]]:
    return parse_tasks(run_cc_notes(evt, "note", "list", "--label", f"memory:{slug}", "--json"))


def note_body(evt: PostToolUseEvent, note_id: str) -> str:
    return json_field(run_cc_notes(evt, "note", "show", note_id, "--json"), "body")


class MemoryWrite(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        if evt.file is None:
            return False
        p = Path(str(evt.file))
        return in_cc_pool_memory(p) and p.suffix == ".md" and p.name != "MEMORY.md"


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit|MultiEdit"), MemoryWrite(), CcNotesAvailable()],
    tests={
        Input(tool="Read", file="/n/.cc-pool/p/memory/x.md"): Allow(),
        Input(tool="Write", file="internal/store/store.go", content="x = 1\n"): Allow(),
        Input(tool="Write", file="/n/.cc-pool/p/memory/MEMORY.md", content="# Memory Index\n"): Allow(),
    },
)
def mirror_memory_to_note(evt: PostToolUseEvent) -> HookResult | None:
    parsed = parse_memory_file(Path(str(evt.file)))
    if parsed is None or parsed.type not in MIRRORED_MEMORY_TYPES:
        return None
    slug = evt.file.stem
    title = clamp_title(parsed.title or slug.replace("-", " "))
    existing = memory_notes(evt, slug)
    if existing:
        note_id = existing[0].get("id", "")
        if existing[0].get("title", "") == title and note_body(evt, note_id) == parsed.body:
            return None
        if run_cc_notes(evt, "note", "edit", note_id, f"--title={title}", f"--body={parsed.body}") is None:
            return None
        action = "updated"
    else:
        out = run_cc_notes(
            evt,
            "note",
            "add",
            "--json",
            f"--body={parsed.body}",
            "--label",
            "memory",
            "--label",
            f"memory:{slug}",
            "--label",
            f"memory-type:{parsed.type}",
            "--",
            title,
        )
        if out is None:
            return None
        action = "created"
    return evt.warn(
        f"Mirrored memory `{slug}` to a durable cc-notes note ({action}). Run `cc-notes sync` to share it.",
    )
