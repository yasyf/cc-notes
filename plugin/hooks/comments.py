"""Redirect a verbose comment's rationale to cc-notes.

Rides along with the general pack's verbose-comment deny (declarative warn, so it survives the
block that a nudge would be skipped behind), pointing durable rationale at a cc-notes record.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    FileFixture,
    Input,
    Tool,
    Warn,
    hook,
)
from captain_hook.ast_grep import lang_for_path, touched_comment_blocks

from .common import CcNotesAvailable

if TYPE_CHECKING:
    from captain_hook.ast_grep import CommentBlock

REDIRECT_MESSAGE = (
    "That verbose comment reads like durable rationale. Record it where it outlives the "
    "file — `cc-notes note add` for a decision or fact, `cc-notes doc add --when "
    "'<read this when…>'` for living guidance — then keep the comment to one terse pointer "
    "at most. (No secrets: cc-notes refs sync to the remote.)"
)

# Fixtures for the inline tests — module constants so each physical line stays short.
PY_SIX_RUN = (
    "# rationale line one goes here\n# rationale line two goes here\n"
    "# rationale line three goes here\n# rationale line four goes here\n"
    "# rationale line five goes here\n# rationale line six goes here\nx = 1\n"
)
PY_TWO_RUN = "# short note one line\n# short note two line\nx = 1\n"
PY_GROW_OLD_FILE = "# a here\n# b here\n# c here\nx = 1\n"
PY_GROW_OLD = "# a here\n# b here\n# c here"
PY_GROW_NEW = "# a here\n# b here\n# c here\n# d here\n# e here\n# f here"
PY_NEAR_FILE = "# a here\n# b here\n# c here\n# d here\n# e here\n# f here\nx = 1\n"
YAML_HASH = "# a\n# b\n# c\n# d\n# e\n# f\n# g\n# h\n# i\n# j\n"


def touched(evt: BaseHookEvent) -> list[CommentBlock]:
    """The comment blocks this edit created or grew, or ``[]`` when the language is unparsable."""
    if (
        not (file := evt.file)
        or not (lang := lang_for_path(file.path))
        or (pre := evt.pre_image) is None
        or (post := evt.post_image) is None
    ):
        return []
    return touched_comment_blocks(pre, post, lang)


class VerboseCommentIntroduced(CustomCondition):
    """True when the edit leaves a too-long comment block it created or grew — doc or inline both
    count, since rationale-stuffed doc comments are equally cc-notes material."""

    def check(self, evt: BaseHookEvent) -> bool:
        return any(block.too_long for block in touched(evt))


hook(
    Event.PreToolUse,
    REDIRECT_MESSAGE,
    only_if=[Tool("Edit", "Write", "MultiEdit"), VerboseCommentIntroduced(), CcNotesAvailable()],
    tests={
        # A too-long run an edit creates or grows draws the redirect — inline or doc-shaped alike.
        Input(file="vc_write.py", content=PY_SIX_RUN): Warn(pattern="cc-notes"),
        Input(file=FileFixture(name="vc_grow.py", content=PY_GROW_OLD_FILE), old=PY_GROW_OLD, content=PY_GROW_NEW): Warn(
            pattern="cc-notes"
        ),
        # A short run, an unparsable language, and a code-only edit beside an untouched legacy run stay quiet.
        Input(file="vc_short.py", content=PY_TWO_RUN): Allow(),
        Input(file="vc.yaml", content=YAML_HASH): Allow(),
        Input(file=FileFixture(name="vc_near.py", content=PY_NEAR_FILE), old="x = 1", content="x = 2"): Allow(),
    },
)
