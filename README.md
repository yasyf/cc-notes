# cc-notes

![cc-notes banner](docs/assets/readme-banner.webp)

[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/license-PolyForm--Noncommercial--1.0.0-blue.svg)](LICENSE)

Notes and tasks for AI agents, stored inside your repo's git object database.

cc-notes gives agents a durable place to write things down between sessions: a notes and task-tracking layer that lives on hidden `refs/cc-notes/*` refs in the repository itself. Everything is versioned in the object database, syncs with a plain `git push` and `git pull`, and never appears in checkouts, diffs, or the GitHub UI. No server, no sidecar database, no dotfile clutter — if you have the repo, you have the data.

Tasks scope to the branch you're on; notes are repo-global, with optional anchors tying them to commits, paths, or branches. Under the hood, each entity is an event-log CRDT (conflict-free replicated data type) riding git as its transport — an approach pioneered by [git-bug](https://github.com/git-bug/git-bug).

## Install

```sh
brew tap yasyf/cc-notes https://github.com/yasyf/cc-notes
brew install yasyf/cc-notes/cc-notes
```

macOS and Linux. The formula installs the prebuilt binary for your platform, FUSE-capable wherever a FUSE (Filesystem in Userspace) build ships; `cc-notes mount` on macOS needs `brew install macos-fuse-t/cask/fuse-t` (on Linux, `fuse3`). Everything else works without it.

No Homebrew? The install script picks the right binary for your platform (preferring the FUSE-capable variant when available) and drops it in `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh
```

Or grab an asset directly from [GitHub Releases](https://github.com/yasyf/cc-notes/releases) — each release ships a `SHA256SUMS.txt` file alongside. Pick the binary for your platform:

| Platform | Binary | With FUSE mount support |
|---|---|---|
| macOS Apple Silicon | `cc-notes_darwin_arm64` | `cc-notes_darwin_arm64_fuse` |
| macOS Intel | `cc-notes_darwin_amd64` | `cc-notes_darwin_amd64_fuse` |
| Linux x86-64 | `cc-notes_linux_amd64` | `cc-notes_linux_amd64_fuse` |
| Linux arm64 | `cc-notes_linux_arm64` | — |

Go users can build from source instead:

```sh
go install github.com/yasyf/cc-notes/cmd/cc-notes@latest
```

## Quickstart

Wire up a repo and run one task through its lifecycle in under five minutes. From any clone with a remote:

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
```

Add a task. Every mutation echoes the entity's new state as a lean tab-separated line:

```console
$ cc-notes task add "Add retry backoff to the API client" --priority 1 --label api
d82c087	open	P1	-	Add retry backoff to the API client
```

`task ready` lists what an agent can pick up right now — open, unassigned, and with no open blockers; `claim` and `done` move it through the lifecycle:

```console
$ cc-notes task ready
d82c087	open	P1	-	Add retry backoff to the API client
$ cc-notes task claim d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

Drop a note anchored to the file it describes:

```console
$ cc-notes note add "Auth tokens expire after 15 minutes" --path services/auth/login.go --tag design --body "Refresh client-side before expiry; the API returns 401 with no Retry-After header."
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes
```

Publish to the remote with `cc-notes sync`:

```console
$ cc-notes sync
pushed: 2
rounds: 1
```

After `init`, plain `git push` and `git pull` carry the refs alongside your branches too.

Verify the finished task's full record — every note, task, and sync command takes `--json`:

```console
$ cc-notes task show d82c087 --json
{"id":"d82c087ca80fbb9c7956cec15dfdf8f01486d1e2","branch":"main","title":"Add retry backoff to the API client","description":"","type":"task","status":"done","priority":1,"assignee":"ada \u003cada@example.com\u003e","labels":["api"],"blocked_by":[],"blocks":[],"parent":null,"comments":[],"created_at":"2026-06-12T21:14:49Z","updated_at":"2026-06-12T21:15:11Z","started_at":"2026-06-12T21:15:11Z","closed_at":"2026-06-12T21:15:11Z"}
```

## Coordinating across branches

Tasks scope to the branch they're created on, so a merge carries the code over but leaves the merged branch's tasks behind on their own namespace. The canonical loop closes that gap: branch off, capture work with `cc-notes task add`, commit and merge as usual, then promote the merged branch's open tasks onto the target with `cc-notes reconcile` and converge with `cc-notes sync`.

```console
$ cc-notes reconcile --into main
scanned: 1
merged: 1
promoted: 2
into: main
feature/x:
08118da	open	P1	-	build the widget
b932fd9	open	P2	-	test the widget
$ cc-notes sync
pushed: 2
rounds: 1
```

`reconcile` auto-discovers the branches fully merged into the target and is idempotent — safe to re-run and to wire into CI.

Keep notes honest as they age. Supersede a changed decision by editing it (`cc-notes note edit <id>`) or retire it with `cc-notes note rm <id>` (a tombstone — listings drop it, history keeps it); tag a context-only note `stale` or `superseded` to filter it out; and anchor each note to the commit or path it describes (`--commit`, `--path`) so drift stays visible.

## Going further

Run `cc-notes task --help` and `cc-notes note --help` for the full command set: tasks add `list`, `edit`, `comment`, `dep`/`undep`, `cancel`, and `promote`; notes add `list`, `edit`, `search`, and `rm`. With a `_fuse` binary, `cc-notes mount [DIR]` exposes everything as an editable filesystem — notes as Markdown, tasks as JSON. `DIR` is created if it does not exist; omit it to use a managed per-repo default under `~/.cc-notes/mnt`.

## License

[PolyForm-Noncommercial-1.0.0](LICENSE)
