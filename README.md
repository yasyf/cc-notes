# cc-notes

![cc-notes banner](docs/assets/readme-banner.webp)

[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/license-PolyForm--Noncommercial--1.0.0-blue.svg)](LICENSE)

Notes and tasks for AI agents, stored inside your repo's git object database.

cc-notes gives agents a durable place to write things down between sessions: a notes and task-tracking layer that lives on hidden `refs/cc-notes/*` refs in the repository itself. Everything is versioned in the object database, syncs with a plain `git push` and `git pull` (or `cc-notes sync` under jj, whose git bridge skips the cc-notes refs), and never appears in checkouts, diffs, or the GitHub UI. No server, no sidecar database, no dotfile clutter — if you have the repo, you have the data.

Tasks are global — one flat ref per task at `refs/cc-notes/tasks/<id>`, with a mutable `branch` attribute and a shared backlog (any task with no branch) that every agent on every branch can see; `task list` and `task ready` default to your current branch. Notes are repo-global, optionally anchored to commits, paths, or branches, and verified as first-class state: re-confirm a fact, supersede a changed one, and catch drift mechanically, not by eye. Under the hood, each entity is an event-log CRDT (conflict-free replicated data type) riding git as its transport — an approach pioneered by [git-bug](https://github.com/git-bug/git-bug).

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

Capture shared work on the backlog. Every mutation echoes the entity's new state as a lean tab-separated line:

```console
$ cc-notes task add "Add retry backoff to the API client" --backlog --priority 1 --label api
d82c087	open	P1	-	Add retry backoff to the API client
```

Orient with `cc-notes status` — a read-only board of the shared backlog, your branch's open and in-progress tasks, every in-progress claim across branches flagged fresh or STALE, and how many notes need review:

```console
$ cc-notes status
backlog
  d82c087	open	P1	-	Add retry backoff to the API client
your branch (main)
notes: 0 total, 0 need review
```

`task start` claims the task — deterministic first-wins — and moves it onto your current branch in one step; `task done` closes it and anchors your HEAD commit onto the task:

```console
$ cc-notes task start d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client
$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

Drop a note anchored to the file it describes. A note is born verified against the current HEAD:

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

After `init`, plain `git push` and `git pull` carry the refs alongside your branches too. Under jj it's different: `jj git push`/`jj git fetch` bridge only `refs/heads/*`, so the `refs/cc-notes/*` refs stay behind — use `cc-notes sync`, which drives git directly and carries them regardless.

Verify the finished task's full record — every note, task, sync, reconcile, and status command takes `--json`:

```console
$ cc-notes task show d82c087 --json
{"id":"d82c087ca80fbb9c7956cec15dfdf8f01486d1e2","branch":"main","title":"Add retry backoff to the API client","description":"","type":"task","status":"done","priority":1,"assignee":"ada \u003cada@example.com\u003e","labels":["api"],"blocked_by":[],"blocks":[],"parent":null,"comments":[],"commits":["4f1c2ab9d3e0c7b1f6e8a2d5c4b3a190f8e7d6c5"],"lease":{"holder":"ada \u003cada@example.com\u003e","heartbeat":"2026-06-12T21:15:11Z"},"created_at":"2026-06-12T21:14:49Z","updated_at":"2026-06-12T21:15:11Z","started_at":"2026-06-12T21:15:11Z","closed_at":"2026-06-12T21:15:11Z"}
```

## Coordinating across agents and branches

Tasks are global, so several agents — across machines, sessions, or branches — coordinate through one remote. The shared **backlog** (`cc-notes task add --backlog`) is the cross-agent queue, and `cc-notes status` boards it alongside your branch's work and every in-progress claim. `cc-notes task start <id>` grabs a backlog item: it claims the task and moves it onto your current branch in one step. Claims are deterministic first-wins, so two agents racing for the same task before either syncs both fold to the same winner — the loser sees it already taken, not a corrupt double-claim.

A claim opens a **lease**, so a crashed agent's grab never locks work forever. Any edit, comment, or `cc-notes task renew <id>` refreshes the heartbeat; `cc-notes task stale` lists leases past the TTL, and `cc-notes task claim <id> --steal` reclaims one — a holder who renewed in time keeps it. Set the threshold with `cc-notes.leaseTTL` in git config, kept larger than your sync interval.

Re-home a task by hand with `cc-notes task move <id> --to <branch>` (`--backlog` sends it back to the backlog). After a merge, a merged branch's still-open tasks keep their old branch until you carry them onto the target with `cc-notes reconcile --into <target>`, then converge with `cc-notes sync`:

```console
$ cc-notes reconcile --into main
scanned: 1
merged: 1
carried: 2
into: main
feature/x:
08118da	open	P1	-	build the widget
b932fd9	open	P2	-	test the widget
$ cc-notes sync
pushed: 2
rounds: 1
```

`reconcile` auto-discovers the branches fully merged into the target — a branch whose tip is an ancestor of the target tip — and is idempotent, safe to re-run and to wire into CI. It is an explicit command, not a git hook, precisely because jj fires no git hooks: an agent driving the repo through jj would silently skip a hook and strand the merged branch's tasks.

Link a commit to the task it implemented with a `cc-task: <id>` git trailer; `cc-notes task done <id>` also anchors your HEAD commit onto the task, so `cc-notes task show` lists what built it. `cc-notes blame <sha>` reads the link back — given a commit, it names the task(s) it implemented.

```console
$ git commit -m "Clamp API retry backoff at 30s

cc-task: d82c087"
$ cc-notes blame 4f1c2ab
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client
```

## Keeping notes honest

A note is a claim about the code, and claims decay — so verification is first-class, not a tag convention you maintain by hand. Every note is born verified against the current HEAD, with a witness snapshot of its anchored content. Re-confirm a fact that still holds with `cc-notes note verify <id>`; when a decision changes, record the replacement with `cc-notes note supersede <old> --by <new>` — the old note drops from default listings and points at the new one, history intact.

`cc-notes note review` surfaces decay mechanically, tagging each flagged note `DRIFTED` (an anchored path or commit changed since it was last verified), `STALE` (verified too long ago), or `UNVERIFIED` (never verified):

```console
$ cc-notes note review
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes	DRIFTED
```

None of these verdicts are stored — each is computed by the reader against a threshold, so they read identically across replicas. At scale, `cc-notes compact <id>` checkpoints a long op-log for cheap folds and `cc-notes gc --prune-remote` reclaims tombstoned refs.

## Going further

Run `cc-notes task --help` and `cc-notes note --help` for the full command set: tasks add `list`, `ready`, `backlog`, `edit`, `comment`, `dep`/`undep`, `cancel`, `move`, `renew`, and `stale`; notes add `list`, `edit`, `search`, `verify`, `supersede`, and `review`. The bundled Claude Code plugin under `plugin/` ships the `using-cc-notes` skill with the complete CLI reference. With a `_fuse` binary, `cc-notes mount [DIR]` exposes everything as an editable filesystem — notes as Markdown, tasks as JSON. `DIR` is created if it does not exist; omit it to use a managed per-repo default under `~/.cc-notes/mnt`.

## License

[PolyForm-Noncommercial-1.0.0](LICENSE)
