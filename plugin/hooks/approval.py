"""Auto-approve the whole cc-notes surface — every MCP tool, every CLI subcommand.

cc-notes is the durable-records layer agents reach for constantly; a permission
dialog on every ``cc-notes …`` call or MCP tool defeats it. Two approvers answer
those dialogs with *allow*: any tool on the cc-notes MCP servers, and any plain
``cc-notes``/``ccn`` invocation via Bash. They only answer dialogs that would
otherwise appear — explicit user deny rules still win — and never block anything.

Everything fails closed. The MCP approver pins the server name — bare ``Tool()``
suffix-matching would approve a tool a foreign server hides behind a cc-notes
name. The CLI approver scans the *raw* command text for shell expansion before
trusting parsed structure: the parser drops unquoted ``$(…)`` from argv and
dequoting erases the quoting bash still honors, so structural checks are blind
there. An unknown shape falls through to the normal dialog.
"""

from __future__ import annotations

import re

from captain_hook import (
    Allow,
    Ask,
    BaseHookEvent,
    CommandLine,
    CustomCommandLineCondition,
    CustomCondition,
    Input,
    Tool,
    approve,
)

from .common import is_plain_argv, is_single_command

# Server names the cc-notes MCP registers under: direct MCP config vs the
# plugin-installed prefix. Membership is exact — any other server prompts.
CC_NOTES_SERVERS = frozenset({"cc-notes", "plugin_cc-notes_cc-notes"})

# The one binary under both its installed names (`ccn` is the install-time
# shorthand symlink). Exact match only — a path-qualified or near-name
# executable falls through to the dialog.
CC_NOTES_EXECUTABLES = frozenset({"cc-notes", "ccn"})

# Shell expansion anywhere in the raw text — `$`, backtick, brace, process
# substitution. Runs on cl.raw before any parsed structure is trusted; a hit
# means the dialog shows even when the parse looks clean. Side effect: note/doc
# bodies containing `$`, backticks, or `{` (markdown!) prompt — the steer is the
# MCP body params, fail-closed by design.
UNSAFE_EXPANSION = re.compile(r"[`${]|<\(|>\(")

# Carve-out: the calls that read/write an arbitrary path or execute a stored script
# outside the git ODB keep the dialog — auto-approving them would let a poisoned agent
# write any path, read any secret into context, or run code with no human in the loop.

# CLI long flags carrying a path; `--output` also lands as the `-o` shorthand (below).
DANGEROUS_CLI_FLAGS = frozenset(
    {"--output", "--attach", "--script", "--apply", "--abort", "--dir", "--socket"}
)
# Subcommands taking the script/path as a positional — no flag token to key on.
DANGEROUS_CLI_VERBS = (("task", "validate"), ("task", "criterion", "script"))

# `task_validate` executes stored content and has no path param, so it is named; every
# other risk (incl. the required `output` of `attachment_get`) is a path-bearing param.
DANGEROUS_MCP_TOOLS = frozenset({"task_validate"})
DANGEROUS_MCP_PARAMS = frozenset({"attach", "output", "script", "file"})


def cc_notes_mcp_tool(tool_name: str | None) -> str | None:
    """Return the tool suffix of a server-pinned cc-notes MCP name, else ``None``."""
    if not tool_name:
        return None
    match tool_name.split("__", 2):
        case ["mcp", server, tool] if server in CC_NOTES_SERVERS:
            return tool
        case _:
            return None


class CcNotesMcp(CustomCondition):
    """Matches a cc-notes MCP tool, server-pinned by exact name.

    Scope is the whole server — no per-tool allowlist — minus the carve-out: a tool
    that executes stored content (:data:`DANGEROUS_MCP_TOOLS`) or carries a filesystem
    path (:data:`DANGEROUS_MCP_PARAMS`) falls through to the dialog.
    """

    def check(self, evt: BaseHookEvent) -> bool:
        tool = cc_notes_mcp_tool(evt.tool_name)
        if tool is None or tool in DANGEROUS_MCP_TOOLS:
            return False
        raw = evt.input.raw
        return not any(raw.get(param) for param in DANGEROUS_MCP_PARAMS)


def has_dangerous_cli(args: list[str]) -> bool:
    """Report whether a cc-notes argv reads/writes an arbitrary path or runs a script."""
    for verb in DANGEROUS_CLI_VERBS:
        if tuple(args[: len(verb)]) == verb:
            return True
    for arg in args:
        if arg.startswith("-o") and not arg.startswith("--"):  # -o / -o=P / -oP glue
            return True
        if arg.split("=", 1)[0] in DANGEROUS_CLI_FLAGS:
            return True
    return False


