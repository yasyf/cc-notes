# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Kind-scoped misses now name the entity's actual kind.** When a noun-scoped
  command (`doc show`, `note edit`, `task done`, …) misses, cc-notes scans the
  other kinds for the id and, on a clean match, appends a hint to the exit-3
  not-found error: `"e39ee86" is a note — "cc-notes show e39ee86" resolves it
  (or use a note-scoped command)`. A prefix matching several other kinds lists
  them; a miss everywhere stays a plain not-found. The MCP `*_show`/`*_edit`
  tools inherit the hint through the CLI bridge.
- **MCP tool calls with wrong argument names now name the accepted ones.**
  A pre-validating middleware checks argument keys against each tool's schema
  before the SDK rejects them, replacing the opaque `unexpected additional
  properties` failure with one line naming the tool's accepted properties
  (required starred) and a did-you-mean for common wrong names (`text`/
  `comment`→`body`, `complaint`→`body`, `evidence`→`note`, `tags`→`labels`).
  Wrong names still fail — arguments are never rewritten.

### Changed
- **BREAKING (MCP): the `papercut` tool's `text` argument is renamed `body`**,
  matching every other free-text tool argument. Schema-driven callers pick the
  new name up automatically; anything hardcoding `text` now gets the middleware
  hint naming `body`.

### Fixed
- Cross-kind ambiguity reports from top-level `show`/`compact` no longer
  mislabel every match as a note: both commands now resolve through
  `notes.Client.ResolveEntity`, whose `AmbiguousKindsError` carries the real
  per-kind labels (the CLI-internal duplicate resolver is gone).

### Added
- **Writes stamp the Claude session id into their op pack.** Every entity
  mutation — note add, doc edit, log append, task ops, compaction, sync
  merges — records the writing Claude session in a new pack-level `session`
  field, read from `CC_NOTES_SESSION_ID` when set (set-but-empty suppresses
  the stamp), else `CLAUDE_CODE_SESSION_ID`, else omitted entirely: a
  session-less pack is byte-identical to the old wire format, so existing
  entity ids and stored chains are untouched, and old binaries ignore the new
  key. `history` shows the stamp as a `session:<first 8 chars>` suffix in text
  and the full id under `session` in `--json`, and the viz entity-trail panel
  shows it beside the author.
- **`notes.Client` now owns the repo-utility surface — status, relevance,
  history, blame, sync, reconcile, and gc — as a library.** The in-process
  `notes` client gains `Status`, `Relevant`, `History`, `Blame`, `Sync`,
  `Reconcile`, and `GC`, each returning a folded `model.*` snapshot or a small
  public report, so an embedding consumer drives them without shelling out or
  importing `internal/*`. The matching CLI commands and the shared print layer
  are rewired onto the client, so the domain logic for these commands lives in
  one place; CLI text and JSON output are byte-identical. `History`
  re-expresses each commit's change trail as a `notes.HistoryEntry` /
  `FieldChange` of formatted strings, keeping `internal/fold` and
  `internal/trail` off the public surface. Reverse-index reads move onto the
  client too — `TasksBlocking`, `SprintTasks`, `ProjectSprints`, `ProjectTasks`,
  and a single-fold `TasksBlockingIndex` — so the print DTOs no longer reach
  into the store, and the client-only repo-utility commands drop their duplicate
  store open.
- **A verbose comment's rationale routes to cc-notes.** The capt-hook pack
  gains `comments.py`: a declarative advisory that rides along with the general
  pack's verbose-comment deny (an `Edit`/`Write`/`MultiEdit` leaving a non-doc
  comment run over 3 lines / 200 chars is blocked). When an edit introduces or
  grows a too-long comment block — inline or doc-shaped, since a
  rationale-stuffed doc comment is equally durable — it points that rationale
  at `cc-notes note add` (a decision or fact) or `cc-notes doc add --when …`
  (living guidance) and asks for the comment to shrink to one terse pointer.
  Declarative (not a handler-backed nudge) so it survives the deny a nudge
  would be skipped behind, surfacing exactly when the block fires.
- **cc-notes usage never permission-prompts.** The capt-hook pack gains
  `approval.py`: two allow-only `PermissionRequest` approvers that answer
  a would-be dialog with *allow* for any MCP tool on the exact servers
  `cc-notes` / `plugin_cc-notes_cc-notes` and for one plain single-command
  `cc-notes`/`ccn` Bash invocation (any subcommand). A carve-out keeps the
  dialog for the calls that reach outside the git ODB — reading or writing an
  arbitrary path, or running a stored script: `attachment get -o`, `--attach`,
  `--apply`, `--abort`, `--script`, `workflows install --dir`,
  `mount --socket`, `task validate`, `task criterion script`, and the matching
  MCP tools (`task_validate`, plus any call carrying an `attach`/`output`/
  `script`/`file` path). Everything else fails closed to the normal dialog —
  shell expansion in the raw text, pipelines, chains, redirects/heredocs,
  env-assignment prefixes, wrappers, path-qualified binaries, and a bare `--`.
  The Claude Code plugin now session-attaches the whole pack via
  `plugin/hooks/hooks.json` (manifest at `plugin/hooks/capt-hook.toml`, plus
  an async binary-install backstop), applying it wherever the plugin is
  enabled; a repo's same-named `packs.toml` pin keeps precedence, so the two
  delivery paths never double-load.
- **`cc-notes papercut` — a repo-wide friction-complaint journal.**
  `cc-notes papercut "<complaint>"` files a one-paragraph complaint — a
  dead-end tool call, a broken link, a misleading doc — as an entry in a
  shared log titled `papercuts` (tagged `papercut`, auto-created on first
  use), and `cc-notes papercut list` reads the chronology back; `--model`
  (or `CC_NOTES_MODEL`, with the flag winning) records the model identity
  on the entry. Mirrored as the `papercut` and `papercut_list` MCP tools.
  To carry the identity, log entries gain an optional `model` field —
  additive and lenient-decode compatible: an old binary ignores the field,
  and an old binary compacting a journal drops stored model values
  (accepted).

