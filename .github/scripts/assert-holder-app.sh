#!/usr/bin/env bash
set -euo pipefail

: "${APP_PATH:?APP_PATH must name the built CCNotesHolder.app}"
: "${TEAM_ID:?TEAM_ID must be exported by import-developer-id}"
: "${MACOS_SIGN_IDENTITY:?MACOS_SIGN_IDENTITY must be exported by import-developer-id}"

APP="$APP_PATH"
EXECUTABLE="$APP/Contents/MacOS/CCNotesHolder"
test -d "$APP"
test -f "$EXECUTABLE"
test ! -L "$APP"
test ! -L "$EXECUTABLE"

GOWORK=off GOFLAGS=-mod=readonly go run ./cmd/cc-notes-fuse-package \
  -app "$APP" -signing-identity "$MACOS_SIGN_IDENTITY"

codesign --verify --deep --strict --verbose=2 "$APP"
test "$(codesign -d --verbose=4 "$APP" 2>&1 | sed -n 's/^TeamIdentifier=//p')" = "$TEAM_ID"
test "$(codesign -d --verbose=4 "$APP" 2>&1 | sed -n 's/^Identifier=//p')" = "com.yasyf.cc-notes.holder"
codesign -d --verbose=4 "$APP" 2>&1 | grep -Eq '^flags=.*runtime'
if codesign -d --entitlements - "$APP" 2>&1 | grep -q 'disable-library-validation'; then
  echo "::error::CCNotesHolder.app permits unsigned or foreign dynamic libraries"
  exit 1
fi

test "$(lipo -archs "$EXECUTABLE")" = "x86_64 arm64" || test "$(lipo -archs "$EXECUTABLE")" = "arm64 x86_64"
test -f "$APP/Contents/Frameworks/libfuse-t.dylib"
test -f "$APP/Contents/Resources/ThirdPartyLicenses/FUSE-T.txt"
test -f "$APP/Contents/Resources/FuseKit/libfuse-t.manifest.json"
