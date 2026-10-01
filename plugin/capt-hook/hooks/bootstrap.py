from __future__ import annotations

import re
import shutil

from captain_hook import Event, SessionStartEvent, on

from .common import run_cc_notes

MIN_VERSION = (0, 55, 0)
INSTALL_URL = "https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh"
_VERSION_RE = re.compile(r"v?(\d+)\.(\d+)\.(\d+)")


def _parse_version(out: str | None) -> tuple[int, int, int] | None:
    if not out or not (m := _VERSION_RE.search(out)):
        return None
    return int(m[1]), int(m[2]), int(m[3])


def _stale(evt: SessionStartEvent) -> bool:
    version = _parse_version(run_cc_notes(evt, "version"))
    return version is None or version < MIN_VERSION


def _install(evt: SessionStartEvent) -> None:
    evt.ctx.call_cli(["sh", "-c", f"curl -fsSL {INSTALL_URL} | sh"], timeout=120, throw=False)


@on(Event.SessionStart, async_=True)
def ensure_cc_notes_binary(evt: SessionStartEvent) -> None:
    if evt.source in ("startup", "resume") and (shutil.which("cc-notes") is None or _stale(evt)):
        _install(evt)