### Changed
- **A detached HEAD is first-class: branch-scoped commands resolve the
  branch jj-aware.** In a colocated jj repo git HEAD sits detached at the
  working-copy parent as the normal state, and `task start`, `task add`,
  `task list`, `task ready`, and `reconcile` hard-errored there
  ("detached HEAD; …"). A jj-aware resolver now finds the current branch
  even when detached: the branch HEAD points at when attached, else the
  nearest local branch on `trunk..HEAD` not yet merged into the trunk
  (the exported jj bookmark you advanced past), else the trunk itself
  (`origin/HEAD`, else a local `main`, else `master`). When even that
  fails, each command degrades instead of erroring: `task start` — which
  gains a `--branch` flag — claims the task without setting a branch and
  warns on stderr; `task add` lands the task on the backlog with a stderr
  note (`--branch` overrides, `--backlog` stays explicit); `task list`
  and `task ready` fall back to the backlog view. `reconcile` still
  requires a real target: it resolves the same way when detached and
  errors asking for `--into` only when nothing resolves. An explicit
  empty `--branch=` / `--into=` is a usage error, rejected before any
  mutation.
- **The `notes` package is the domain core.** The importable
  `notes.Client` (`github.com/yasyf/cc-notes/notes`) is now the single
  implementation of every entity operation — task, note, doc, log,
  runbook, sprint, and project create/read/edit/transition/lifecycle,
  plus attachments — and the `cc-notes` CLI and the MCP server both
  drive it; it was previously a thin, partial facade nothing used.
  Repo-utility commands (`status`, `relevant`, `history`, `blame`,
  `sync`, `reconcile`, `gc`, `mount`, `viz`) still operate on the store
  directly.
- `cc-notes init` and `cc-notes hooks install` wire capt-hook through `uvx
  --isolated capt-hook`, keeping a machine-wide `uv tool install capt-hook`
  from silently pinning bare `uvx` to a stale environment.
- The release workflow no longer bumps `.claude/capt-hook.toml` on `main`
  after tagging — that ordering left every tagged commit self-labeled one
  pack version behind. The manifest's `version` is frozen at the inert
  `0.0.0` (capt-hook ≥ 9.7.0 shows the resolved release tag for moving
  packs instead of reading it).

### Fixed
- **Auto-sync no longer hard-codes `origin`.** The capt-hook pack derives
  the cc-notes-wired remotes from git config and syncs each via `cc-notes
  sync --remote <name>` (one bare sync when none is wired), and the
  SessionEnd dirty backstop checks every wired remote's tracking refs
  instead of only `refs/cc-notes-sync/origin/*`. The Go side follows: a
  bare `cc-notes sync` (no `--remote`) fans out over every cc-notes-wired
  remote in config order (else `origin`), matching the pack, while `gc`'s
  tombstone prune, `task claim`'s post-claim sync, and the refspec
  auto-install (its announcements included) derive a single remote — the
  sole wired remote wins, else `origin`. Either way a repo wired via
  `cc-notes init --remote upstream` syncs where it was wired.
- **A cross-repo cc-notes write syncs the repo it wrote.** `cd /other/repo
  && cc-notes note add …` synced the session repo and confirmed as if the
  foreign write had shipped. The pack now walks the parsed command legs,
  tracking every literal `cd` to resolve the directory each write leg runs
  in, and runs `cc-notes sync` in the written repo, confirming "Synced
  cc-notes refs in <dir>." — once per target repo per turn. A `cd` it can't
  resolve structurally (`cd -`, a `$var`, a `~`, a backtick substitution)
  falls back to the session repo; record writes only, and an MCP write
  always targets the session repo.

## [0.27.0] - 2026-07-12

Holder v2: cc-notes becomes a plain tenant of the shared `fusekit-holder`.

### Changed
- **The private mount holder is gone.** Mounts are served by the shared
  multi-tenant `fusekit-holder` cask (wire proto 2, `Owner="cc-notes"`,
  feature-negotiated via `hello`), and the store→tree renderer now runs
  behind **contentd** — a KeepAlive LaunchAgent serving the content tree on
  `~/.fusekit/spool/cc-notes/c.sock` with commit-on-Flush semantics and
  per-node versions driving the holder's NFS cache defeat. The holder alone
  mounts, unmounts, and clears carcasses; cc-notes retains no force
  primitive of any kind.
- **`mount --shutdown` reclaims only cc-notes' own mounts** (owner-scoped,
  lease-gated holder-side) and can no longer stop any holder process;
  `mount --stop` refuses a foreign tenant's mount and, while the legacy
  private holder still serves the target, prints the graceful displacement
  recipe instead of tearing down beneath it.
- First mounts await contentd's socket readiness; a brew upgrade recycles a
  stale contentd via a boot stamp; detached mounting is macOS-only
  (`mount --foreground` remains the portable path).

### Removed
- The `mount-holder` subcommand, the in-process holder host, and the private
  holder state (`~/.cc-notes/mounts.sock`, `~/.cc-notes/bin`,
  `mount-holder.log`).

## [0.25.0] - 2026-07-11

### Added
- **Auto-sync now covers the whole publish surface, with a SessionEnd
  backstop.** The capt-hook pack syncs after jj commits (`jj commit`,
  `jj describe`, `ccx vcs ship`), pushes (`git push`/`jj git push` — jj's git
  bridge never carries `refs/cc-notes/*`), `jj git fetch` (the reconcile
  path), and every cc-notes write — mutating CLI subcommands and MCP tools;
  reads never trigger — still deduped to one sync per turn. A new async
  SessionEnd hook backstops write-only sessions: a zero-network dirty check
  of `refs/cc-notes/*` against the `refs/cc-notes-sync/origin/*` tracking
  namespace, syncing only when something is unpushed and staying silent on
  no-remote/offline/timeout. The backstop requires capt-hook >= 9.2, whose
  captain-hook plugin dispatches `run SessionEnd --async`.

### Changed
- Auto-reconcile falls back to a plain sync on a detached HEAD (the
  colocated-jj norm) or a failed reconcile instead of staying silent, so
  fetched refs still ship; manual `cc-notes reconcile` remains only for jj
  merges and rebases.
- The pack's command matching moved from regexes to capt-hook's structured
  `Runs()` argv-prefix conditions: compound lines match, quoted mentions
  don't, and flag-interleaved forms (`git --no-pager commit`) are missed by
  design.
- The pack owns session bootstrap: an async SessionStart hook installs or
  upgrades the binary (>= 0.22.0) and re-ensures the mount, and a
  once-per-session UserPromptSubmit nudge announces availability, replacing
  the plugin's `ensure-cc-notes.sh` SessionStart script and the plugin.json
  hooks block, both removed.
