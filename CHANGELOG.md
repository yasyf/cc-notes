# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

### Changed
- README rewritten as a slim front door: pitch, install, quickstart, and
  command pointers. The agent-contract, syncing, mounting, and architecture
  deep dives are gone.

### Removed
- The Python package and its PyPI release pipeline.
- The Python-era documentation site (GitHub Pages) and the repo homepage link
  that pointed at it.

[Unreleased]: https://github.com/yasyf/cc-notes/commits/main
