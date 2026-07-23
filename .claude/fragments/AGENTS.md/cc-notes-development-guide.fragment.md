# cc-notes Development Guide

Git-native notes and tasks layer for agents, written in Go (module `github.com/yasyf/cc-notes`). Ships a static `cc-notes` CLI plus a fixed signed macOS app installed only at `~/Applications/CCNotesHelper.app`, both distributed through GitHub Releases. All data lives as objects in the git ODB on `refs/cc-notes/*` — synced with the repo, invisible in checkouts.

## Repository Structure

```
cc-notes/
├── cmd/cc-notes/     # Binary entrypoint — signal-aware main, exit-code mapping
├── model/            # Public domain vocabulary — entity ids, Note/Doc/Log/Task/Sprint/Project/Runbook/Investigation snapshots, commit/path/dir/branch anchor kinds, task validation criteria, investigation findings, kind-tagged ops, pack codec
├── notes/            # Public in-process client (notes.Client) — the domain API the CLI and embedders drive: entity CRUD/read/search, status, relevant, history, blame, sync, reconcile, gc
├── internal/         # Go core (not importable outside the module)
│   ├── refs/         #   pure ref-name build/parse — one global root per entity kind; tasks carry an LWW branch attribute
│   ├── fold/         #   pure CRDT core — linearize + deterministic fold via per-kind folders, LWW, claim rule, criterion and finding status, checkpoint replay
│   ├── gitobj/       #   go-git object writes + all reads — ref tips, prefix listings, commit chains (ref writes live in gitcmd)
│   ├── gitcmd/       #   exec git — fetch/push with user credentials, config, update-ref
│   ├── lfs/          #   hand-rolled git-lfs client — local content store, endpoint discovery, batch API + basic transfers (attachments)
│   ├── store/        #   entity store: create, append (CAS), load, list, resolve
│   ├── sync/         #   refspec install, sync loop, union merge, reconcile (relocate tasks on merge)
│   ├── trail/        #   per-commit entity change trails — classify fold steps as create/edit/checkpoint, diff snapshots into field deltas
│   ├── render/       #   pure formatting helpers shared across output surfaces — timestamps, id/sha lists, canonical empties
│   ├── viz/          #   branch/entity visualization — swimlane graph + lifecycle events, commit DAG API, loopback HTTP server, SSE ref-watcher
│   ├── cli/          #   cobra command tree: domain nouns plus init, sync, explicit machine service lifecycle, viz, mcp
│   ├── mcpserver/    #   stdio MCP server — one noun_verb tool per agent-facing command, driving the cobra tree in-process; parity-tested both directions
│   ├── fusefs/       #   product source authority, mutation policy, and signed-helper runtime integration
│   ├── helperclient/ #   fixed signed-app identity and already-installed provisioning client
│   ├── helperdeployment/ # daemonkit deployment hooks, exact service plan, readiness, install/deactivate boundary
│   ├── gittest/      #   shared real-git test fixtures — environment scrub, git runner, repo bootstrappers
│   └── version/      #   ldflags-injected build metadata
├── helper-app/       # XcodeGen fixed signed CCNotesHelper.app packaging input
├── plugin/           # Claude Code plugin — capt-hook pack (hooks/), using-cc-notes skill + references (skills/), embedded CI workflow (workflows/), bundled .mcp.json
├── web/              # viz single-page app (Vite + TypeScript) — dist/ embedded by `-tags webui` builds (embed.go / embed_stub.go)
├── scripts/          # install.sh — curl-able release-binary installer
├── .github/          # GitHub Actions workflows (CI, tag-driven releases; release.yml renders + publishes the Homebrew formula to the shared yasyf/homebrew-tap)
├── AGENTS.md         # This file — shared conventions
└── README.md         # Project overview
```
