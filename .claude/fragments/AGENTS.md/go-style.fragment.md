## Go Style

Target Go 1.26+. Full rules live in STYLEGUIDE.md; the build/test loop:

- **Build**: `go build ./...` (pure Go, `CGO_ENABLED=0` for release binaries)
- **Test**: `go test -race -count=1 ./...`
- **Vet**: `go vet ./...` before every commit
- **Fuse variant**: `go build -tags fuse ./...` needs cgo + a FUSE implementation (fuse-t on macOS, fuse3 on Linux); the default build must stay pure Go.
- **Webui variant**: `go build -tags webui ./...` embeds `web/dist` into the binary; build it first with `cd web && npm ci && npm run build`. The default build compiles the stub instead and must stay pure Go and green with no node and no `dist/` present. CI builds `dist/` and compiles the webui variant; release binaries carry it.

**Comments are terse and used sparingly — the code documents itself** through names, types, and organization. The one exception is documentation-generation comments: godoc on exported types, funcs, and the package, each starting with the identifier's name (`// NewRootCmd builds …`); unexported helpers get none. Beyond godoc, comment only for TODOs, non-obvious workarounds, or disabled code — never to restate the signature.

@STYLEGUIDE.md

## General Rules

**Minimal changes.** Stay within scope; fix the issue, then stop.

**Match surrounding code.** Follow the conventions of the file you're in, then the module.

**No defensive coding.** No fallbacks, shims, or backwards-compat layers; no guards against impossible states. If unused, delete it. Crash on the unexpected.

**Search before writing.** Before creating a helper, query the codebase via `ccx code search` (intent) or `ccx code symbol` (a named symbol). Sibling modules and base classes win over re-implementation.

**Code stewardship.** When you touch a file, fix nearby bugs, style violations, and broken tests; don't wave them off as pre-existing or out of scope. Mechanical lint noise is the exception (see § Mechanical linting).

**Observe, don't infer.** Inspect actual data — read fixtures, dump objects, run the code — before reasoning from assumption.

**Don't use external failures as an excuse to stop.** API quota, rate-limit, and outage errors rarely block the whole task; trace the catch sites and confirm a failure actually stops you before claiming it does.

**Verify before asserting.** Don't report something as working, fixed, blocked, or impossible until you've checked — run it, read the output, reproduce the failure. "It should work" is not "it works."

**Reproduce before fixing.** When something breaks, isolate the smallest failing case before editing or re-running. Re-running the whole command while changing code between runs hides the root cause; narrow to the one failing call, payload, or test first.

**Research after repeated failure.** After ~2 failed approaches, stop guessing and gather evidence — search the web, read the docs and source — before a third attempt.

**Get a second opinion on a plateau.** On a debugging plateau (2 failed attempts before a 3rd), a non-trivial architectural decision, or algorithmic/security-sensitive code, get an outside check (e.g. `/codex`) before committing to the approach.

**Don't contort code to satisfy a checker.** The type checker and linter serve the code, not the other way around. Don't reshape a data model, widen a type, or bolt on a `cast(...)` / narrowing-only `assert isinstance(...)` / blanket ignore just to silence a diagnostic. If a clean fix isn't obvious, leave the diagnostic — a visible diagnostic is preferable to scar tissue. (Most checker noise isn't worth acting on at all; act only when it flags a real bug.)

**Mechanical linting.** `gofmt` and `go vet` own formatting and mechanical issues (plus `golangci-lint` if a config lands). Run them before committing; fix only what needs human judgment. When reviewing code, don't flag mechanical lint violations (formatting, import order) — tooling owns them.

**Testing.** Go tests live next to the code as `*_test.go`; run `go test -race -count=1 ./...` from the repo root. Table-driven with named cases, strict assertions against specific expected values. Git is not a mock boundary — tests that need a repository run real `git init` in `t.TempDir()` and operate on the real object database; mock only true externals (network, clock).

**Writing docs.** When writing or revising docs, a README, a tutorial, a how-to, or reference, use the `writing-docs` skill (Diataxis modes, voice rules, and runnable code-sample rules) and run `slop-cop check <file> --lang=markdown` before you finish.

**Version control.** This is a colocated `jj` repo over git — prefer `jj` (`jj describe` / `jj commit`, `jj git push`) for day-to-day work; commits stay atomic and scoped, one logical change each. For the routine commit, push, and watch-CI cycle, `ccx vcs ship -m "<msg>"` runs the whole dance in one call — a jj-aware commit, the push, and `gh run watch --exit-status` — instead of the three-to-six Bash calls it took by hand; drop to the manual `jj` steps when ship doesn't fit, like a multi-commit split or a partial-staging commit. A dirty tree is just the working-copy commit `@`: to land work on an updated remote, `jj git fetch` then `jj rebase -r <change> -o main@origin` (your in-flight `@` rides along untouched), never `git stash` or a worktree + cherry-pick dance. jj's git bridge carries only `refs/heads/*` — to move this repo's `refs/cc-notes/*` notes/tasks, run `cc-notes sync` (it drives real git); `jj git push` alone leaves them behind.

**Watch CI after every push.** A push that kicks off CI isn't done until the run is green. `ccx vcs ship` folds this in — it pushes, then runs `gh run watch --exit-status`, so a shipped commit is already watched to its conclusion. For a push ship didn't make, watch the run to completion yourself before you stop — `gh run watch "$(gh run list -L1 --json databaseId -q '.[0].databaseId')" --exit-status` — and never walk away from a red run: fix it or report it. (`--exit-status` exits non-zero when the run fails; give the run a moment to register before watching.)

**Releases.** Tagging `v*` triggers the release workflow, which cross-compiles the platform binaries (`cc-notes_<os>_<arch>`, plus `_fuse` variants built with cgo and `-tags fuse`), generates `SHA256SUMS`, and uploads everything as GitHub Release assets; `scripts/install.sh` resolves the right asset per platform, preferring the `_fuse` variant. The version comes from the tag, injected via `-ldflags` into `internal/version`. Tag merged commits on `main` (e.g. `git tag vX.Y.Z origin/main`), not a feature branch. On stable tags (no `-` suffix) the `bump-formula` job renders the Homebrew formula from the canonical inline template in `release.yml`, filling in the tag version and asset sha256s, then publishes it to the shared `yasyf/homebrew-tap` repo via the `yasyf/homebrew-tap/.github/actions/publish@main` composite action, so `brew install yasyf/tap/cc-notes` tracks the latest stable release. The formula no longer lives in this repo, and prerelease tags skip it. The same job separately bumps the capt-hook pack manifest on cc-notes' own `main`. Never re-tag a published stable version — the `--clobber` re-upload changes asset sha256s out from under installed brew users.
