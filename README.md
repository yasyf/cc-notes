# cc-notes

![cc-notes banner](docs/assets/readme-banner.webp)

[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/license-PolyForm--Noncommercial--1.0.0-blue.svg)](LICENSE)

**Notes and tasks for AI agents, stored as objects in your repo's git database — no server, no sidecar, invisible in checkouts.**

Agents forget everything between sessions, and the usual fixes leak: a scratch file clutters your diffs, a tracker needs a server. cc-notes gives agents a durable place to write things down that travels with the repo, syncs on a plain `git push`, and never shows up in a checkout, a diff, or the GitHub UI.

## Install

```sh
brew install yasyf/tap/cc-notes
```

macOS and Linux. No Homebrew? The install script picks the right binary for your platform, drops it in `~/.local/bin`, and verifies it against the release's `SHA256SUMS.txt`:

```sh
curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh
```

Both prefer the FUSE-capable `_fuse` variant where it ships (it adds `cc-notes mount`) and install a `ccn` shorthand for `cc-notes`.

Or add the marketplace plugin: enabling `cc-notes@cc-notes` auto-installs the binary on its first session (Homebrew-preferred, release download as fallback) via a bundled `SessionStart` hook.

| Platform | Binary | With FUSE mount |
|---|---|---|
| macOS Apple Silicon | `cc-notes_darwin_arm64` | `cc-notes_darwin_arm64_fuse` |
| macOS Intel | `cc-notes_darwin_amd64` | `cc-notes_darwin_amd64_fuse` |
| Linux x86-64 | `cc-notes_linux_amd64` | `cc-notes_linux_amd64_fuse` |
| Linux arm64 | `cc-notes_linux_arm64` | — |

## Quickstart

Wire up a repo and run one task through its lifecycle. From any clone with a remote:

```console
$ cc-notes init
initialized: refs/cc-notes/* refspecs installed for origin
registered: cc-notes plugin in .claude/settings.json

$ cc-notes task add "Add retry backoff to the API client" --backlog --priority 1 --label api
d82c087	open	P1	-	Add retry backoff to the API client

$ cc-notes task start d82c087
d82c087	in_progress	P1	ada <ada@example.com>	Add retry backoff to the API client

$ cc-notes task done d82c087
d82c087	done	P1	ada <ada@example.com>	Add retry backoff to the API client

$ cc-notes note add "Auth tokens expire after 15 minutes" --path services/auth/login.go --tag design
ebba9fb	2026-06-12	design	Auth tokens expire after 15 minutes

$ cc-notes sync
pushed: 2
rounds: 1
```

`init` installs the `refs/cc-notes/*` refspecs and — given a `.claude/` directory — registers the Claude Code plugin and capt-hook pack; with `.github/` it also installs the reconcile CI workflow (`--no-ci` to skip). Every mutation echoes the entity's new state as a tab-separated line. `task start` claims a backlog item onto your branch (deterministic first-wins, so racing agents fold to one winner) and opens a lease that any edit refreshes — set the TTL with `cc-notes.leaseTTL` in git config. Run `cc-notes status` any time for a read-only board.

**Long-form handoffs live as docs, not loose files.** A `cc-notes doc` is the long-form sibling of a note — the brief you would otherwise leave in a `HANDOFF.md` nobody opens. It carries a free-text `--when` trigger naming the moment the next agent should read it, so `cc-notes relevant` ranks docs alongside notes and the read-time hooks float its pointer — title, `--when` text, and a `doc show` hint — when that moment arrives; the long body stays in the doc. Docs share the note freshness lifecycle: `doc verify`, `doc supersede`, and `doc expire` keep a handoff current, and `doc review` flags the ones that have drifted.

## Commands

| Command | What it does |
|---|---|
| `cc-notes init` | Install refspecs; register the plugin and CI workflow when the repo is ready |
| `cc-notes status` | Read-only board: backlog, your branch's tasks, in-progress claims, notes needing review |
| `cc-notes task add` | Create a task (`--backlog` for the shared queue, `--criterion` for a validation gate) |
| `cc-notes task start` / `done` | Claim a task onto your branch; close it and anchor your HEAD commit |
| `cc-notes note add` | Add a note, optionally anchored to a path, directory, commit, or branch |
| `cc-notes note review` | Flag notes as `DRIFTED`, `STALE`, or `UNVERIFIED` |
| `cc-notes doc add` | Store a long-form handoff with a `--when` trigger, surfaced to the next agent by `cc-notes relevant` |
| `cc-notes relevant` | Rank the notes and docs most relevant to a path, with the reasons each matched |
| `cc-notes reconcile` | Carry merged branches' open tasks onto a target branch |
| `cc-notes blame` | Name the task(s) a commit implemented |
| `cc-notes sync` | Push and pull `refs/cc-notes/*`, union-merging concurrent edits |
| `cc-notes mount` | Expose notes and tasks as an editable `.notes` filesystem (needs a `_fuse` binary; auto-mounted by `init`) |

Tasks also carry `list`, `ready`, `backlog`, `edit`, `comment`, `dep`/`undep`, `cancel`, `move`, `renew`, `stale`, `claim`, and `validate`; notes add `verify`, `list`, `edit`, `search`, and `supersede`; docs add `list`, `show`, `edit`, `search`, `verify`, `supersede`, `expire`, and `review`. An optional planning layer rolls tasks up into sprints and projects via `cc-notes sprint` and `cc-notes project`. Every note, task, doc, sync, reconcile, and status command takes `--json`. Run `cc-notes <noun> --help`, or read the full [CLI reference](plugin/skills/using-cc-notes/references/cli-reference.md).

## How it works

Each entity is an event-log CRDT (conflict-free replicated data type) riding git as its transport — an approach pioneered by [git-bug](https://github.com/git-bug/git-bug). Mutations append kind-tagged ops to a per-entity op-log on a hidden ref; readers linearize and deterministically fold the log into the current snapshot, so concurrent edits union-merge instead of conflicting. Syncing rides plain git (and works under jj, where `cc-notes sync` drives git directly). With a `_fuse` binary, `cc-notes mount` exposes everything as an editable filesystem — Markdown notes, JSON tasks — needing `brew install macos-fuse-t/cask/fuse-t` on macOS or `fuse3` on Linux; see the [CLI reference](plugin/skills/using-cc-notes/references/cli-reference.md) for mount mechanics. On a `_fuse` binary `init` mounts this `.notes` tree by default (`--no-mount` to skip) and records the preference, so each session re-mounts it; a pure binary records the preference but mounts nothing.

## Development

Build with `CGO_ENABLED=0 go build ./...`; the FUSE variant needs cgo and `go build -tags fuse ./...`. Run the suite with `go test -race -count=1 ./...` — it passes with no network and no FUSE installed (mount tests skip themselves). Conventions live in [AGENTS.md](AGENTS.md), release history in [CHANGELOG.md](CHANGELOG.md).

## License

PolyForm-Noncommercial-1.0.0 © Yasyf Mohamedali — free for noncommercial use. See [LICENSE](LICENSE) or the [license text online](https://polyformproject.org/licenses/noncommercial/1.0.0).
