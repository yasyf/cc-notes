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

if command -v cc-notes >/dev/null 2>&1; then
  emit "cc-notes $(cc-notes version 2>/dev/null) is installed; its durable task/note tooling is available."
  exit 0
fi

if curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh >/dev/null 2>&1 && command -v cc-notes >/dev/null 2>&1; then
  emit "Installed cc-notes $(cc-notes version 2>/dev/null) on first use; its durable task/note tooling is now available."
  exit 0
fi

exit 0
