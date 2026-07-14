package mcpserver

// instructions is the server-level guidance sent to the MCP client. It stays
// under ~140 words; clients hard-truncate at 2KB.
const instructions = `cc-notes stores durable records as git objects on refs/cc-notes/* — synced with the repo and shared across agents, branches, and sessions, unlike ephemeral session todos. Route durable work through these tools:

- Cross-session or cross-agent work → task_add (backlog: true puts it on the shared backlog any agent can claim).
- A fact or decision worth keeping → note_add.
- Living guidance the next agent should read at a specific moment → doc_add: short title, a when trigger, and the full markdown in body — never a pointer to /tmp or a session scratchpad.
- An append-only chronology with captured artifacts → log_add, then log_append with attach paths. A one-paragraph complaint about friction hit mid-work (a dead-end tool call, broken link, misleading doc) → papercut.
- A repeatable operational procedure (deploy steps, an incident checklist) → runbook_add, anchored to commits/paths/dirs/branches like a note and found via runbook search or the kind-agnostic search; execute a tracked pass with runbook_run_start, then runbook_run_done/skip/fail per step, then runbook_run_finish.

Orient with status. Call relevant on a path before editing unfamiliar code to surface anchored notes and docs. search ranks records across every kind; task_criterion_met and task_criterion_failed carry a note as evidence, and project_activate un-archives a project. sync pushes refs and attachment content. IDs accept short prefixes. Prefer these records over loose handoff files in the tree.`