- `cc-notes init` now enables the captain-hook plugin (`uvx capt-hook skills
  install`, the dispatcher) before adding the pack; under capt-hook 9.0.0,
  `capt-hook pack add` writes only `.claude/hooks/packs.toml` — event wiring
  ships in the captain-hook plugin's hooks.json, and settings generation is
  gone.

## [0.24.0] - 2026-07-11

### Changed
- **Whole-codebase consolidation, no format changes.** Every layer that
  repeated per-kind wiring now derives it from one kind registry: a canonical
  `model.Kind` with `Meta()` snapshot headers and kind-tagged create ops, a
  splicing op codec, a table-driven ref scheme, one generic fold engine with
  per-kind folders, a generic store list core, CLI verb/document builders over
  per-kind specs, and a fusefs codec + layout + diff-combinator engine. Net
  effect: about 1,600 fewer production lines and a new entity kind now costs
  registry rows plus its genuinely kind-specific logic instead of ~30 hand
  -written seams. The storage format, CLI vocabulary, JSON output, mount file
  format, and viz wire format are unchanged — pinned by new golden batteries
  (wire-byte, vocabulary, DTO-shape, and a 33-fixture mount corpus) and proven
  by an end-to-end diff: the v0.23.0 and v0.24.0 binaries produce byte
  -identical output for every read command and every help screen over the same
  repo, and old on-disk histories fold identically.
- `note add` validates its title and body before touching the repository,
  matching `doc add` — a usage error no longer installs cc-notes refspecs
  into `.git/config` as a side effect.

### Added
- The release workflow smoke-tests the pure (non-FUSE) darwin binary on a
  macOS runner before publishing assets: version stamp, `init`, and a note
  round-trip in a fresh repo.

### Removed
- The standalone reconcile CI workflow — a byte-duplicate of the dogfooded
  template's job that raced it on every push to main.
- web: the unused `d3-array` dependency, orphaned CSS, and duplicated
  clipboard/format helpers; the TypeScript config now enables
  `noUncheckedIndexedAccess`.

## [0.23.0] - 2026-07-10

### Added
- **Runbooks: ordered procedures with tracked runs.** A runbook is a sequence
  of steps — instruction text plus an optional command — whose executions are
  first-class: each run records the runner, timestamps, per-step outcomes
  (done/failed/skipped), an overall status, and an optional task cite. Runs
  live as ops on the runbook's own chain (`refs/cc-notes/runbooks/*`), folded
  with the same rules as every other kind; step positions use fractional
  indexing so inserts never renumber neighbors. Surfaces: a full `runbook`
  CLI noun group (`runbook` / `step` / `run` verbs, with writes gated on
  active runbooks), nine MCP tools covering the run loop, viz timeline +
  browse + detail, read-only FUSE rendering, a runbooks reference in the
  using-cc-notes skill, and the plugin record-router now routes
  runbook-shaped content to `runbook add` instead of `doc add`.

### Changed
- `cc-notes compact ID` now accepts all seven entity kinds — sprint, project,
  and runbook join note, doc, log, and task — resolving the id across every
  kind the way `show` and `history` already do. Its unknown-id failure is now
  the shared `no entity matches "<id>"`, unifying the message with `show` and
  `history`.
- Duplicate creates now report as a typed `DuplicateError` (sentinel
  `ErrDuplicate`) carrying the surviving snapshot: the CLI warns on stderr
  and echoes the survivor, and the `notes` client treats create as
  idempotent over content. Sprint and project also join the fold disk cache
  alongside runbook.

## [0.22.0] - 2026-07-08

### Changed
- **BREAKING: one flag vocabulary across the whole CLI.** Tasks, sprints, and
  projects take `--body` (was `--desc`); notes, docs, and logs take
  `--label`/`--add-label`/`--rm-label` (was `--tag`/`--add-tag`/`--rm-tag`);
  the `note`/`doc`/`log search` anchor filters match `list`
  (`--path`/`--dir`/`--branch`/`--commit`, were `--anchor-*`); the
  `note`/`doc supersede` undo is `--clear` (was `--remove`); `log append`
  takes `--entry` (was `-m`/`--message`), matching `log add`; `task edit`
  clears the assignee with `--no-assignee` (was `--unassign`) and gains
  `--branch`/`--backlog`; `sprint start` is now `sprint activate`; and
  `task criterion reset` is now `task criterion pending`. Hard cutover, no
  aliases. The MCP tools follow the same vocabulary: inputs renamed to match,
  `task_move` deleted, `sprint_start` renamed `sprint_activate`, and
  `task_criterion_reset` renamed `task_criterion_pending`. JSON output and the
  storage format are unchanged — entities still store and echo `description`
  and `tags`.
- The plugin's session bootstrap (`ensure-cc-notes.sh`) now enforces a version
  floor: an installed binary older than v0.22.0 is re-installed through the
  canonical installer, so upgraded hooks never run the new flags against an
  old binary.

### Added
- **Did-you-mean hints on the old spellings.** An unknown or renamed flag
  exits 2 with a hint naming the replacement (`unknown flag: --desc (did you
  mean --body?)`), and a removed or noun-less command names its successor
  (`task move` hints `task edit --branch`; a bare `list` hints `task list`,
  `note list`, …).
- **A kind-agnostic `cc-notes show ID`.** Like `history`, `compact`, and
  `blame`, `show` is a global read: it resolves any entity id — note, doc,
  log, task, sprint, or project — with no noun and renders that entity's
  noun-scoped `show` output, `--json` included.

### Removed
- `cc-notes task move` — re-home a task with `task edit --branch <branch>`
  (or `--backlog`); running `task move` exits 2 with that hint.

## [0.21.2] - 2026-07-08

### Fixed
- Listing cc-notes refs no longer races concurrent git ref writes — a ref
  rewritten mid-scan is retried instead of failing the listing.

### Security
- Builds moved to Go 1.26.5, picking up the fix for GO-2026-5856.

## [0.21.1] - 2026-07-08

### Fixed
- **A long-lived repo handle no longer misses objects from a freshly landed
  pack.** go-git seeds its packfile index on the first pack-touching read and
  never rescans it, so a pack that arrived afterward — a later `sync` fetch
  round, the `viz`/mount holder's long-running handle, or an external
  `repack`/`gc` — was invisible, surfacing as a spurious `incomplete chain …
  (shallow clone?)` in `sync` and as silently truncated graphs in `viz`. Object
  reads now reindex and retry once on a miss, mirroring git's own object
  database. Genuine misses report `missing from object database` on a full
  clone and keep the `missing (shallow clone)` hint only when `.git/shallow`
  actually exists.

