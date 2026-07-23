#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_TEMP:?RUNNER_TEMP must be set by GitHub Actions}"
: "${GITHUB_ENV:?GITHUB_ENV must be set by GitHub Actions}"

requirements="$RUNNER_TEMP/cc-notes-helper.requirements"
printf '%s\n' 'designated => identifier "com.yasyf.cc-notes.helper" and anchor apple generic and certificate leaf[subject.OU] = "SXKCTF23Q2" and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists' > "$requirements"
printf 'DESIGNATED_REQUIREMENT_FILE=%s\n' "$requirements" >> "$GITHUB_ENV"
