#!/usr/bin/env bash
# Unit-style harness for install.sh. Fakes `uname` and `curl` on PATH and points
# the downloader at a local fixture binary, then asserts the platform -> asset
# mapping, the prefer-fuse rule, and the version no-op re-run. Run manually:
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
  *)
    printf '%s\n' "$url" >> "$REQUESTED_LOG"
    cp "$FIXTURE_BIN" "$out"
    ;;
esac
EOF
chmod +x "$WORK/stub/curl"

REQUESTED_LOG="$WORK/requested"
export REQUESTED_LOG FAKE_TAG="v0.9.9"
export FIXTURE_BIN="$WORK/fixture-binary"

run_install() { # $1=os $2=arch
  : > "$REQUESTED_LOG"
  rm -rf "${WORK:?}/bin"
  PATH="$WORK/stub:$PATH" FAKE_OS="$1" FAKE_ARCH="$2" CC_NOTES_BIN_DIR="$WORK/bin" \
    "$INSTALL" >/dev/null 2>&1
}

expect() { # $1=os $2=arch $3=expected-asset
  run_install "$1" "$2"
  got="$(basename "$(tail -n1 "$REQUESTED_LOG")")"
  if [ "$got" != "$3" ]; then
    echo "FAIL: uname(-s=$1 -m=$2) requested '$got', expected '$3'" >&2
    exit 1
  fi
  echo "ok: $1/$2 -> $3"
}

# Platform mapping + prefer-fuse.
expect Darwin arm64 cc-notes_darwin_arm64_fuse
expect Darwin x86_64 cc-notes_darwin_amd64_fuse
expect Linux x86_64 cc-notes_linux_amd64_fuse
# linux/arm64 has no fuse asset -> falls back to the pure binary.
expect Linux aarch64 cc-notes_linux_arm64

# Re-running at the installed version is a silent no-op (no download).
PATH="$WORK/stub:$PATH" FAKE_OS=Linux FAKE_ARCH=x86_64 CC_NOTES_BIN_DIR="$WORK/bin" \
  "$INSTALL" >/dev/null 2>&1
: > "$REQUESTED_LOG"
PATH="$WORK/stub:$PATH" FAKE_OS=Linux FAKE_ARCH=x86_64 CC_NOTES_BIN_DIR="$WORK/bin" \
  "$INSTALL" >/dev/null 2>&1
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

echo "PASS: install.sh harness"