### Changed
- Entity creation gained a best-effort exact-duplicate guard across all kinds,
  and the viz web UI refetches on SSE reconnect, lazy-loads the detail chunk,
  and keeps the lane-label gutter sticky.

## [0.21.0] - 2026-07-08

### Added
- **The viz web UI grew a Browse tab and a richer detail view.** Browse offers
  a faceted entity table, a task kanban, global search, and hash routing; the
  detail sidebar renders markdown, a typed trail, and attachment viewers.
  Merged-and-deleted branches are mined from the git DAG into real timeline
  lanes, backed by new `/api/entities` and `/api/blob` endpoints (blob access
  hardened per security review).

## [0.20.0] - 2026-07-07

### Fixed
- git-lfs batch auth honors `http.<url>.extraheader`, so attachment sync works
  behind header-injected credentials (for example, CI tokens).

### Changed
- The bundled hooks re-registered for capt-hook 8.7.0, adding the SessionEnd
  and SubagentStop hook groups.

## [0.19.0] - 2026-07-07

### Added
- **`--checkout` prefills the edit buffer.** `note add`/`doc add --checkout`
  now seed the frontmatter buffer from the `TITLE` and the
  `--when`/`--tag`/`--commit`/`--path`/`--dir`/`--branch` flags passed alongside
  it (commit anchors resolved to full SHAs), so the file-mode long-body flow
  starts from a filled-in template. `TITLE` is optional with `--checkout` — the
  buffer's `title` field or a leading `# ` heading supplies it.
- **Attachments ingest at apply and edit time.** `note add`/`doc add --apply
  <path> --attach <file>` ingests attachments in the same create transaction,
  and `--attach` on `note edit`/`doc edit` attaches to an entity that already
  exists — a name colliding with a live attachment needs `--replace`, and
  `--rm-attachment` still drops one.
- **An MCP server exposes the CLI as tools.** `cc-notes mcp` runs a stdio Model
  Context Protocol server whose tools mirror the command surface one-to-one
  (`doc_add`, `note_edit`, `task_claim`, …), each driving the CLI in-process so
  validation and `--json` output match exactly; a long doc or note body rides
  the `body` parameter instead of a checkout buffer. The Claude Code plugin
  wires it automatically through a bundled `.mcp.json`, surfacing the tools as
  `mcp__plugin_cc-notes_cc-notes__<tool>`. Setup and host-facing commands
  (`init`, mount, `gc`/`compact`, `viz`, `version`, the installers, and the
  `--checkout`/`--apply` file mode) stay CLI-only; a missing or older binary
  leaves the server `failed` in `/mcp` without disturbing the session, and
  `deniedMcpServers` turns it off while keeping the hooks.

### Changed
- The authoring guidance — the README, the using-cc-notes skill and CLI
  reference, and the capt-hook record nudges — now leads with the
  `--checkout`/`--apply` file-mode flow for a long doc or note body, keeping
  `--body`/`--body -` as the short-body path.
- **The capt-hook nudges follow the MCP server.** When the cc-notes MCP server
  is serving the repo — detected best-effort from its liveness marker under the
  git common dir, or a prior MCP tool call this session — the record, plan,
  claim, commit, and staleness nudges teach the matching `noun_verb` tools
  (the `body` param for a long doc/note body, `task_add` with `backlog=true` for
  shared work) instead of CLI lines. Detection only ever changes wording, never
  whether a nudge fires, so with the server absent the CLI wording is unchanged.
  A new nudge also catches an MCP record write (`doc_add`/`note_add`/…) whose
  body points at a purge-bound `/tmp` or scratchpad path.

## [0.18.0] - 2026-07-07

### Added
- **`cc-notes viz` serves a live visualization of branch and entity activity.**
  It binds a loopback web server (`--port`, default ephemeral; `--no-open` to
  skip the browser) with two views: a swimlane timeline of branches and entity
  lifecycle events with a detail panel per event, and a commit-DAG view with
  paging and per-commit entity badges. Both refresh live over SSE as refs move,
  driven by a poll watcher (`--poll`, default 2s), and the default window
  reaches back to the oldest fork still backed by a ref.
- **Release binaries embed the viz web UI.** The published binaries are built
  with `-tags webui` and carry the compiled single-page app; a plain `go build`
  stays pure Go and serves a no-UI notice instead.

## [0.17.0] - 2026-07-05

### Changed
- **Entity titles are capped at 256 bytes.** Every `add` and `edit --title` —
  notes, docs, logs, tasks, sprints, projects, and the `--checkout`/`--apply`
  file mode — rejects a longer (or empty) title with a hint naming where the
  content belongs on that command (`--body`, the checked-out buffer, `--desc`,
  or log entries). A title is a short handle: it renders on every lean line and
  floats into future agents' context, so the content belongs in the body.
  Existing entities with longer titles stay readable; only new writes through
  the CLI are held to the cap.
- **A doc must carry its body.** `doc add` with no `--body` and no `--attach`
  is rejected, and `doc edit --body ""` can no longer blank one. The failure
  this guards against: a durable handoff doc whose title is the whole payload
  and whose "full detail" lives in a purged `/tmp` scratchpad file.

### Added
- **The capt-hook pack nudges on ephemeral-path references.** A
  `cc-notes note/doc/log` write whose title or body text points at `/tmp`,
  `/var`, or a session scratchpad draws a static nudge to carry the content in
  the record instead — `--body -` for text, `--attach` for artifacts. Tag,
  branch, and anchor flag values are exempt, as is `--attach` itself. The
  memory mirror now clamps note titles to the cap instead of silently failing.

## [0.16.0] - 2026-07-04

### Fixed
- **Plain `git fetch --prune` can no longer delete unsynced cc-notes data.** The
  installed fetch refspec used to mirror straight into the canonical
  `refs/cc-notes/*` namespace, so a plain fetch or pull with `fetch.prune`
  enabled pruned locally-created, not-yet-synced entities and could
  force-clobber diverged tips. `init`/auto-install now install
  `+refs/cc-notes/*:refs/cc-notes-sync/<remote>/*` — the same tracking namespace
  `cc-notes sync` converges from — and upgrade the old refspec in place on every
  configured remote (announced on stderr) the next time any cc-notes command
  runs in a wired repo.

