"""Surface (pull) direction: recall durable records anchored to a touched file, LLM-filter, float."""

from __future__ import annotations

from typing import Any

from captain_hook import (
    Allow,
    Event,
    HookResult,
    Input,
    PostToolUseEvent,
    Prompt,
    Tool,
    on,
)
from pydantic import BaseModel

from .common import (
    CcNotesAvailable,
    entry_payload,
    filter_drifted,
    mcp_active,
    parse_relevant,
    render_note_lines,
    run_cc_notes,
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
    """The surface filter's verdict: which candidate record ids are worth surfacing now."""

    ids: list[str] = []


def unseen_entries(evt: PostToolUseEvent, entries: list[dict[str, Any]], *, scope: str) -> list[dict[str, Any]]:
    fresh = set(evt.ctx.s.unseen([entry_payload(e)["id"] for e in entries], scope=scope))
    return [e for e in entries if entry_payload(e)["id"] in fresh]


def surface_filter(evt: PostToolUseEvent, fresh: list[dict[str, Any]], *, touched: str) -> list[dict[str, Any]]:
    """Pick which freshly-recalled records to surface, biased toward surfacing."""
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
        # Fail OPEN: a recall-side filter that errors must show everything, never hide context.
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
def float_note_context(evt: PostToolUseEvent) -> HookResult | None:
    """Surface the notes, docs, and logs relevant to a freshly read file, once per id per session."""
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
def check_note_staleness(evt: PostToolUseEvent) -> HookResult | None:
    """Surface drifted records anchored to a path an edit just touched, for reconciliation."""
    if not evt.file:
        return None
    entries = parse_relevant(run_cc_notes(evt, "relevant", str(evt.file), "--attached", "--worktree", "--json"))
    drifted = filter_drifted(entries)
    # Distinct `stale` dedup-scope (vs `floated`) so a read-time float never suppresses the
    # edit-time warning for the same id.
    fresh = unseen_entries(evt, drifted, scope="stale")
    if not fresh:
        return None
    picked = surface_filter(evt, fresh, touched="edited")
    if not picked:
        return None
    if mcp_active(evt):
        guidance = (
            f"You edited {evt.file} — durable cc-notes records anchored here look out of date. "
            "Reconcile each against its kind with the MCP tools: re-confirm it against HEAD "
            "(note_verify / doc_verify), revise it (note_edit / doc_edit — pass the full new text "
            "as the body param), replace it (note_supersede / doc_supersede), or flag it out-of-date "
            "(note_expire / doc_expire)."
        )
    else:
        guidance = (
            f"You edited {evt.file} — durable cc-notes records anchored here look out of date. "
            "Reconcile each against its kind — `verify <id>` to re-confirm it against HEAD, `edit <id>` "
            "to revise it, `supersede <old> --by <new>` to replace it, or `expire <id>` to flag it "
            "out-of-date: for a note use `cc-notes note verify/edit/supersede/expire`, "
            "for a doc use `cc-notes doc verify/edit/supersede/expire`. To revise a long "
            "record with your file tools, `edit <id> --checkout` writes it to a file and "
            "`--apply` commits the change."
        )
    return evt.warn(guidance, *render_note_lines(picked))
