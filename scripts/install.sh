#!/bin/sh
# Download the prebuilt cc-notes binary for this platform from a GitHub release
# and install it to ~/.local/bin (override with CC_NOTES_BIN_DIR), alongside a
# `ccn` shorthand symlink. Re-running for a version that is already installed
# skips the download but still refreshes the ccn symlink.
#
# Usage:
#   install.sh [VERSION]        # VERSION defaults to "latest"
#   curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh
set -eu

REPO="yasyf/cc-notes"
VERSION="${1:-latest}"
BIN_DIR="${CC_NOTES_BIN_DIR:-$HOME/.local/bin}"
DEST="$BIN_DIR/cc-notes"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# (Re)create the ccn shorthand next to the binary. A relative target keeps the
# link valid if the bin dir is moved; ln -sf overwrites any prior link.
link_alias() {
  ln -sf cc-notes "$BIN_DIR/ccn"
}

# Prefer Homebrew for the default "latest" install: it owns updates, lands on
# PATH, and the formula carries its own checksums. Best-effort — any failure
# (no brew, the Homebrew 6 tap-trust sandbox bug #22603, network) falls through
# to the direct release download below. A pinned-version request skips brew,
# since the formula only tracks the latest stable release.
if [ "$VERSION" = "latest" ] && command -v brew >/dev/null 2>&1; then
  if brew install yasyf/tap/cc-notes >/dev/null 2>&1 && command -v cc-notes >/dev/null 2>&1; then
    echo "cc-notes: installed via Homebrew ($(cc-notes version))" >&2
    exit 0
  fi
  echo "cc-notes: Homebrew unavailable or failed (e.g. tap-trust #22603); using direct download" >&2
fi

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

asset="cc-notes_${os}_${arch}"

# Already on the target version? Skip the download, but still refresh the ccn
# shorthand. Release binaries print "<tag> (<commit>)", so a leading-tag match
# counts.
if [ -x "$DEST" ]; then
  installed="$("$DEST" version 2>/dev/null || true)"
  case "$installed" in
    "$VERSION" | "$VERSION "*) link_alias; exit 0 ;;
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
# Verify the download against the release's SHA256SUMS.txt before trusting it.
# (Homebrew already verifies via the formula; this guards the direct path.)
if ! sums="$(curl -fsSL --retry 2 "https://github.com/$REPO/releases/download/$VERSION/SHA256SUMS.txt")"; then
  echo "cc-notes: could not fetch SHA256SUMS.txt for $VERSION" >&2
  exit 1
fi
expected="$(printf '%s\n' "$sums" | awk -v a="$asset" '$2 == a {print $1}')"
if [ -z "$expected" ]; then
  echo "cc-notes: no checksum for $asset in SHA256SUMS.txt" >&2
  exit 1
fi
actual="$(sha256_of "$tmp")"
if [ "$actual" != "$expected" ]; then
  echo "cc-notes: checksum mismatch for $asset (expected $expected, got $actual)" >&2
  exit 1
fi
chmod +x "$tmp"
mv -f "$tmp" "$DEST"
link_alias
echo "cc-notes: installed $DEST ($("$DEST" version))" >&2