### Changed
- **A plain `git fetch`/`git pull` now stages incoming cc-notes data instead of
  publishing it into your view.** `cc-notes sync` folds staged data into the
  canonical refs, and `cc-notes reconcile` now folds before reconciling — so the
  capt-hook pack, the reconcile CI workflow, and the post-merge hook all see
  just-pulled work. Plain `git push` still publishes immediately.
  `reconcile --dry-run` stays strictly no-write and reads canonical state only.

## [0.15.1] - 2026-07-03

### Fixed
- The hooks pack migrated off the `captain_hook.command` module that newer
  capt-hook releases removed, and the plugin version was bumped so
  `claude plugin update` re-caches the fix — sessions on current capt-hook no
  longer break on the cc-notes hooks.

## [0.15.0] - 2026-07-02

### Added
- **File attachments on notes, docs, and logs.** A repeatable `--attach` on
  `note`/`doc`/`log add` and `log append` (replacing a live name requires
  `--replace`), `--rm-attachment` on edit, `attachment get`/`attachment path`,
  and `show` rendering attachments with a missing-locally marker plus the sync
  hint. The FUSE mount serves a read-only `/attachments/<short>/<name>` tree.
  Content is stored content-addressed under `<git-common-dir>/lfs` and moved by
  a native, stdlib-only git-lfs client — endpoint discovery, Batch API,
  `git credential` and ssh `git-lfs-authenticate` auth — with no `git-lfs`
  binary required. `cc-notes sync` uploads referenced objects before pushing
  refs and downloads missing content after the push loop converges, so an LFS
  outage never blocks publishing refs. Only `cc-notes sync` moves LFS content —
  a plain `git push` publishes refs without it.
- The plugin nudges evidence archiving: run output copied into a git worktree
  or machine-generated evidence files suggest `cc-notes log append --attach`,
  with a tripwire for files over 1 MB headed into git history.

### Fixed
- **`git lfs prune` can no longer delete un-synced attachments.**
  `lfs.pruneverifyremotealways` only verifies objects reachable from git
  commits; attachments are referenced solely by `refs/cc-notes/*`, so prune
  could drop an un-synced attachment without ever consulting the remote —
  unrecoverable data loss. The first attach now also installs
  `lfs.pruneverifyunreachablealways=true`, under which prune either retains the
  unverified object or refuses outright, naming it.

## [0.14.1] - 2026-07-02

### Changed
- CI and the embedded `cc-notes workflows install` template moved to the
  Node-24 actions baseline: `actions/checkout@v7` and
  `golangci-lint-action@v9`.

## [0.14.0] - 2026-06-27

### Added
- **Edit docs and notes as plain files, no mount required.** `doc` and `note`
  `add`/`edit` gain `--checkout`, which renders the entity to a
  Markdown+frontmatter buffer under `<git-common-dir>/cc-notes/edit/` and
  prints its path; `--apply` diffs the edited file against the checkout-time
  snapshot and commits the resulting ops, so concurrent edits to untouched
  fields merge; `--abort` discards the buffer. File-created entities are born
  verified, like a flag-driven add.

### Changed
- **The plugin now syncs and reconciles automatically.** The run-sync and
  reconcile nudges became side-effects: `cc-notes sync` runs after
  commit/claim/merge (once per turn), and `cc-notes reconcile --into <branch>`
  plus a sync run after merge/pull. No-remote and offline repos stay silent; a
  genuine push rejection surfaces a retry hint instead of being reported as
  synced. The pack itself split into themed modules.

## [0.13.1] - 2026-06-25

### Changed
- The release workflow adopted the shared `yasyf/homebrew-tap` actions —
  tag-on-main verification, Developer-ID import, formula render and publish.
  The pure/fuse build matrix and smoke tests are unchanged.

## [0.13.0] - 2026-06-24

### Changed
- **Every plugin nudge now follows one Surface/Record framework.** Surface
  (pull): `cc-notes relevant` floats the durable records anchored to a touched
  file, and a small LLM keeps the worthwhile subset — failing open, so a broken
  filter shows everything instead of hiding context. Record (push): a cheap
  static gate flags a candidate write and a small LLM confirms it is durable
  and routes it to exactly one primitive — note, doc, log, or task — failing
  closed to silence. A new commit handler always emits the `cc-task` link and
  sync reminders and routes durable decisions to a note or doc; a new plan
  handler extracts a plan's durable items into `cc-notes task add`.
  `NUDGE_MAX_FIRES` caps every Record router, with per-key dedup on top. The
  pack requires capt-hook ≥ 4.2.0.

## [0.12.0] - 2026-06-23

### Added
- **An append-only `log` primitive.** Logs are a sixth entity: like docs but
  append-only — a chronological journal (incident timeline, rollout log,
  debugging session) you keep adding to. Each entry's author and timestamp come
  from its own commit, and entries fold in linearization order, so cross-branch
  merges converge with no reconcile. `cc-notes log
  add/append/list/show/edit/rm/search`; logs float via `relevant` and count in
  `status`; the FUSE mount serves `/logs/<id>.md` with append-only write-back
  (tail appends create entries; editing an existing entry fails the flush).
- `cc-notes history <id>` (and per-noun `history` aliases): a read-only viewer
  over an entity's op-commit chain that reports the fields each edit changed —
  who set the status, when a label was added — with `--reverse`, `--limit`, and
  `--json`. No-op and bookkeeping commits are skipped; checkpoints render as a
  compacted marker.

## [0.11.0] - 2026-06-23

### Added
- **cc-pool memory writes now mirror into durable notes.** A PostToolUse
  handler auto-captures an agent's repo-relevant memory writes (feedback,
  project, and reference kinds; user memories are skipped) as git-synced notes
  keyed by a `memory:<slug>` tag, so the first write creates the note and a
  later edit updates it in place. It falls closed to silence on any cc-notes
  failure, so the memory write it shadows is never disturbed.

## [0.10.1] - 2026-06-23

### Added
- A nudge that routes durable internal writes — handoffs, decisions — toward
  `cc-notes doc`/`note`/`task` instead of loose files.

### Fixed
- Short commit anchors expand to the full sha at add time.

## [0.10.0] - 2026-06-23

