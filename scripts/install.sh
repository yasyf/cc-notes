#!/usr/bin/env bash
# Download the prebuilt cc-notes binary for this platform from a GitHub release
# and install it to ~/.local/bin (override with CC_NOTES_BIN_DIR). The FUSE
# variant is preferred wherever one is published (darwin both arches, linux
# amd64), falling back to the pure static binary. Re-running for a version that
# is already installed is a silent no-op.
#
# Usage:
#   install.sh [VERSION]        # VERSION defaults to "latest"
#   curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh
set -euo pipefail

REPO="yasyf/cc-notes"
VERSION="${1:-latest}"
BIN_DIR="${CC_NOTES_BIN_DIR:-$HOME/.local/bin}"
DEST="$BIN_DIR/cc-notes"

# Resolve "latest" to a concrete tag by following the releases/latest redirect
# and parsing the tag off the resulting .../releases/tag/<tag> URL.
if [ "$VERSION" = "latest" ]; then
  effective="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")"
  VERSION="${effective##*/tag/}"
  case "$VERSION" in
    v*) ;;
    *)
      echo "cc-notes: could not resolve the latest release tag (got '$effective')" >&2
      exit 1
      ;;
  esac
fi

# Map uname output onto the GOOS/GOARCH tokens used in the asset names.
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin | linux) ;;
  *)
    echo "cc-notes: unsupported OS '$os'" >&2
    exit 1
    ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  arm64 | aarch64) arch="arm64" ;;
  *)
    echo "cc-notes: unsupported architecture '$arch'" >&2
    exit 1
    ;;
esac

# Prefer the fuse asset where the release ships one for this platform.
asset="cc-notes_${os}_${arch}"
case "${os}_${arch}" in
  darwin_arm64 | darwin_amd64 | linux_amd64) asset="${asset}_fuse" ;;
esac

# Already on the target version? Nothing to do. Release binaries print
# "<tag> (<commit>)", so a leading-tag match counts.
if [ -x "$DEST" ]; then
  installed="$("$DEST" version 2>/dev/null || true)"
  case "$installed" in
    "$VERSION" | "$VERSION "*) exit 0 ;;
  esac
fi

url="https://github.com/$REPO/releases/download/$VERSION/$asset"
mkdir -p "$BIN_DIR"
# Stage on the destination filesystem and rename into place: writing onto a
# running executable fails with ETXTBSY on Linux, and the rename keeps any
# still-executing inode alive.
tmp="$(mktemp "$BIN_DIR/.cc-notes.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 2 -o "$tmp" "$url"
chmod +x "$tmp"
mv -f "$tmp" "$DEST"
echo "cc-notes: installed $DEST ($("$DEST" version))" >&2
