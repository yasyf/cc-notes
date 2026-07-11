"""SessionStart bootstrap: install or upgrade the cc-notes binary and ensure the mount."""

from __future__ import annotations

import re
import shutil

from captain_hook import Event, SessionStartEvent, on

from .common import run_cc_notes

# The v0.22.0 flag cutover renamed the pack's CLI flags (--label/--body/--entry), so a cc-notes binary
# older than this rejects the shell-outs the nudges make — reinstall it.
MIN_VERSION = (0, 22, 0)
INSTALL_URL = "https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh"
_VERSION_RE = re.compile(r"v?(\d+)\.(\d+)\.(\d+)")


def _parse_version(out: str | None) -> tuple[int, int, int] | None:
    if not out or not (m := _VERSION_RE.search(out)):
        return None
    return int(m[1]), int(m[2]), int(m[3])


def _install(evt: SessionStartEvent) -> bool:
    evt.ctx.call_cli(["sh", "-c", f"curl -fsSL {INSTALL_URL} | sh"], timeout=120, throw=False)
    return shutil.which("cc-notes") is not None


def _stale(evt: SessionStartEvent) -> bool:
    version = _parse_version(run_cc_notes(evt, "version"))
    return version is None or version < MIN_VERSION


def ensure_mount(evt: SessionStartEvent) -> None:
    # `mount --auto` self-gates on the cc-notes.autoMount config and a fuse-capable binary, adopts a live
    # mount with zero RPC, and is quiet — a no-op in repos that did not opt in, never blocking startup.
    run_cc_notes(evt, "mount", "--auto")


@on(Event.SessionStart, async_=True)
def ensure_cc_notes_binary(evt: SessionStartEvent) -> None:
    """On startup/resume, install or upgrade the cc-notes binary and ensure the mount.

    Dispatched async (fleet-wide `capt-hook run SessionStart --async`), so the harness ignores its
    return value — this runs side-effects only. The once-per-session availability line the agent
    reads lives in the `announce_cc_notes_available` UserPromptSubmit nudge (session.py).
    """
    if evt.source not in ("startup", "resume"):
        return
    if shutil.which("cc-notes") is None:
        if _install(evt):
            ensure_mount(evt)
        return
    if _stale(evt):
        _install(evt)
    ensure_mount(evt)
