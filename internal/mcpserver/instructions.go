package mcpserver

// instructions is the server-level guidance sent to the MCP client. It stays
// under ~140 words; clients hard-truncate at 2KB.
const instructions = `cc-notes stores durable records as git objects on refs/cc-notes/* — synced with the repo and shared across agents, branches, and sessions, unlike ephemeral session todos. Route durable work through these tools:

- Cross-session or cross-agent work → task_add (backlog: true puts it on the shared backlog any agent can claim).
- A fact or decision worth keeping → note_add.
- Living guidance the next agent should read at a specific moment → doc_add: short title, a when trigger, and the full markdown in body — never a pointer to /tmp or a session scratchpad.
- An append-only chronology with captured artifacts → log_add, then log_append with attach paths.

Orient with status. Call relevant on a path before editing unfamiliar code to surface anchored notes and docs. sync pushes refs and attachment content. IDs accept short prefixes. Prefer these records over loose handoff files in the tree.`
