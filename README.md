# cc-notes

![cc-notes banner](https://github.com/yasyf/cc-notes/raw/main/docs/assets/readme-banner.webp)

[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/license-PolyForm--Noncommercial--1.0.0-blue.svg)](https://github.com/yasyf/cc-notes/blob/main/LICENSE)

Notes and tasks for AI agents, stored inside your repo's git object database.

cc-notes gives agents a durable place to write things down between sessions: a notes and task-tracking layer that lives on hidden `refs/cc-notes/*` refs in the repository itself. Everything is versioned in the git ODB, syncs with a plain `git push` and `git pull`, and never appears in checkouts, diffs, or the GitHub UI. No server, no sidecar database, no dotfile clutter — if you have the repo, you have the data.

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

Or grab an asset directly from [GitHub Releases](https://github.com/yasyf/cc-notes/releases). Each release ships a `SHA256SUMS` file alongside:

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

n.b. the `cc-notes` 0.1.0 package on PyPI is the retired Python prototype. It is not this tool and gets no further releases.

## Quickstart

Wire up a repo and run one task through its lifecycle in under five minutes. From any clone with a remote:

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
```

Add a task. Every mutation echoes the entity's new state as a lean tab-separated line, `<id>\t<status>\t<priority>\t<assignee>\t<title>`:

```console
$ cc-notes task add "Wire retry backoff into sync" --priority 1 --label sync
e2d9f83	open	P1	-	Wire retry backoff into sync
```

`task ready` lists what an agent can pick up right now — open, unassigned, and with no open blockers:

```console
$ cc-notes task ready
e2d9f83	open	P1	-	Wire retry backoff into sync
$ cc-notes task claim e2d9f83
e2d9f83	in_progress	P1	ada <ada@example.com>	Wire retry backoff into sync
$ cc-notes task done e2d9f83
e2d9f83	done	P1	ada <ada@example.com>	Wire retry backoff into sync
```

Notes are repo-global and can anchor to commits, paths, or branches:

```console
$ cc-notes note add "Sync design: union merges, never force" --path internal/sync/sync.go --tag design --body "Diverged entity refs merge with both tips as parents; ops from both sides fold deterministically."
627408b	2026-06-12	design	Sync design: union merges, never force
```

Publish to the remote:

```console
$ cc-notes sync
pushed: 3
rounds: 1
```

And when a script needs structure, every command takes `--json`:

```console
$ cc-notes task show e2d9f83 --json
{"id":"e2d9f83baa83c0f030b69c6f9f723c873a37178e","branch":"main","title":"Wire retry backoff into sync","description":"","type":"task","status":"done","priority":1,"assignee":"ada \u003cada@example.com\u003e","labels":["sync"],"blocked_by":[],"blocks":["944e2e6e9a18afa6f3f6670daf08117ea42ec7a7"],"parent":null,"comments":[],"created_at":"2026-06-12T15:50:53Z","updated_at":"2026-06-12T15:51:02Z","started_at":"2026-06-12T15:51:02Z","closed_at":"2026-06-12T15:51:02Z"}
```

Run `cc-notes task --help` and `cc-notes note --help` for the full command set: `edit`, `comment`, `dep`/`undep`, `cancel`, `promote`, `search`, `rm`.

## The agent contract

The CLI is built for agents first: deterministic output, no color, no tables, no prompts. stdout carries data only; errors are one greppable stderr line, `<label>: <message>`.

**Lean lines.** Lists and mutations print one tab-separated line per entity. Tasks: `<id7>\t<status>\tP<0-3>\t<assignee|->\t<title>`. Notes: `<id7>\t<YYYY-MM-DD updated>\t<tags|->\t<title>`. A mutation reloads the entity after the write and prints the resulting line, so the echo doubles as verification.

**`--json`.** One compact document per line: fixed field order, full 40-hex ids, RFC 3339 UTC timestamps, sorted sets, `null` for unset optionals.

**Exit codes.** Branch on these instead of parsing stderr:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | error (git failure, bad state) |
| 2 | usage (unknown flag, wrong arity) |
| 3 | entity not found |
| 4 | conflict (lost claim race, illegal transition) |
| 5 | ambiguous id prefix |

`done` on an already-done task is a conflict, so a retry loop can't silently double-complete:

```console
$ cc-notes task done e2d9f83
conflict: e2d9f83 already done
$ echo $?
4
```

**Identity.** Writes attribute to git's author identity. Set `CC_NOTES_ACTOR` to override per invocation — handy when several agents share one checkout:

```console
$ CC_NOTES_ACTOR="reviewer <agent@example.com>" cc-notes task claim 944e2e6
944e2e6	in_progress	P2	reviewer <agent@example.com>	Cut the v0.2.0 release
```

`claim` is atomic across replicas: when two actors race for the same task, exactly one wins and the other exits 4, even if they raced offline and merge later.

## Tasks are branch-scoped; notes are global

Tasks live in a namespace per branch (`refs/cc-notes/tasks/<branch>/<id>`), so `task list` and `task ready` default to the work in front of you on the current branch, and task ids resolve within that namespace — `task claim`, `done`, and friends act on the current branch's tasks. `task promote` moves tasks to another branch's namespace, say surviving tasks from a feature branch back to `main` after a merge:

```console
$ cc-notes task promote --to main f7e58a4
f7e58a4	open	P1	-	Backport backoff fix
```

Notes are repo-global, with optional anchors tying them to what they describe: `--commit SHA`, `--path some/file.go`, `--branch name` (all repeatable). `note list` filters by any of them.

## Syncing

`cc-notes init` wires your remote so ordinary git commands carry the data. It adds exactly these lines to `.git/config` (idempotently — rerunning changes nothing):

```ini
[core]
	logAllRefUpdates = always
[remote "origin"]
	fetch = +refs/cc-notes/*:refs/cc-notes/*
	push = HEAD
	push = refs/cc-notes/*:refs/cc-notes/*
```

`push = HEAD` preserves git's default branch-push behavior, which any explicit push refspec would otherwise override. `logAllRefUpdates = always` extends the reflog to entity refs as a safety net.

After that, plain `git push` and `git pull` move notes and tasks alongside your branches. The remote stores the refs but they're effectively invisible: GitHub serves them to fetches yet shows no trace in the UI — no branches, no files, no PR noise. Verify they're there with `git ls-remote origin 'refs/cc-notes/*'`.

Three sharp edges are worth knowing, all consequences of riding plain git:

1. **A diverged entity ref makes `git push` exit 1.** If someone else changed the same note or task since you last synced, your branch still pushes — only the entity ref is rejected:

   ```console
   $ git push
   To /tmp/scratch/remote.git
      57343dc..b89cc42  HEAD -> main
    ! [rejected]        refs/cc-notes/tasks/main/944e2e6e9a18afa6f3f6670daf08117ea42ec7a7 -> refs/cc-notes/tasks/main/944e2e6e9a18afa6f3f6670daf08117ea42ec7a7 (fetch first)
   error: failed to push some refs to '/tmp/scratch/remote.git'
   # git's standard fetch-first hints follow, trimmed
   $ cc-notes sync
   merged: 1
   pushed: 1
   rounds: 1
   ```

   `cc-notes sync` is the convergence path: it union-merges the diverged history — both sides' changes survive, nothing is overwritten — and pushes the result.

2. **A plain `git fetch` (or `git pull`) force-overwrites a diverged local entity ref** with the remote's version; the fetch refspec is forced so stale clones always converge. Your local changes are not folded in — they're parked in the reflog (`git rev-parse '<ref>@{1}'`), which `init` enabled for all refs. If you've made entity edits offline, run `cc-notes sync` instead of a bare fetch; sync merges instead of clobbering.

3. **`push.default` no longer applies to the wired remote.** Git ignores `push.default` for any remote with an explicit push refspec, which is why `init` writes `push = HEAD` — plain `git push` keeps pushing the current branch to its same-named remote branch. If you'd tuned `push.default` (`upstream`, say), pushes to this remote follow the `HEAD` rule instead. Wiring also happens implicitly — the first mutating cc-notes command installs the same lines — and announces what it added on stderr.

`cc-notes sync --remote <name>` targets a non-default remote; `--json` reports the created/fast-forwarded/merged/pushed counts.

## Mounting as files

`cc-notes mount DIR` exposes everything as an editable filesystem — notes as Markdown, tasks as JSON — so you can browse and edit in your editor while the CLI and other agents keep writing underneath. Saves commit straight to the git ODB.

It needs a FUSE-capable binary (the `_fuse` release assets, or `go build -tags fuse`) and a FUSE implementation:

| Platform | Setup |
|---|---|
| macOS | `brew install fuse-t` |
| Linux | `apt install fuse3` |

The pure binary fails fast with the same instructions:

```console
$ cc-notes mount /tmp/ccn-mount
error: fuse support unavailable: this binary was built without FUSE support; rebuild with -tags fuse, or download the _fuse release binary (macOS: brew install fuse-t; Linux: apt install fuse3)
$ echo $?
1
```

The mount runs in the foreground; Ctrl-C unmounts. Notes render as Markdown with YAML frontmatter, tasks as pretty JSON in the same shape as `task show --json`, under branch directories mirroring `refs/heads/*`:

```text
notes/
  627408b-sync-design-union-merges-never-force.md
tasks/
  fix/sync-backoff/
  main/
    944e2e6.json
    e2d9f83.json
    f7e58a4.json
```

```console
$ cat /tmp/ccn-mount/notes/627408b-sync-design-union-merges-never-force.md
---
id: 627408bac50bf7e93a40d15526bbba2ba33ea256
title: 'Sync design: union merges, never force'
tags: [design]
paths: [internal/sync/sync.go]
author: ada <ada@example.com>
created: "2026-06-12T15:51:02Z"
updated: "2026-06-12T15:51:02Z"
---
Diverged entity refs merge with both tips as parents; ops from both sides fold deterministically.
```

In a note's frontmatter, `title`, `tags`, and the `commits`/`paths`/`branches` anchors are editable, plus the Markdown body below it. In a task file, `title`, `description`, `status`, `priority`, and `labels` are editable. Saving a file diffs it against the state at open time and commits only the fields you changed, so concurrent edits to different fields merge cleanly. Changing an immutable field (ids, authors, timestamps, comments) rejects the save with the reason on the daemon's stderr:

```text
2026/06/12 08:54:59 cc-notes mount: rejected save: immutable field: branch
```

macOS specifics: FUSE-T mounts appear as NFS volumes, and the first mount needs a one-time "Network Volumes" permission grant in the Privacy & Security pane of System Settings. Because NFS doesn't reliably deliver error codes to editors, a rejected save reverts in your editor with no visible error; the daemon's stderr carries the reason, so keep that terminal visible while editing.

## Architecture

Each note and task is an append-only chain of operation commits on its own ref — `refs/cc-notes/notes/<id>` globally, `refs/cc-notes/tasks/<branch>/<id>` per branch. A commit's tree holds a single `ops.json` blob (an op-pack); the entity's id is the hash of its root commit. Current state is a deterministic fold over the chain: last-writer-wins per field, append-only comments, and a conditional first-wins rule for `claim`. Concurrent histories never conflict — sync joins them with a union merge commit and every replica folds the union to the same state. It's an event-log CRDT wearing git as its transport, an approach pioneered by [git-bug](https://github.com/git-bug/git-bug).

## Development

```sh
go vet ./... && go test -race -count=1 ./...   # pure build, full suite
go test -race -count=1 -tags fuse ./...        # adds the FUSE layer (needs cgo + fuse-t/fuse3)
```

Release binaries build from tags: pushing `v*` cross-compiles the assets above, generates `SHA256SUMS`, and cuts a GitHub release. See [AGENTS.md](AGENTS.md) and [STYLEGUIDE.md](STYLEGUIDE.md) for conventions.

## License

[PolyForm Noncommercial 1.0.0](LICENSE)