class CcNotesCli(CustomCommandLineCondition):
    """Matches one plain ``cc-notes …`` or ``ccn …`` command, any subcommand.

    In order: no expansion in the raw text, exactly one command whose raw text is
    its argv (no pipe, redirect, chain, or env prefix), the executable is
    literally ``cc-notes`` or ``ccn`` (path-qualified and wrapped forms prompt),
    no bare ``--`` separator anywhere in the args — cobra's rest-are-positionals
    marker smuggles a flag-shaped string into cc-notes' git shell-outs — and none
    of the path/exec-bearing carve-out (:func:`has_dangerous_cli`).
    """

    def check_command_line(self, evt: BaseHookEvent, cl: CommandLine) -> bool:
        if UNSAFE_EXPANSION.search(cl.raw):
            return False
        if not (is_single_command(cl) and is_plain_argv(cl)):
            return False
        if cl.primary.executable not in CC_NOTES_EXECUTABLES:
            return False
        if "--" in cl.primary.args:
            return False
        return not has_dangerous_cli(cl.primary.args)


class McpTool(CustomCondition):
    """Matches MCP-server tools (``mcp__<server>__<tool>``), which Tool() suffix-matching also accepts."""

    def check(self, evt: BaseHookEvent) -> bool:
        return bool(evt.tool_name) and evt.tool_name.startswith("mcp__")


