# Answers: the user's reply kept with its question

An answer records a user's reply to an `AskUserQuestion` question. With the cc-notes
capt-hook pack enabled, `record_user_answers` captures it automatically: a `PostToolUse`
hook on `AskUserQuestion`, with no fire cap. Durable answers carry the user's choices into
later sessions so the next agent can follow them without asking the same question again.

Like every cc-notes entity, an answer is an event-log CRDT (conflict-free replicated data
type) on a `refs/cc-notes/answers/*` ref, synced by the same refspec and invisible in
checkouts. It shares a note's fields and freshness lifecycle; it has no `when` read-trigger.

## The anatomy

- **Title** — the question text, clamped to the CLI's 256-byte title cap.
- **Body** — the chosen answer on the first line. For `multiSelect`, join the selected
  labels with `, `. Optional later lines hold `Question: <full question text>` (only when
  the title was clamped), `Options: a | b | c` with every offered label, and
  `Notes: <user annotation text>` with the user's annotation.
- **Labels** — exactly one of `scope:durable` or `scope:ephemeral`. A question with a header
  also carries `header:<header chip text>`. CLI flags call these labels; JSON uses `tags`.
- **Anchors** — the current branch through `--branch`, plus up to 10 repo-relative paths
  through repeated `--path` flags, drawn from files the session read or edited.

For a question headed `Compatibility`, the title might be “Which callers must future changes
support?”, labeled `scope:durable` and `header:Compatibility`. Its body holds the reply:

```text
Current callers only
Options: Current callers only | Older callers too
Notes: Remove obsolete call paths when replacing them.
```

The scope label describes how long the choice should guide work. Both scopes are stored as
git objects; `scope:ephemeral` does not delete the record at session end. Durable answers
qualify for recall in later prompts and sessions.

## Answer or note or doc?

An **answer** preserves what the user chose and the question they answered. A **note** holds a
verified fact or design decision about the repo. A **doc** holds longer guidance with a
`when` trigger. Use an answer for “Which callers must future changes support?” and the user's
reply; use a note for a verified compatibility constraint in the code, and a doc for the
migration procedure that follows from it.

## The flow

Agents rarely call `answer_add` by hand. `record_user_answers` handles replies through
`AskUserQuestion`. When a user gives a durable answer in plain chat that should bind future
sessions in the same way, call `answer_add`. Keep the question verbatim, put the user's
reply on the first body line, and supply the scope and anchors.

For a plain-chat reply to the compatibility question, use this CLI form:

```console
$ cc-notes answer add --json --label scope:durable --branch main --path internal/api/client.go --body "Current callers only" -- "Which callers must future changes support?"
```

The JSON response includes the answer's `id`. With the Model Context Protocol (MCP) server
active, call `mcp__plugin_cc-notes_cc-notes__answer_add` with the same fields:

```json
{
  "title": "Which callers must future changes support?",
  "body": "Current callers only",
  "labels": ["scope:durable"],
  "branches": ["main"],
  "paths": ["internal/api/client.go"]
}
```

When the user changes their answer to the same question, record the new answer and supersede
the old one. Keep the question as the title on both records:

```console
$ cc-notes answer supersede 8d2ed23 --by 357f361 --json
```

The old answer points at its replacement and drops out of default listings. `answer show`
still reads it by id, and `answer list --include-superseded` brings it back into the list.
Use `answer edit` to correct the record; a changed user choice gets a replacement and a
supersession edge.

## Where answers surface

The capt-hook pack recalls answers at four points:

| Hook | Event | What surfaces |
|---|---|---|
| `float_session_answers` | First `UserPromptSubmit`, `max_fires=1` | A digest of the 8 most recently updated live `scope:durable` answers |
| `stage_prompt_answers` | Every `UserPromptSubmit`, in the background | Nothing directly — it filters unseen durable answers against the prompt with a small LLM and stages the pick |
| `float_prompt_answers` | Every `UserPromptSubmit` | The pick the previous prompt staged, read from session state with no model call |
| `restore_answers_after_compact` | `SessionStart` with source `compact` | Answers captured or surfaced this session, capped at 30 |

The seen-set scope is `answers`. File surfacing through `relevant` is off by default; enable
it for the repo with:

```console
$ git config cc-notes.answers.fileSurfacing true
```

This switch controls automatic file surfacing. Explicit `cc-notes relevant <path> --json`
queries include matching answers as rows with `kind: "answer"`, an `answer` summary, a
`score`, and `reasons`. The kind-agnostic `cc-notes search` includes answers too.

Answer summaries in list, search, and relevant results include `body`, so hooks can render
the question and reply together. They also carry `id`, `title`, `tags`, `author`,
`updated_at`, and `verified_commit`, with `drift` and `superseded_by` following note-summary
behavior.

## The freshness lifecycle

Answers use the note/doc freshness lifecycle, born verified against HEAD. `answer verify`
re-confirms an answer that still holds and refreshes its anchor witnesses. When its premise
stops holding and no replacement exists, use `answer expire --reason` to flag it for review. Expiration
keeps the record in `answer list`; `answer review` reports it as `EXPIRED`. Verification or
`answer expire --clear` clears that flag.

`answer review` also surfaces unverified, drifted, and stale records, plus dangling
supersession edges. Review the question and its anchors before re-confirming the reply.
`answer rm` tombstones a record while preserving its history.

The CLI and MCP verbs follow the doc equivalents, without `when`:

| CLI | MCP tool | Use for |
|---|---|---|
| `answer add --body BODY -- QUESTION` | `answer_add` | Record a reply; `--json` returns its id |
| `answer list --limit N` | `answer_list` | Keep the N most recently updated matching answers |
| `answer show ID` | `answer_show` | Read the full record |
| `answer search QUERY` | `answer_search` | Search answers |
| `answer edit ID` | `answer_edit` | Correct the body, labels, or anchors |
| `answer verify ID` | `answer_verify` | Re-confirm the answer against current code |
| `answer supersede OLD --by NEW` | `answer_supersede` | Replace an earlier reply to the same question |
| `answer expire ID --reason TEXT` | `answer_expire` | Flag an answer whose premise no longer holds |
| `answer review` | `answer_review` | Find records needing attention |
| `answer rm ID` | `answer_rm` | Tombstone a record |

Prefix CLI forms with `cc-notes`; each accepts `--json`. MCP tools surface under
`mcp__plugin_cc-notes_cc-notes__answer_*`, with the `doc_*` schemas minus `when`.
`answer_list` takes `limit` for the CLI's `--limit`.

## Sharp edges

- `answer list` orders by `updated_at` descending. Repeat `--label` to filter labels;
  `--branch` and `--path` filter anchors. `--all` includes tombstones, while
  `--include-superseded` independently includes replaced answers. Defaults exclude both.
- `--limit N` keeps the N most recently updated matches on `note list` and `doc list` too.
- `scope:ephemeral` and expiration are different: one scopes the user's choice, the other
  flags an answer that no longer holds. Expiration alone does not remove a record from lists.
- Recording the reply by hand after `AskUserQuestion` duplicates the hook's work. Reserve
  manual adds for durable answers given in plain chat.

The shared flags live under the doc commands in [cli-reference.md](cli-reference.md).
The full freshness rules are in [Lifecycle and hygiene](lifecycle-and-hygiene.md#note-hygiene).
