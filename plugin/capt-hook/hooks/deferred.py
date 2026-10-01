
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
    if not evt.ctx.s.load(DeferredNotices).messages:
        return None
    with evt.ctx.s[DeferredNotices].mutate() as state:
        staged, state.messages = state.messages, []
    return evt.warn(*staged) if staged else None
