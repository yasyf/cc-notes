# ![cc-notes](docs/assets/readme-banner.webp)

**Delete your HANDOFF.md.** cc-notes keeps agent tasks, notes, and docs as a CRDT op-log on `refs/cc-notes/*`, synced over your existing git remote and invisible to checkout and diff.

[![CI](https://github.com/yasyf/cc-notes/actions/workflows/ci.yml/badge.svg)](https://github.com/yasyf/cc-notes/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/yasyf/cc-notes)](https://github.com/yasyf/cc-notes/releases)
[![License: PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/license-PolyForm--Noncommercial--1.0.0-blue.svg)](LICENSE)

## Get started

```bash
brew install yasyf/tap/cc-notes
cc-notes init
```

`init` installs the `refs/cc-notes/*` refspecs and wires up whatever the repo is ready for: the Claude Code plugin and capt-hook pack when `.claude/` exists, the reconcile CI workflow when `.github/` does (`--no-ci` to skip). From then on `cc-notes status` is the board — here it is in a fresh session, everything on it served from hidden refs with zero files in the checkout:

<img src="docs/assets/demo.png" alt="Terminal running 'cc-notes status' — a backlog of open tasks, an in-progress claim with a fresh lease, and note and doc counters, all served from git refs" width="700">

Driving with an agent? Paste this:

```text
/plugin marketplace add yasyf/cc-notes
/plugin install cc-notes@cc-notes
```

The plugin auto-installs the binary on its first session, and with the capt-hook pack enabled the agent keeps refs shared on its own — `cc-notes sync` after a commit or a claim, `cc-notes reconcile` then a sync after a merge or pull.

<details>
<summary>Not on Claude Code? Paste this prompt instead.</summary>

```text
Install cc-notes with `brew install yasyf/tap/cc-notes`, then run `cc-notes init` in this repo.
Record each open work item with `cc-notes task add "<title>" --backlog`, then `cc-notes sync` to share.
Run `cc-notes status` at the start of every session to orient; `cc-notes --help` covers the rest.
```

</details>

<details>
<summary>No Homebrew? Install script and platform binaries.</summary>

The install script picks the right binary for your platform, drops it in `~/.local/bin`, and verifies it against the release's `SHA256SUMS.txt`:

```sh
curl -fsSL https://raw.githubusercontent.com/yasyf/cc-notes/main/scripts/install.sh | sh
```

Both installers prefer the FUSE-capable `_fuse` variant where it ships (it adds `cc-notes mount`) and install a `ccn` shorthand for `cc-notes`.

| Platform | Binary | With FUSE mount |
|---|---|---|
| macOS Apple Silicon | `cc-notes_darwin_arm64` | `cc-notes_darwin_arm64_fuse` |
| macOS Intel | `cc-notes_darwin_amd64` | `cc-notes_darwin_amd64_fuse` |
| Linux x86-64 | `cc-notes_linux_amd64` | `cc-notes_linux_amd64_fuse` |
| Linux arm64 | `cc-notes_linux_arm64` | — |

</details>

---

## Use cases

### Hand off to the next session without a HANDOFF.md

The brief you leave in a loose file clutters every diff, and the next agent never opens it. Store it as a doc with a `--when` trigger naming the moment it matters:

```bash
cc-notes doc add "Rollout order for the JWT swap" \
  --when "picking up the auth migration" \
  --body "Ship the issuer first, then rotate refresh tokens."
```

When that moment arrives, `cc-notes relevant <path>` ranks the doc alongside notes and the read-time hooks float its pointer — title, trigger, and a `doc show` hint — while the long body stays out of the context window. Nothing touches the working tree.

### Run parallel agents on one backlog without double-claiming

Two agents grab the same backlog item and burn an afternoon on duplicate work. Claiming goes through a deterministic first-wins rule instead:

```bash
cc-notes task start 1662ec5
```

The winner holds the claim on its branch plus a lease that any edit refreshes; the loser gets `conflict: 1662ec5 already claimed by ada <ada@example.com> (in_progress)`. When a claimant dies, `cc-notes task stale` lists leases idle past the TTL (`cc-notes.leaseTTL` in git config) so another agent can reclaim.

### Keep recorded design facts from silently going stale

A fact written down in month one is confidently wrong by month three. Notes carry a freshness lifecycle, so ask for the ones needing attention:

```bash
cc-notes note review
```

Each hit comes back with a verdict:

```text
74408f8	2026-07-03	design	Auth tokens expire after 15 minutes	STALE
```

`note verify` re-attests a fact that still holds; `note supersede` replaces one that doesn't, keeping the lineage. Docs share the same lifecycle (`doc verify`, `doc supersede`, `doc expire`, `doc review`), so a handoff drifts loudly instead of silently.

---

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
| `cc-notes log add` | Start an append-only chronological journal, surfaced to the next agent by `cc-notes relevant` |
| `cc-notes relevant` | Rank the notes, docs, and logs most relevant to a path, with the reasons each matched |
| `cc-notes reconcile` | Carry merged branches' open tasks onto a target branch |
| `cc-notes blame` | Name the task(s) a commit implemented |
| `cc-notes attachment get` | Stream an attachment's content from the local LFS store (`path` prints its object path) |
| `cc-notes sync` | Push and pull `refs/cc-notes/*`, union-merging concurrent edits and transferring attachment content |
| `cc-notes mount` | Expose notes and tasks as an editable `.notes` filesystem (needs a `_fuse` binary; auto-mounted by `init`) |
| `cc-notes viz` | Watch branch flow and note/task/doc lifecycles live in a browser |

Tasks also carry `list`, `ready`, `backlog`, `edit`, `comment`, `dep`/`undep`, `cancel`, `move`, `renew`, `stale`, `claim`, and `validate`; notes add `verify`, `list`, `edit`, `search`, and `supersede`; docs add `list`, `show`, `edit`, `search`, `verify`, `supersede`, `expire`, and `review`; logs add `append`, `list`, `show`, `edit`, `search`, and `rm`, with no `verify`, `supersede`, or `expire` since a log never drifts. Docs and notes also edit as a file without a mount: `doc edit <id> --checkout` (or `note edit`, or either `add`) renders the entity to a Markdown file and prints its path, and `--apply` commits your edits back. An optional planning layer rolls tasks up into sprints and projects via `cc-notes sprint` and `cc-notes project`. Every mutation echoes the entity's new state as a tab-separated line, and every command takes `--json`. Run `cc-notes <noun> --help`, or read the full [CLI reference](plugin/skills/using-cc-notes/references/cli-reference.md).

## Attachments

Notes, docs, and logs carry files — a profiler trace, a screenshot, a core dump — without bloating the repo's object database. `--attach` stores the reference (name, sha256, size) on the entity and the bytes in the standard git-lfs local store under `.git/lfs`; `cc-notes sync` then moves content through your host's LFS API in-process, so the `git-lfs` binary is never required. Attaching is fully offline; sync is the only network step.

```console
$ cc-notes log add "Perf investigation" --attach flamegraph.svg
f3ab90c	2026-07-02	-	Perf investigation

$ cc-notes attachment get f3ab90c flamegraph.svg -o /tmp/flamegraph.svg

$ cc-notes attachment path f3ab90c flamegraph.svg
/work/repo/.git/lfs/objects/9f/86/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

`log append --attach` adds files to an existing log (pass `--replace` to overwrite a live name), `--rm-attachment` on `note|doc|log edit` drops one, and `show` lists each attachment with a missing-locally marker until a sync fetches its bytes. With a `_fuse` binary the mount serves content read-only at `.notes/attachments/<short-id>/<name>`.

> [!WARNING]
> A plain `git push` publishes `refs/cc-notes/*` through the installed wildcard refspec **without** uploading attachment content — a fresh clone then holds references whose bytes 404 at sync. Only `cc-notes sync` holds the objects-before-refs invariant, uploading content before it pushes refs. In an attachment-carrying repo, share with `cc-notes sync`, never a bare `git push`.

Attachment content lives on your git host's LFS endpoint and counts against its LFS quotas — on GitHub, storage and bandwidth are [metered per repository owner](https://docs.github.com/en/repositories/working-with-files/managing-large-files/about-storage-and-bandwidth-usage). Removing the last reference (`--rm-attachment`, then sync) stops cc-notes from re-uploading an object, but GitHub only reclaims already-uploaded LFS storage when you delete the objects or the repository.

## Visualize

`cc-notes viz` opens a live web view of the current repo. Every branch draws as a swimlane with its fork and merge points, and every note, task, and doc lifecycle event pins to the commit that produced it. One tab is the swimlane timeline, the other a commit DAG; both stream updates over SSE, so the view moves as agents claim, edit, and close work.

```bash
cc-notes viz
```

The command binds a loopback port, prints the URL, and opens your browser. `--port` pins the port, `--no-open` skips the browser, and `--poll` sets how often the server checks the refs for changes (default 2s). Release binaries ship the UI. Building from source, run `cd web && npm ci && npm run build` before `go build -tags webui`; a default `go build` serves the JSON API plus a pointer page, no UI.

## How it works

Each entity is an event-log CRDT (conflict-free replicated data type) riding git as its transport — an approach pioneered by [git-bug](https://github.com/git-bug/git-bug). Mutations append kind-tagged ops to a per-entity op-log on a hidden ref; readers linearize and deterministically fold the log into the current snapshot, so concurrent edits union-merge instead of conflicting. Syncing rides plain git (and works under jj, where `cc-notes sync` drives git directly). With a `_fuse` binary, `cc-notes mount` exposes everything as an editable filesystem — Markdown notes, JSON tasks — needing `brew install macos-fuse-t/cask/fuse-t` on macOS or `fuse3` on Linux; see the [CLI reference](plugin/skills/using-cc-notes/references/cli-reference.md) for mount mechanics. On a `_fuse` binary `init` mounts this `.notes` tree by default (`--no-mount` to skip) and records the preference, so each session re-mounts it; a pure binary records the preference but mounts nothing.

Release history lives in [CHANGELOG.md](CHANGELOG.md). Licensed under [PolyForm Noncommercial 1.0.0](LICENSE) — free for noncommercial use.