### Changed
- **`init` now auto-mounts the repo's `.notes` by default.** `--no-mount` opts
  out, and the preference persists in the `cc-notes.autoMount` git config; the
  bundled SessionStart hook runs a self-gating, best-effort `mount --auto` so
  the mount returns each session. The mount holder now spawns from a stable
  binary copy, so the macOS "Network Volumes" grant survives version upgrades,
  and a holder left running at an older cc-notes version is converge-replaced
  on the next mount. A non-fuse binary never spawns or contacts a holder.

### Fixed
- The plugin version was bumped so `claude plugin update` re-caches the doc
  skill and handoff-detection nudge shipped in 0.9.0 — clients had stayed
  pinned to the older cache.

## [0.9.0] - 2026-06-23

### Added
- **A `doc` primitive for long-form agent handoffs.** A doc is the long-form
  sibling of a note: durable, repo-global, drift-checked guidance written for
  future agents, carrying a free-text `--when` read-trigger. It is a full peer
  of note/task/sprint/project across the model, refs, fold, store, CLI command
  group, and FUSE mount, with the note freshness lifecycle
  (verify/witness/expire/supersede). `cc-notes relevant` ranks docs alongside
  notes, the read- and edit-time hooks float a doc's pointer and verdict —
  title, `--when`, drift, a `doc show` hint — without ever surfacing the body,
  and an LLM-backed nudge suggests storing internal agent-handoff `.md` files
  as docs instead of loose files.

## [0.8.0] - 2026-06-23

### Changed
- **`cc-notes mount` now defaults to an in-repo `.notes` symlink.** Run with no
  `MOUNTPOINT` and the mount is served at the managed per-repo default under
  `~/.cc-notes/mnt` and presented in the repo as a `.notes` symlink into it
  (`cd .notes` to browse), kept out of git via `.git/info/exclude` — never the
  tracked `.gitignore`. The live mount stays out of the working tree: on macOS it
  is an NFS-backed fuse-t mount that doesn't belong inside a checkout that
  `git status`, editors, and watchers walk. Pass an explicit `MOUNTPOINT` to
  serve there instead, with no symlink. `mount --stop .notes` resolves the
  symlink to its managed mountpoint, and `--stop`/`--shutdown` remove the
  `.notes` symlink they created. A pre-existing real `.notes` file or directory
  is never clobbered — the mount fails fast before serving.

## [0.7.8] - 2026-06-21

### Added
- A `ccn` shorthand for `cc-notes`. Homebrew and the install script now drop a
  `ccn` symlink next to the binary, and the CLI shows `ccn` in its help/usage
  when invoked through it.
- **The plugin auto-installs the cc-notes binary on first session.** Enabling
  the plugin adds a bundled SessionStart hook that installs cc-notes when it is
  missing — preferring Homebrew, falling back to the release download verified
  against `SHA256SUMS.txt` — and is an instant no-op once present. A visible
  fallback nudge fires only when the binary is still absent after the
  bootstrap.

## [0.7.7] - 2026-06-20

### Changed
- `hooks install` and `init` now pin the hooks pack to
  `github:yasyf/cc-notes@latest` instead of a frozen version. capt-hook fetches
  the pack on demand and re-resolves `@latest` at most once a day, so a fresh
  clone needs no manual `pack update`.
- `skills install --global` enables the cc-notes plugin in the user's
  `~/.claude/settings.json`, so the using-cc-notes skill is discoverable in
  every repo.

### Fixed
- The adoption nudges now gate on the binary alone instead of also probing
  `refs/cc-notes/*` — a fresh repo with cc-notes installed gets the nudges that
  prompt the first write, instead of the chicken-and-egg silence that kept the
  pack dark until something had already written a ref.

## [0.7.6] - 2026-06-20

### Fixed
- `mount --shutdown` now reaps a wedged holder whose socket lingered after a
  timed-out shutdown, killing it by peer credentials — bounded, identity-gated,
  never by process name.

## [0.7.5] - 2026-06-19

Identical to 0.7.4 — `v0.7.5` tags the same commit as `v0.7.4`, re-tagged to
re-run the release pipeline. No code changes.

## [0.7.4] - 2026-06-19

### Fixed
- `cc-notes init` no longer aborts when capt-hook's pack loader tries to import
  the pack's standalone test script as a hook — the script moved under
  `plugin/hooks/tests/`, outside the loader's glob.

## [0.7.3] - 2026-06-19

### Fixed
- **macOS 15/26 no longer SIGKILLs the darwin binaries.** Release binaries are
  Developer-ID signed and notarized instead of ad-hoc signed; the fuse build
  disables library validation for FUSE-T.

## [0.7.2] - 2026-06-18

### Changed
- The Homebrew formula publishes via the shared `yasyf/homebrew-tap` publish
  action instead of per-repo tap git mechanics.

## [0.7.1] - 2026-06-18

### Added
- `cc-notes note expire`: an agent-asserted out-of-date flag, distinct from the
  time-based `STALE` verdict and softer than `rm`/`supersede`. Adds a
  top-precedence `EXPIRED` review verdict, an `--expired` filter, and `--clear`
  (also cleared on verify); who/when are stamped from the commit, like
  `note verify`.

### Changed
- **Homebrew installs moved to the shared tap:**
  `brew install yasyf/tap/cc-notes`. The formula publishes to
  `yasyf/homebrew-tap`, keeping the native-linux FUSE build; the in-repo tap is
  gone.

## [0.7.0] - 2026-06-18

### Added
- **A public in-process Go API.** A top-level `notes` package exposes a
  `Client`, opened over a repo dir, that drives projects, sprints, and tasks —
  create, read, list, resolve, and lifecycle transitions — and returns folded
  model snapshots, so external modules can drive cc-notes in-process instead of
  shelling out to the CLI. The facade's import graph is CGO/FUSE-free, so a
  consumer links it into a static binary.

### Changed
- `internal/model` was promoted to the public `model` package at the repo root.
  The wire format is keyed on JSON kind/tags, not the import path, so existing
  repos and entity ids are unaffected.

## [0.6.0] - 2026-06-18

### Added
- **`cc-notes relevant PATH`: predictive note ranking.** Ranks notes by
  relevance to a file and branch — path, dir-ancestor, sibling, current-branch,
  and merged-commit/branch signals, plus a cross-author boost — with
  `--attached` and `--worktree` modes and plain or `--json` output for hook
  consumption.
