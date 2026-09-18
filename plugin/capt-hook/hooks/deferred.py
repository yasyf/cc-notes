"""Advisories a background hook computed, floated by the next synchronous event."""

from __future__ import annotations

from captain_hook import (
    Allow,
    BaseHookEvent,
    Event,
    HookResult,
    Input,
    on,
)
from pydantic import BaseModel


class DeferredNotices(BaseModel):
    """Advisories background hooks staged, oldest first."""

    messages: list[str] = []


def defer(evt: BaseHookEvent, result: HookResult | None) -> None:
    """Stage ``result``'s message for the next event to float, or nothing when there is no result.

    Claude Code never reads a background hook's output, so a hook that moved off the synchronous
    path has to hand its advisory to a hook that is still on one. Every staged message floats:
    each deferring hook already dedups its own subject — the surface hooks through their
    ``unseen`` scopes, the answer capture by recording each answer once — so collapsing here
    would only drop advisories about whatever was new the second time round.
    """
    if result is None or not result.message:
        return
    with evt.ctx.s[DeferredNotices].mutate() as state:
        state.messages.append(result.message)


@on(
    Event.PostToolUse | Event.PostToolUseFailure | Event.UserPromptSubmit,
    max_fires=None,
    tests={
        Input(command="git status"): Allow(),
        Input(prompt="keep going"): Allow(),
    },
)
def float_deferred_notices(evt: BaseHookEvent) -> HookResult | None:
    """Float every advisory a background hook staged since the last event, each exactly once."""
    if not evt.ctx.s.load(DeferredNotices).messages:
        return None
    with evt.ctx.s[DeferredNotices].mutate() as state:
        staged, state.messages = state.messages, []
    return evt.warn(*staged) if staged else None
