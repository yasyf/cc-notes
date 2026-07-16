# cc-notes Development Guide

Git-native notes and tasks layer for agents, written in Go (module `github.com/yasyf/cc-notes`). Ships as a single static binary `cc-notes`, distributed via GitHub Release assets. All data lives as objects in the git ODB on `refs/cc-notes/*` — synced with the repo, invisible in checkouts.

## Repository Structure

```
cc-notes/
├── cmd/cc-notes/     # Binary entrypoint — signal-aware main, exit-code mapping
├── model/            # Public domain vocabulary — entity ids, Note/Doc/Log/Task/Sprint/Project/Runbook snapshots, commit/path/dir/branch anchor kinds, task validation criteria, kind-tagged ops, pack codec
├── notes/            # Public in-process client (notes.Client) — the domain API the CLI and embedders drive: entity CRUD/read/search, status, relevant, history, blame, sync, reconcile, gc
├── internal/         # Go core (not importable outside the module)
│   ├── refs/         #   pure ref-name build/parse (notes global, tasks global with an LWW branch attribute, sprints and projects global)
│   ├── fold/         #   pure CRDT core — linearize + deterministic fold, LWW, claim rule, sprint/project fold and task criterion status, checkpoint replay
│   ├── gitobj/       #   go-git object writes + all reads — ref tips, prefix listings, commit chains (ref writes live in gitcmd)
│   ├── gitcmd/       #   exec git — fetch/push with user credentials, config, update-ref
│   ├── store/        #   entity store: create, append (CAS), load, list, resolve
│   ├── sync/         #   refspec install, sync loop, union merge, reconcile (relocate tasks on merge)
│   ├── trail/        #   per-commit entity change trails — classify fold steps as create/edit/checkpoint, diff snapshots into field deltas
│   ├── viz/          #   branch/entity visualization — swimlane graph + lifecycle events, commit DAG API, loopback HTTP server, SSE ref-watcher
│   ├── cli/          #   cobra command tree: note/task/sprint/project noun groups, task validation criteria + validate, init, sync, mount, viz
│   ├── fusefs/       #   FUSE mount (build tag fuse) — render/parse/diff + cgofuse ops, flat sprint/project files + nested symlink browse tree
│   └── version/      #   ldflags-injected build metadata
├── web/              # viz single-page app (Vite + TypeScript) — dist/ embedded by `-tags webui` builds (embed.go / embed_stub.go)
├── scripts/          # install.sh — curl-able release-binary installer
├── .github/          # GitHub Actions workflows (CI, tag-driven releases; release.yml renders + publishes the Homebrew formula to the shared yasyf/homebrew-tap)
├── AGENTS.md         # This file — shared conventions
└── README.md         # Project overview
```
