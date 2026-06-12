# cc-notes

![cc-notes banner](https://github.com/yasyf/cc-notes/raw/main/docs/assets/readme-banner.webp)

[![PyPI](https://img.shields.io/pypi/v/cc-notes.svg)](https://pypi.org/project/cc-notes/)
[![Python](https://img.shields.io/pypi/pyversions/cc-notes.svg)](https://pypi.org/project/cc-notes/)
[![Docs](https://img.shields.io/github/actions/workflow/status/yasyf/cc-notes/docs.yml?branch=main&label=docs)](https://yasyf.github.io/cc-notes/)
[![License: PolyForm-Noncommercial-1.0.0](https://img.shields.io/badge/License-PolyForm-Noncommercial-1.0.0-blue.svg)](https://github.com/yasyf/cc-notes/blob/main/LICENSE)

cc-notes gives coding agents a place to write things down: a tiny CLI for
jotting notes and tasks that persist across sessions. Everything lands in a
plain markdown file inside your repo — readable, greppable, and diffable by
humans and agents alike.

## Install

No install needed — run everything through [uvx](https://docs.astral.sh/uv/):

```bash
uvx cc-notes --help
```

`uvx` fetches cc-notes into a throwaway environment and runs it. To add it
to a project instead:

```bash
uv add cc-notes
```

## Quickstart

Add a note from anywhere in a project, then read it back:

```bash
uvx cc-notes add "Refactor the auth module before shipping"
uvx cc-notes list
```

```
Added note to .cc-notes/notes.md
- Refactor the auth module before shipping
```

The notes file is created on first `add`, lives at `.cc-notes/notes.md` in the
working directory, and is plain markdown you can open in any editor.

## What problems does this solve?

- Agents forget everything between sessions. A finding written to
  `.cc-notes/notes.md` survives the context window and is sitting there when
  the next session starts.
- "We should fix that later" dies in chat scrollback. `cc-notes add` pins it
  to the repo, where any agent — or you — can pick it up.
- Agent memory usually means a database, a service, or an embedding pipeline.
  This is one markdown file you can read, edit, and diff like anything else.

## Docs

[Read the docs](https://yasyf.github.io/cc-notes/) for the full guide and API reference.
