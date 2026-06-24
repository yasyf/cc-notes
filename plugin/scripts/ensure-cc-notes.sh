#!/bin/sh
# Claude Code SessionStart hook bundled in the cc-notes plugin. On the first
# session where the cc-notes binary is absent, install it (Homebrew-preferred via
# the canonical installer) so the skill and nudges work; an instant no-op once
# present. Always exits 0 — a failed bootstrap must never break session start; the
# capt-hook prompt_install_cc_notes nudge surfaces the manual command instead.
set -u

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

if command -v cc-notes >/dev/null 2>&1; then
  ensure_mount
  emit "cc-notes $(cc-notes version 2>/dev/null) is installed; its durable task, note, doc, and log tooling is available."
  exit 0
fi

if curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh >/dev/null 2>&1 && command -v cc-notes >/dev/null 2>&1; then
  ensure_mount
  emit "Installed cc-notes $(cc-notes version 2>/dev/null) on first use; its durable task, note, doc, and log tooling is now available."
  exit 0
fi

exit 0