- Directory anchors: a first-class `dir` anchor kind across
  `note add/edit/list/search`, witnessed by the subtree tree oid so the anchor
  drifts when the subtree changes.
- The static session-start nudge became three capt-hook handlers, all
  warn-only and gated on adoption: a session-start task floater, a note-context
  floater that runs `cc-notes relevant` on read, and a staleness check that
  prompts verify/edit/supersede on edit.

### Fixed
- `scripts/install.sh` is POSIX sh (`#!/bin/sh`, no `pipefail`), so
  `curl | sh` works on dash-based systems such as Ubuntu; the reconcile
  workflow's installer step and the `init --ci` template are fixed the same
  way.

## [0.5.0] - 2026-06-18

### Changed
- **`cc-notes init` now does everything.** It still installs the
  `refs/cc-notes/*` refspecs, and now also wires up whatever the repository is
  ready for: when a `.claude/` directory exists it registers the cc-notes plugin
  in `.claude/settings.json` and enables the cc-notes capt-hook pack, and when a
  `.github/` directory exists it installs the reconcile GitHub Actions workflow.
  Pass `--no-ci` to skip the workflow or `--ci` to force it without a `.github/`
  directory. `init` never creates `.claude/` — it only wires Claude Code when the
  repo already uses it.
- `cc-notes skills install` now registers the cc-notes plugin in
  `.claude/settings.json` (the skill loads from the plugin, tracking the
  repository) instead of copying the skill tree into `.claude/skills/`. The
  capt-hook pack manifest moved from the repo root to `.claude/capt-hook.toml`.
- **BREAKING (UX): `cc-notes mount` now detaches by default.** A background mount
  holder serves the mount; the command prints the mountpoint and returns, and the
  mount outlives the invocation. `mount` no longer blocks, and **Ctrl-C no longer
  unmounts** — tear a mount down with `cc-notes mount --stop DIR` or a plain
  `umount DIR` (the holder reconciles either). To keep the old in-process
  lifecycle — block in the foreground, Ctrl-C unmounts — pass
  `cc-notes mount --foreground`. Three new flags drive the holder directly:
  `--stop DIR` unmounts one mount, `--list` prints what the holder serves, and
  `--shutdown` unmounts everything and stops the holder. One holder serves every
  repo mounted on the machine over a single socket (`~/.cc-notes/mounts.sock`)
  and outlives the CLI invocations that drive it. Failures keep the existing
  exit-code contract: a holder conflict (a busy dir, a foreign mount in the way,
  a base mismatch) exits 4; an unreachable holder, a denied "Network Volumes"
  grant, or a wedged unmount exits 1; a bad flag combination exits 2.
