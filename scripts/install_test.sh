#!/usr/bin/env bash
# Unit-style harness for install.sh. Fakes `uname` and `curl` on PATH and points
# the downloader at a local fixture binary, then asserts the platform -> asset
# mapping, checksum verification, and the version no-op re-run. Run manually:
#
#   scripts/install_test.sh
#
# Deliberately NOT wired into `go test`: it shells out and stubs PATH, which has
# nothing to do with the Go module.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL="$ROOT/scripts/install.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/stub"

# Fixture "binary": prints a fixed version line so the no-op path can match.
cat > "$WORK/fixture-binary" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FIXTURE_COMMAND_LOG"
[ "$1" = "version" ] && echo "v0.9.9 (deadbee)"
exit 0
EOF
chmod +x "$WORK/fixture-binary"

# uname stub: answers from FAKE_OS / FAKE_ARCH.
cat > "$WORK/stub/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -s) echo "${FAKE_OS:-Linux}" ;;
  -m) echo "${FAKE_ARCH:-x86_64}" ;;
  *) echo "${FAKE_OS:-Linux}" ;;
esac
EOF
chmod +x "$WORK/stub/uname"

cat > "$WORK/stub/brew" <<'EOF'
#!/bin/sh
[ "${FAKE_BREW_SUCCESS:-0}" = 1 ]
EOF
chmod +x "$WORK/stub/brew"

cat > "$WORK/stub/cc-notes" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FIXTURE_COMMAND_LOG"
[ "$1" = "version" ] && echo "v0.9.9 (deadbee)"
exit 0
EOF
chmod +x "$WORK/stub/cc-notes"

# curl stub: resolves "latest" to FAKE_TAG and "downloads" by copying the
# fixture, logging the requested asset URL to REQUESTED_LOG.
cat > "$WORK/stub/curl" <<'EOF'
#!/bin/sh
url=""
out=""
prev=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  url="$a"
  prev="$a"
done
case "$*" in
  *url_effective*)
    printf '%s' "https://github.com/yasyf/cc-notes/releases/tag/${FAKE_TAG:-v0.9.9}"
    ;;
  *SHA256SUMS.txt*)
    if command -v sha256sum >/dev/null 2>&1; then
      hash="$(sha256sum "$FIXTURE_BIN" | awk '{print $1}')"
    else
      hash="$(shasum -a 256 "$FIXTURE_BIN" | awk '{print $1}')"
    fi
    printf '%s  %s\n' "$hash" "$FAKE_ASSET"
    ;;
  *)
    printf '%s\n' "$url" >> "$REQUESTED_LOG"
    cp "$FIXTURE_BIN" "$out"
    ;;
esac
EOF
chmod +x "$WORK/stub/curl"

REQUESTED_LOG="$WORK/requested"
FIXTURE_COMMAND_LOG="$WORK/fixture-commands"
export REQUESTED_LOG FIXTURE_COMMAND_LOG FAKE_TAG="v0.9.9"
export FIXTURE_BIN="$WORK/fixture-binary"

run_install() { # $1=os $2=arch $3=expected-asset
  : > "$REQUESTED_LOG"
  rm -rf "${WORK:?}/bin"
  PATH="$WORK/stub:$PATH" FAKE_OS="$1" FAKE_ARCH="$2" FAKE_ASSET="$3" CC_NOTES_BIN_DIR="$WORK/bin" \
    "$INSTALL" >/dev/null 2>&1
}

expect() { # $1=os $2=arch $3=expected-asset
  run_install "$1" "$2" "$3"
  if ! grep -q "/$3$" "$REQUESTED_LOG"; then
    echo "FAIL: uname(-s=$1 -m=$2) did not request '$3': $(cat "$REQUESTED_LOG")" >&2
    exit 1
  fi
  echo "ok: $1/$2 -> $3"
}

# Platform mapping.
expect Linux x86_64 cc-notes_linux_amd64
expect Linux aarch64 cc-notes_linux_arm64

# macOS never downloads a helper or CLI directly. The formula-local helper is
# installed and activated only after Homebrew succeeds.
: > "$REQUESTED_LOG"
: > "$FIXTURE_COMMAND_LOG"
PATH="$WORK/stub:$PATH" FAKE_OS=Darwin FAKE_ARCH=arm64 FAKE_BREW_SUCCESS=1 \
  "$INSTALL" >/dev/null 2>&1
if [ -s "$REQUESTED_LOG" ]; then
  echo "FAIL: macOS formula install performed a direct download: $(cat "$REQUESTED_LOG")" >&2
  exit 1
fi
if ! grep -qx 'package install' "$FIXTURE_COMMAND_LOG"; then
  echo "FAIL: macOS formula install did not activate its local helper resource" >&2
  exit 1
fi
echo "ok: macOS uses only the formula-local helper"

if PATH="$WORK/stub:$PATH" FAKE_OS=Darwin FAKE_ARCH=arm64 FAKE_BREW_SUCCESS=0 \
  "$INSTALL" >/dev/null 2>&1; then
  echo "FAIL: macOS install succeeded without Homebrew" >&2
  exit 1
fi
echo "ok: macOS without Homebrew fails closed"

# Re-running at the installed version is a silent no-op (no download).
PATH="$WORK/stub:$PATH" FAKE_OS=Linux FAKE_ARCH=x86_64 CC_NOTES_BIN_DIR="$WORK/bin" \
  FAKE_ASSET=cc-notes_linux_amd64 "$INSTALL" >/dev/null 2>&1
: > "$REQUESTED_LOG"
PATH="$WORK/stub:$PATH" FAKE_OS=Linux FAKE_ARCH=x86_64 CC_NOTES_BIN_DIR="$WORK/bin" \
  FAKE_ASSET=cc-notes_linux_amd64 "$INSTALL" >/dev/null 2>&1
if [ -s "$REQUESTED_LOG" ]; then
  echo "FAIL: re-run downloaded '$(cat "$REQUESTED_LOG")' instead of a no-op" >&2
  exit 1
fi
echo "ok: re-run at installed version is a no-op"

# Unsupported platforms exit non-zero.
if PATH="$WORK/stub:$PATH" FAKE_OS=Plan9 FAKE_ARCH=x86_64 CC_NOTES_BIN_DIR="$WORK/bin" \
  "$INSTALL" >/dev/null 2>&1; then
  echo "FAIL: unsupported OS should have errored" >&2
  exit 1
fi
echo "ok: unsupported OS errors"

if grep -Eq '(^| )((service (install|uninstall))|init)( |$)' "$FIXTURE_COMMAND_LOG"; then
  echo "FAIL: installer implicitly invoked init or changed the service" >&2
  exit 1
fi
echo "PASS: install.sh harness"
