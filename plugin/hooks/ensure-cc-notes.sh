#!/bin/sh
set -eu

is_current() {
  command -v cc-notes >/dev/null 2>&1 || return 1
  raw="$(cc-notes version 2>/dev/null)" || return 1
  version="${raw%% *}"
  version="${version#v}"
  case "$version" in
    *[!0-9.]* | "") return 1 ;;
  esac
  printf '%s\n' "$version" | awk -F. '
    NF == 3 && $1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ {
      exit !(($1 + 0) > 0 || (($1 + 0) == 0 && ($2 + 0) >= 45))
    }
    { exit 1 }
  '
}

if is_current; then
  exit 0
fi
curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh
