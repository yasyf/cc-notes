#!/usr/bin/env bash
set -euo pipefail

: "${APP_PATH:?APP_PATH must name the built CCNotesHelper.app}"
: "${TEAM_ID:?TEAM_ID must be exported by import-developer-id}"
: "${MACOS_SIGN_IDENTITY:?MACOS_SIGN_IDENTITY must be exported by import-developer-id}"
: "${DESIGNATED_REQUIREMENT_FILE:?DESIGNATED_REQUIREMENT_FILE must name the exact helper requirement}"

APP="$APP_PATH"
EXECUTABLE="$APP/Contents/MacOS/CCNotesHelper"
test -d "$APP"
test -f "$EXECUTABLE"
test ! -L "$APP"
test ! -L "$EXECUTABLE"
test -f "$DESIGNATED_REQUIREMENT_FILE"
test ! -L "$DESIGNATED_REQUIREMENT_FILE"

DESIGNATED_REQUIREMENT="$(sed -n 's/^designated => //p' "$DESIGNATED_REQUIREMENT_FILE")"
test -n "$DESIGNATED_REQUIREMENT"
test "$(wc -l < "$DESIGNATED_REQUIREMENT_FILE" | tr -d ' ')" = "1"

verify_designated_requirement() {
  codesign --verify --strict --verbose=2 -R "=$DESIGNATED_REQUIREMENT" "$APP"
}

verify_designated_requirement

GOWORK=off GOFLAGS=-mod=readonly go run ./cmd/cc-notes-helper-package \
  -app "$APP" -signing-identity "$MACOS_SIGN_IDENTITY"

codesign --verify --deep --strict --verbose=2 "$APP"
verify_designated_requirement
CODE_DETAILS="$(codesign -d --verbose=4 "$APP" 2>&1)"
test "$(sed -n 's/^TeamIdentifier=//p' <<< "$CODE_DETAILS")" = "$TEAM_ID"
test "$(sed -n 's/^Identifier=//p' <<< "$CODE_DETAILS")" = "com.yasyf.cc-notes.helper"
grep -Eq 'flags=.*\(([^,]+,)*runtime(,[^,]+)*\)' <<< "$CODE_DETAILS"
ENTITLEMENTS="$(codesign -d --entitlements - "$APP" 2>&1)"
if grep -q 'disable-library-validation' <<< "$ENTITLEMENTS"; then
  echo "::error::CCNotesHelper.app permits unsigned or foreign dynamic libraries"
  exit 1
fi

test "$(lipo -archs "$EXECUTABLE")" = "x86_64 arm64" || test "$(lipo -archs "$EXECUTABLE")" = "arm64 x86_64"
test -f "$APP/Contents/Frameworks/libfuse-t.dylib"
test -f "$APP/Contents/Resources/ThirdPartyLicenses/FUSE-T.txt"
test -f "$APP/Contents/Resources/FuseKit/libfuse-t.manifest.json"
