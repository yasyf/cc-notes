
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
    "A verbose comment is durable rationale that should outlive the file. "
    "Record it with `cc-notes note add` and keep the comment to one terse pointer."
)

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
TXT_HASH = "# a\n# b\n# c\n# d\n# e\n# f\n# g\n# h\n# i\n# j\n"


def touched(evt: BaseHookEvent) -> list[CommentBlock]:
    if (
        not (file := evt.file)
        or not (lang := lang_for_path(file.path))
        or (pre := evt.pre_image) is None
        or (post := evt.post_image) is None
    ):
        return []
    return touched_comment_blocks(pre, post, lang)


class VerboseCommentIntroduced(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return any(block.too_long for block in touched(evt))


hook(
    Event.PreToolUse,
    only_if=[Tool("Edit", "Write", "MultiEdit"), VerboseCommentIntroduced(), CcNotesAvailable()],
    message=REDIRECT_MESSAGE,
    tests={
        Input(file="vc_write.py", content=PY_SIX_RUN): Warn(pattern="cc-notes"),
        Input(file=FileFixture(name="vc_grow.py", content=PY_GROW_OLD_FILE), old=PY_GROW_OLD, content=PY_GROW_NEW): Warn(
            pattern="cc-notes"
        ),
        Input(file="vc_short.py", content=PY_TWO_RUN): Allow(),
        Input(file="vc.txt", content=TXT_HASH): Allow(),
        Input(file=FileFixture(name="vc_near.py", content=PY_NEAR_FILE), old="x = 1", content="x = 2"): Allow(),
    },
)