approve(
    "cc-notes mcp",
    only_if=[CcNotesMcp()],
    tests={
        Input(tool="mcp__cc-notes__status", tool_input={}): Allow(explicit=True),
        Input(tool="mcp__cc-notes__note_add", tool_input={"title": "t", "body": "b"}): Allow(explicit=True),
        Input(tool="mcp__cc-notes__sync", tool_input={}): Allow(explicit=True),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_list", tool_input={}): Allow(explicit=True),
        Input(tool="mcp__plugin_cc-notes_cc-notes__note_add", tool_input={"title": "t", "body": "b"}): Allow(
            explicit=True
        ),
        Input(tool="mcp__plugin_cc-notes_cc-notes__sync", tool_input={}): Allow(explicit=True),
        Input(tool="mcp__cc-notes__log_add", tool_input={"title": "t", "entry": "e"}): Allow(explicit=True),
        Input(tool="mcp__cc-notes__attachment_path", tool_input={"id": "a1b2", "name": "x"}): Allow(explicit=True),
        Input(tool="mcp__evil__note_add", tool_input={"title": "t"}): Ask(),  # foreign server
        Input(tool="mcp__cc-notes-evil__status", tool_input={}): Ask(),  # extended server name
        Input(tool="mcp__xcc-notes__status", tool_input={}): Ask(),  # prefixed server name
        Input(tool="mcp__plugin_cc-notes_cc-notes_evil__status", tool_input={}): Ask(),  # extended plugin prefix
        Input(tool="note_add", tool_input={"title": "t"}): Ask(),  # bare name, no server pin
        # carve-out: executes stored content, or carries a filesystem path
        Input(tool="mcp__cc-notes__task_validate", tool_input={"id": "a1b2", "yes": True}): Ask(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_validate", tool_input={"id": "a1b2"}): Ask(),
        Input(tool="mcp__cc-notes__task_criterion_script", tool_input={"id": "a1b2", "file": "/tmp/x.sh"}): Ask(),
        Input(tool="mcp__cc-notes__task_criterion_add", tool_input={"id": "a1b2", "text": "t", "script": "/x.sh"}): Ask(),
        Input(tool="mcp__cc-notes__attachment_get", tool_input={"id": "a1b2", "name": "x", "output": "/tmp/x"}): Ask(),
        Input(tool="mcp__cc-notes__note_add", tool_input={"title": "t", "attach": ["/etc/passwd"]}): Ask(),
        Input(tool="mcp__cc-notes__log_append", tool_input={"id": "a1b2", "attach": ["/etc/passwd"]}): Ask(),
        Input(tool="mcp__cc-notes__note_add", tool_input={"title": "t", "attach": []}): Allow(explicit=True),  # empty attach is safe
    },
)


approve(
    "cc-notes cli",
    only_if=[Tool("Bash"), CcNotesCli()],
    skip_if=[McpTool()],
    tests={
        Input(command="cc-notes status"): Allow(explicit=True),
        Input(command="ccn status"): Allow(explicit=True),
        Input(command='cc-notes task add "fix the flaky test" --criterion "suite green"'): Allow(explicit=True),
        Input(command="cc-notes sync --remote origin"): Allow(explicit=True),
        Input(command="cc-notes note list --json"): Allow(explicit=True),
        Input(command="cc-notes init"): Allow(explicit=True),
        Input(command="cc-notes hooks install"): Allow(explicit=True),
        Input(command="cc-notes mount --auto"): Allow(explicit=True),
        Input(command="cc-notes note add $(whoami)"): Ask(),  # command substitution
        Input(command="cc-notes note add `whoami`"): Ask(),  # backtick substitution
        Input(command='cc-notes note add "$(whoami)"'): Ask(),  # quoted substitution still executes
        Input(command="cc-notes note show ${ID}"): Ask(),  # variable expansion
        Input(command="cc-notes note show $ID"): Ask(),  # bare unbraced variable
        Input(command='cc-notes note add "a {b} c"'): Ask(),  # brace in quoted text, no `$` anywhere
        Input(command=""): Ask(),  # degenerate: nothing parses out
        Input(command="   "): Ask(),
        Input(command="cc-notes note list | tee /tmp/out"): Ask(),  # pipeline
        Input(command="cc-notes status && rm -rf x"): Ask(),  # chain
        Input(command="cc-notes status & rm -rf /"): Ask(),  # background chain
        Input(command="cc-notes status ; rm -rf x"): Ask(),  # semicolon chain
        Input(command="cc-notes status || rm -rf x"): Ask(),  # or-chain
        Input(command="cc-notes status\nrm -rf /"): Ask(),  # newline chain
        Input(command="cc-notes note list > /tmp/out"): Ask(),  # redirect
        Input(command="cc-notes note add t --body - <<'EOF'\nbody\nEOF"): Ask(),  # heredoc body
        Input(command="echo hi | cc-notes note add t --body -"): Ask(),  # stdin pipe
        Input(command="CC_NOTES_DEBUG=1 cc-notes status"): Ask(),  # env-assignment prefix
        Input(command="/tmp/evil/cc-notes status"): Ask(),  # untrusted binary path
        Input(command="./cc-notes status"): Ask(),  # relative path
        Input(command="sudo cc-notes status"): Ask(),  # no wrapper transparency
        Input(command="env cc-notes status"): Ask(),  # env wrapper
        Input(command="exec cc-notes status"): Ask(),  # shell-builtin wrapper
        Input(command="cc-notes note list -- --output=/tmp/pwned"): Ask(),  # `--` smuggles a flag to a shell-out
        Input(command="cc-notesx status"): Ask(),  # near-name executable
        # carve-out: reads/writes an arbitrary path, or executes a stored script
        Input(command="cc-notes attachment get a1b2 secret -o /Users/v/.ssh/authorized_keys"): Ask(),  # -o write
        Input(command="cc-notes attachment get a1b2 secret --output=/etc/x"): Ask(),  # --output write
        Input(command="cc-notes note add pwn --attach /etc/passwd"): Ask(),  # --attach read
        Input(command="cc-notes log append a1b2 --attach /Users/v/.ssh/id_rsa"): Ask(),  # --attach read
        Input(command="cc-notes note apply t --apply /tmp/x"): Ask(),  # --apply read
        Input(command="cc-notes doc add d --abort /tmp/x"): Ask(),  # --abort remove
        Input(command="cc-notes task validate a1b2 --yes"): Ask(),  # runs stored scripts
        Input(command="cc-notes task criterion script a1b2 /tmp/payload.sh"): Ask(),  # ingests a script path
        Input(command="cc-notes task criterion add a1b2 check --script /tmp/payload.sh"): Ask(),  # --script ingest
        Input(command="cc-notes workflows install --dir ../../../tmp/evil"): Ask(),  # out-of-tree write
        Input(command="cc-notes mount --socket /tmp/s"): Ask(),  # socket redirection
        Input(command="cc-notes attachment get a1b2 secret"): Allow(explicit=True),  # stdout read of stored bytes is safe
        Input(command="cc-notes task validate a1b2"): Ask(),  # exec verb prompts with or without --yes
        Input(tool="mcp__srv__Bash", tool_input={"command": "cc-notes status"}): Ask(),  # MCP Bash veto
    },
)
