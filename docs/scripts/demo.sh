#!/bin/sh
# Regenerates docs/assets/demo.png from a real `cc-notes status` run.
#
# Stages a throwaway repo (local bare remote, fake identity), runs `cc-notes
# init`, seeds a backlog, claims one task on a feature branch, then freezes
# the status board as seen from main. The board prints no ANSI of its own, so
# bat paints the captured text (yaml reads the board best) before freeze
# renders it. Requires cc-notes, bat, and freeze on PATH.
set -eu

OUT=$(cd "$(dirname "$0")/../assets" && pwd)/demo.png

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

mkdir -p "$STAGE/remote" "$STAGE/repo"
git -C "$STAGE/remote" init --bare -q
cd "$STAGE/repo"
git init -q -b main
git config user.name "ada"
git config user.email "ada@example.com"
git remote add origin "$STAGE/remote"
echo "# scratch" >README.md
git add README.md
git commit -qm "init"
git push -q origin main

cc-notes init --no-ci >/dev/null

FIRST=$(cc-notes task add "Add retry backoff to the API client" --backlog --priority 1 --label api \
	--criterion "go test ./internal/api/... passes" | cut -f1)
cc-notes task add "Migrate auth tokens to short-lived JWTs" --backlog --priority 2 --label auth \
	--criterion "login flow issues a JWT with a 15m TTL" >/dev/null
cc-notes task add "Profile the sync hot path" --backlog --priority 3 --no-validation-criteria >/dev/null

cc-notes note add "Auth tokens expire after 15 minutes" \
	--path services/auth/login.go --tag design >/dev/null
cc-notes doc add "Auth migration handoff" \
	--when "picking up the JWT migration" \
	--body "Plan, open questions, and the rollout order for the JWT swap." >/dev/null

git checkout -qb api-retries
cc-notes task start "$FIRST" >/dev/null
git checkout -q main

CAPTURE="$STAGE/demo.ansi"
{
	printf '$ cc-notes status\n' | bat --plain --color=always --language bash
	cc-notes status | bat --plain --color=always --language yaml
} >"$CAPTURE"

freeze "$CAPTURE" --language ansi \
	--theme github-dark --background "#0d1117" --window --padding 24 --font.size 28 \
	--output "$OUT"
