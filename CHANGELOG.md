# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
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

[Unreleased]: https://github.com/yasyf/cc-notes/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/yasyf/cc-notes/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/yasyf/cc-notes/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yasyf/cc-notes/releases/tag/v0.2.0