- The FUSE mount machinery now rides the shared
  [`github.com/yasyf/fusekit`](https://github.com/yasyf/fusekit) library: the
  detached mount-holder protocol, cgofuse-load panic recovery, pre-mount carcass
  cleanup, bounded teardown, and the NFS cache-defeat hooks. What the mount
  renders is unchanged.

## [0.4.0] - 2026-06-17

### Added
- Projects and Sprints, an optional planning layer over tasks and notes: new
  entity kinds at flat repo-wide refs `refs/cc-notes/{projects,sprints}/<id>`,
  carried by the existing `refs/cc-notes/*` refspec with no sync changes. A task
  holds independent sprint and project pointers and a sprint holds a project
  pointer — membership is an upward last-write-wins pointer, with tasks-in-sprint,
  sprints-in-project, and tasks-in-project derived as reverse indexes. Both kinds
  carry a lifecycle status; sprints add optional start/end dates. Managed via the
  new `cc-notes project` and `cc-notes sprint` command groups.
- Task validation criteria: structured `{id, text, script, status}` checks on a
  task, required by default at `cc-notes task add` (escape hatch
  `--no-validation-criteria`) and managed via the `cc-notes task criterion`
  subgroup. `cc-notes task done` is gated on every criterion being met, with
  `--force` to override.
- `cc-notes task validate`: runs a criterion's check script, explicit-only — it
  prints each script first, requires `--yes` or an interactive TTY, refuses a
  non-terminal stdin, and runs under `sh -c` with a bounded timeout in the repo.
  Validation scripts ride sync from untrusted peers, so they never execute
  implicitly.
- FUSE mount support for the planning layer: flat editable `/sprints/<id>.json`
  and `/projects/<id>.json` files (criteria editable, membership display-only),
  plus a read-only nested browse tree at
  `/projects/<p>/sprints/<s>/tasks/<t>.json` whose leaves symlink to the real
  flat task files.

### Changed
- The cc-notes nudge hooks now ship as a capt-hook pack. `cc-notes hooks install`
  enables it via `capt-hook pack add github:yasyf/cc-notes`, which caches the
  pinned pack and wires the events into `.claude/settings.local.json`, instead of
  vendoring `cc_notes.py` and hand-wiring `.claude/settings.json`.

## [0.3.0] - 2026-06-16

Tasks are now global. A task's branch is a folded attribute, not part of its
ref, so one task crosses branches, merges, and machines while every agent shares
a single backlog.

### Added
- Leases on claimed tasks: a claim opens a lease with a heartbeat, so a crashed
  agent's grab never locks work forever. Any edit, comment, or
  `cc-notes task renew` refreshes the heartbeat; `cc-notes task stale` lists
  leases past the TTL, and `cc-notes task claim --steal` reclaims an expired one
  — a holder who renewed in time keeps it. Set the threshold with
  `cc-notes.leaseTTL` in git config.
- Note verification as first-class state: every note is born verified against
  the current HEAD with a witness snapshot of its anchored content.
  `cc-notes note verify` re-confirms a fact, `cc-notes note supersede` records a
  replacement and drops the old note from default listings, and
  `cc-notes note review` flags decay as `DRIFTED`, `STALE`, or `UNVERIFIED` —
  each verdict computed by the reader against a threshold, never stored.
- `cc-notes reconcile`: carries a merged branch's open and in-progress tasks
  onto the target branch by rewriting their branch attribute, idempotently.
  It auto-discovers branches whose tip is an ancestor of the target; `--from`
  with `--force` handles squash and rebase merges that break the ancestry test.
  Wired into CI via the `reconcile.yml` workflow and, optionally, a git
  post-merge hook installed by `cc-notes init --hook`.
- Commit-to-task linkage: a `cc-task: <id>` git trailer or `cc-notes task done`
  anchors a commit onto the task it implemented, and `cc-notes blame <sha>`
  reads the link back to name the task(s) a commit built.
- Compaction and garbage collection for long-lived entities:
  `cc-notes compact` checkpoints an entity's op-log into a seed the fold replays
  from for cheap reads, and `cc-notes gc` prunes the local fold cache, with
  `--prune-remote` deleting tombstoned refs on the default remote.
- A Claude Code plugin under `plugin/`: the `using-cc-notes` skill bundling the
  full CLI reference, plus capt-hook enforcement hooks, installed by
  `cc-notes skills install` and `cc-notes hooks install`.

### Changed
- Tasks live at one flat ref per id, `refs/cc-notes/tasks/<id>`, with a `branch`
  attribute that folds last-write-wins. The branch-less backlog is the shared
  cross-agent queue; `task list` and `task ready` default to the current branch,
  `cc-notes task move` re-homes a task, and `cc-notes task start` claims a
  backlog item and moves it onto your branch in one step.
- `cc-notes sync` drives git directly, so it converges `refs/cc-notes/*` even
  under jj, whose git bridge only carries `refs/heads/*`. reconcile and sync are
  explicit commands, not git hooks, because jj fires no hooks and would silently
  strand a merged branch's tasks.
- README rewritten to the global-task model: coordinating across branches with
  the backlog and leases, reconcile after a merge, and keeping notes honest.

### Removed
- The per-branch task model, where a task's branch was encoded in its ref name.
- The branch-scoped `promote` command, replaced by `cc-notes reconcile`.

## [0.2.0] - 2026-06-12

cc-notes is now a Go program. The Python `note add/list` scaffold, published to
PyPI as `cc-notes` 0.1.0, is superseded by prebuilt binaries from GitHub
Releases.

### Added
- Git-native storage: notes and tasks live as custom objects on
  `refs/cc-notes/*` — notes globally, tasks per-branch — pushed and pulled with
  the repo and invisible in checkouts and the GitHub UI.
- Event-log CRDT: each entity is a chain of operation-pack commits; concurrent
  edits union-merge, fold deterministically, last-write-wins per field, and
  `claim` is conditional first-wins.
- Agents-first CLI: `note` and `task` noun groups with lean tab-separated
  output, `--json` everywhere, and branchable exit codes — 0 ok, 1 error,
  2 usage, 3 not-found, 4 conflict, 5 ambiguous.
- Task lifecycle: claim, ready, done, cancel, dependencies, and branch-scoped
  `promote`.
- Sync: `cc-notes init` installs refspecs so plain `git push`/`git pull` carry
  the data; `cc-notes sync` converges diverged refs via union merge.
- Optional FUSE layer: `cc-notes mount DIR` exposes notes as Markdown and tasks
  as JSON, write-through on save, backed by FUSE-T on macOS and libfuse on
  Linux.
- Release binaries: pure static builds plus FUSE variants for
  darwin/linux × amd64/arm64, with `scripts/install.sh` for installation.
- Homebrew install: an in-repo tap formula
  (`brew install yasyf/cc-notes/cc-notes`), auto-bumped by the release
  workflow on stable tags.

### Changed
- README rewritten as a slim front door: pitch, install, quickstart, and
  command pointers. The agent-contract, syncing, mounting, and architecture
  deep dives are gone.

### Removed
- The Python package and its PyPI release pipeline.
- The Python-era documentation site (GitHub Pages) and the repo homepage link
  that pointed at it.

[Unreleased]: https://github.com/yasyf/cc-notes/compare/v0.25.0...HEAD
[0.25.0]: https://github.com/yasyf/cc-notes/compare/v0.24.0...v0.25.0
[0.24.0]: https://github.com/yasyf/cc-notes/compare/v0.23.0...v0.24.0
[0.23.0]: https://github.com/yasyf/cc-notes/compare/v0.22.0...v0.23.0
[0.22.0]: https://github.com/yasyf/cc-notes/compare/v0.21.2...v0.22.0
[0.21.2]: https://github.com/yasyf/cc-notes/compare/v0.21.1...v0.21.2
[0.21.1]: https://github.com/yasyf/cc-notes/compare/v0.21.0...v0.21.1
[0.21.0]: https://github.com/yasyf/cc-notes/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/yasyf/cc-notes/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/yasyf/cc-notes/compare/v0.18.0...v0.19.0
[0.18.0]: https://github.com/yasyf/cc-notes/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/yasyf/cc-notes/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/yasyf/cc-notes/compare/v0.15.1...v0.16.0
[0.15.1]: https://github.com/yasyf/cc-notes/compare/v0.15.0...v0.15.1
[0.15.0]: https://github.com/yasyf/cc-notes/compare/v0.14.1...v0.15.0
[0.14.1]: https://github.com/yasyf/cc-notes/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/yasyf/cc-notes/compare/v0.13.1...v0.14.0
[0.13.1]: https://github.com/yasyf/cc-notes/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/yasyf/cc-notes/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/yasyf/cc-notes/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/yasyf/cc-notes/compare/v0.10.1...v0.11.0
[0.10.1]: https://github.com/yasyf/cc-notes/compare/v0.10.0...v0.10.1
[0.10.0]: https://github.com/yasyf/cc-notes/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/yasyf/cc-notes/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/yasyf/cc-notes/compare/v0.7.8...v0.8.0
[0.7.8]: https://github.com/yasyf/cc-notes/compare/v0.7.7...v0.7.8
[0.7.7]: https://github.com/yasyf/cc-notes/compare/v0.7.6...v0.7.7
[0.7.6]: https://github.com/yasyf/cc-notes/compare/v0.7.5...v0.7.6
[0.7.5]: https://github.com/yasyf/cc-notes/compare/v0.7.4...v0.7.5
[0.7.4]: https://github.com/yasyf/cc-notes/compare/v0.7.3...v0.7.4
[0.7.3]: https://github.com/yasyf/cc-notes/compare/v0.7.2...v0.7.3
[0.7.2]: https://github.com/yasyf/cc-notes/compare/v0.7.1...v0.7.2
[0.7.1]: https://github.com/yasyf/cc-notes/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/yasyf/cc-notes/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/yasyf/cc-notes/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/yasyf/cc-notes/compare/v0.4.1...v0.5.0
[0.4.0]: https://github.com/yasyf/cc-notes/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/yasyf/cc-notes/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yasyf/cc-notes/releases/tag/v0.2.0
