# cc-notes Style Guide

The concrete style rules for this repository. Target Go 1.26+. `gofmt` is law;
everything below is what `gofmt` can't decide for you.

## Core Principles

1. **Fail fast, fail loud.** No defensive coding: no silent fallbacks, shims, or
   backwards-compat layers, and no guards against impossible states. No sentinel
   values, no silent defaults. If a precondition can't hold, return an error; if
   it can't even be expressed, `panic` (programmer error only). If unused, delete
   it.
2. **Make invalid states unrepresentable.** Typed constants over stringly state
   (`type Status int` + `iota`, not `"open"` literals scattered around). Required
   fields over pointers-meaning-optional; every `*T` field forces every reader to
   handle nil. Small, consumer-defined interfaces over big provider-defined ones.
3. **Reuse before creating.** Check the same package, then sibling `internal/*`
   packages, before writing a helper. No premature `util` package; a helper used
   by one package lives in that package.
4. **Match surrounding code.** Priority: (1) this guide, (2) same file, (3) same
   package. If surrounding code violates this guide, fix it while you're there.
5. **Minimal changes.** Stay within scope. Make the test pass, then stop. Improve
   only the code you touch.
6. **No backwards-compat shims.** This is a single-binary tool. Dead code,
   compatibility layers, and deprecated aliases get deleted, not kept.

## Errors

- Wrap with context exactly once per layer: `fmt.Errorf("load note %s: %w", id, err)`.
  Don't re-wrap the same information at every level, and never both log *and*
  return an error — pick one (return; the caller logs).
- Sentinel errors (`var ErrNotFound = errors.New(...)`) + `errors.Is`/`errors.As`
  for control flow. Never match on error strings.
- `internal/*` packages return errors; they never `os.Exit` or `log.Fatal`. Only
  `main` maps errors to exit codes and terminates.
- Handle errors at the level that has context to act; pass them up otherwise.
  `_ =` discards only for genuinely best-effort cleanup (and say why if
  non-obvious).
- Keep the fallible call adjacent to its `if err != nil` — no unrelated
  statements between them (the Go analog of "minimal try block").
- `panic` only for programmer error — a violated invariant the type system
  couldn't express, never a condition the environment can produce.

```go
// Good — wrapped once, contextual, propagated
tip, err := store.Tip(ctx, id)
if err != nil {
    return fmt.Errorf("resolve tip for %s: %w", id, err)
}

// Bad — context lost, error swallowed
tip, _ := store.Tip(ctx, id)
```

## Comments & Documentation

- **Godoc on every exported symbol** — full sentences, starting with the symbol
  name. This is the one mandated deviation from "no comments": Go tooling depends
  on it. A doc comment that restates the signature is clutter to delete.
- **Inside function bodies: no noise comments.** Code is self-documenting via
  names, types, and small functions. Comments only for:
  - TODOs (`// TODO: ...`)
  - Non-obvious workarounds and invariants (e.g. canonical JSON marshal layout is
    part of the storage format — changing it changes entity ids; that comment
    earns its place)
  - Disabled code that may be re-enabled
- No section-marker comments (`// --- helpers ---`); split into files instead.
- Don't restate the code (`// increment i`). Don't narrate history (`// previously
  this used X`) — git remembers.

## Organization & Naming

- File order: package doc → imports → constants → types → constructors → methods
  → related helpers. Constants at the top. `UPPER_SNAKE` is not Go — use
  `MixedCaps` (`maxRetries`, `DefaultTimeout`).
- Imports in two groups: stdlib, then everything else (`gofmt`/`goimports`
  handles the rest).
- **No stuttering names.** The package name is part of the API: `store.Entity`,
  not `store.StoreEntity`; `fold.State`, not `fold.FoldState`.
- Naming: short receivers (`s *Store`, not `store` or `self`/`this`); no `Get`
  prefix (`Tip()`, not `GetTip()`); acronyms keep case (`ID`, `URL`, `SHA`).
- Lowercase unexported by default; export only what another package must call.
  The public surface is the `cmd/cc-notes` binary, not an importable Go API.
- Flat control flow: early returns, happy path at the lowest indentation. Nesting
  >3 deep is a smell — extract a function.
- One concept per file; file names describe contents (`refs.go`, `fold.go`), no
  `misc.go` dumping grounds growing without bound.
- Keep packages small and single-purpose under `internal/`.

## Concurrency

- `ctx context.Context` is the first parameter of anything that blocks, sleeps,
  retries, or makes a request. Honor cancellation — `select` on `ctx.Done()` in
  loops.
- Every goroutine has a defined exit path before it's spawned. If you can't say
  what makes it return, don't start it.
- Channels are owned (created and closed) by the sender. Never close from the
  receiver side.
- Mutex scope is minimal: lock, touch the shared state, unlock. Never hold a lock
  across I/O, a network call, or a subprocess. Prefer `defer mu.Unlock()` unless
  the critical section is a small early slice of the function.
- Shared state needs exactly one synchronization story (one mutex, one owner
  goroutine, or immutability) — document which at the declaration.
- Bounded fan-out goes through `errgroup` with a concurrency limit, not unbounded
  goroutine spawns.

## Testing

Tests exist to catch bugs, not to satisfy coverage. Before writing one, answer:
*what bug would make this fail?* If nothing would, delete it. Run the suite with
`go test -race -count=1 ./...`.

- **Strong assertions** — compare against specific expected values. `if got != want`
  with both in the message. `result != nil` as the sole assertion is worthless.
- **Table-driven tests** with descriptive case names covering happy, edge, and
  error paths. Golden vectors pin externally-derived values against an
  independent oracle.
- **Real fixtures over mocks.** Git is not a mock boundary: tests that need a
  repository run `git init` in `t.TempDir()` and operate on the real object
  database. Mock only true externals (network, clock); leave the code under test
  real. If you mock the function you're testing, you're testing the mock.
- **Litmus test** — revert the implementation; the test must fail. If it still
  passes, it exercises nothing.
- **Never degrade tests to pass**: no deleting failing cases, no weakening `==`
  to `!= nil`, no dropping error-path tests because setup is annoying. When a
  test fails: read the error → fix the test if its expectation is wrong →
  otherwise fix the implementation. Stuck after two attempts? Ask, don't simplify
  silently.
- **Negative tests required** — invalid input, dependency failure, boundary
  conditions.
- Concurrent code is tested under `-race` with real synchronization (channels,
  `sync.WaitGroup`), never `time.Sleep` as a synchronization primitive.

## Tooling

- `gofmt` and `go vet` are assumed clean — never hand-flag mechanical issues
  (formatting, import order) in review; tooling owns them.
- Default build is pure Go (`CGO_ENABLED=0 go build ./...`); only `-tags fuse`
  needs cgo + a FUSE implementation. Code shared by both variants must compile
  under both — keep the build-tag surface minimal (`fuse.go` / `fuse_stub.go`
  pattern).
- `go test ./...` must pass with no network and no FUSE installed (mount tests
  skip themselves).
