#!/bin/sh
# Claude Code SessionStart hook bundled in the cc-notes plugin. On the first
# session where the cc-notes binary is absent, install it (Homebrew-preferred via
# the canonical installer) so the skill and nudges work; a binary older than the
# flag cutover is re-installed, and a current one is an instant no-op. Always exits
# 0 — a failed bootstrap must never break session start; the capt-hook
# prompt_install_cc_notes nudge surfaces the manual command instead.
set -u

# The v0.22.0 flag cutover renamed the pack's CLI flags (--label/--body/--entry), so a
# cc-notes binary older than this rejects the shell-outs the nudges make — re-install it.
MIN_VERSION="0.22.0"

emit() {
  printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"%s"}}\n' "$1"
}

# ensure_mount brings the repo's .notes mount back up at session start when the
# repo opted in (`cc-notes init`, unless --no-mount). `mount --auto` self-gates
# on the cc-notes.autoMount config and on a fuse-capable binary, adopts an
# already-live mount with zero RPC, and is quiet and best-effort — so this is a
# no-op in repos that did not opt in and never blocks session start.
ensure_mount() {
  cc-notes mount --auto >/dev/null 2>&1 || true
}

# install_cc_notes runs the canonical release installer (Homebrew-preferred) and
# confirms the binary landed on PATH. Shared by the absent-binary and stale-binary
# paths so both bootstrap identically.
install_cc_notes() {
  curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh >/dev/null 2>&1 &&
    command -v cc-notes >/dev/null 2>&1
}

# cc_notes_version parses the "cc-notes vX.Y.Z (sha)" line down to X.Y.Z; empty
# output when the version can't be read or parsed.
cc_notes_version() {
  cc-notes version 2>/dev/null | sed -n 's/.*v\([0-9][0-9.]*\).*/\1/p' | head -n1
}

# version_stale is true when its argument is older than MIN_VERSION. An empty or
# unparseable version counts as stale, so a binary we can't read gets re-installed.
version_stale() {
  _v=$1
  [ -n "$_v" ] || return 0
  [ "$_v" = "$MIN_VERSION" ] && return 1
  [ "$(printf '%s\n%s\n' "$_v" "$MIN_VERSION" | sort -V | head -n1)" = "$_v" ]
}

if command -v cc-notes >/dev/null 2>&1; then
  if version_stale "$(cc_notes_version)" && install_cc_notes; then
    ensure_mount
    emit "Upgraded cc-notes to $(cc-notes version 2>/dev/null); its durable task, note, doc, and log tooling is now current."
    exit 0
  fi
  ensure_mount
  emit "cc-notes $(cc-notes version 2>/dev/null) is installed; its durable task, note, doc, and log tooling is available."
  exit 0
fi

if install_cc_notes; then
  ensure_mount
  emit "Installed cc-notes $(cc-notes version 2>/dev/null) on first use; its durable task, note, doc, and log tooling is now available."
  exit 0
fi

exit 0
