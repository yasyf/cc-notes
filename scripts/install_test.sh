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
printf 'signed helper archive fixture\n' > "$WORK/fixture-helper"

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

# Never let a developer machine's Homebrew installation short-circuit the
# direct-download path this harness exercises.
cat > "$WORK/stub/brew" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod +x "$WORK/stub/brew"

cat > "$WORK/stub/ditto" <<'EOF'
#!/bin/sh
for destination; do :; done
mkdir -p "$destination/CCNotesHelper.app"
printf 'fixture\n' > "$destination/CCNotesHelper.app/marker"
EOF
chmod +x "$WORK/stub/ditto"

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
    if command -v sha256sum >/dev/null 2>&1; then
      helper_hash="$(sha256sum "$FIXTURE_HELPER" | awk '{print $1}')"
    else
      helper_hash="$(shasum -a 256 "$FIXTURE_HELPER" | awk '{print $1}')"
    fi
    printf '%s  cc-notes-helper-%s-darwin.zip\n' "$helper_hash" "${FAKE_TAG:-v0.9.9}"
    ;;
  *)
    printf '%s\n' "$url" >> "$REQUESTED_LOG"
    case "$url" in
      *cc-notes-helper-*) cp "$FIXTURE_HELPER" "$out" ;;
      *) cp "$FIXTURE_BIN" "$out" ;;
    esac
    ;;
esac
EOF
chmod +x "$WORK/stub/curl"

REQUESTED_LOG="$WORK/requested"
FIXTURE_COMMAND_LOG="$WORK/fixture-commands"
export REQUESTED_LOG FIXTURE_COMMAND_LOG FAKE_TAG="v0.9.9"
export FIXTURE_BIN="$WORK/fixture-binary"
export FIXTURE_HELPER="$WORK/fixture-helper"

run_install() { # $1=os $2=arch $3=expected-asset
  : > "$REQUESTED_LOG"
  rm -rf "${WORK:?}/bin" "${WORK:?}/libexec"
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
expect Darwin arm64 cc-notes_darwin_arm64
expect Darwin x86_64 cc-notes_darwin_amd64
expect Linux x86_64 cc-notes_linux_amd64
expect Linux aarch64 cc-notes_linux_arm64

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
if ! grep -qx 'package install' "$FIXTURE_COMMAND_LOG"; then
  echo "FAIL: Darwin installer did not activate the packaged helper" >&2
  exit 1
fi
echo "ok: installer activates only the packaged helper"

echo "PASS: install.sh harness"
