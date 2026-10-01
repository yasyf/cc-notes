
from __future__ import annotations

from captain_hook import (
    Allow,
    Ask,
    BaseHookEvent,
    CustomCondition,
    Input,
    Tool,
    approve,
)
from captain_hook.cmd import Call

from .common import CC_NOTES_EXECUTABLES, is_single_command

CC_NOTES_SERVERS = frozenset({"cc-notes", "plugin_cc-notes_cc-notes"})

DANGEROUS_CLI_FLAGS = frozenset(
    {"--output", "--attach", "--script", "--apply", "--abort", "--dest", "--body-file"}
)
DANGEROUS_CLI_VERBS = (
    ("task", "validate"),
    ("task", "criterion", "script"),
)

DANGEROUS_MCP_TOOLS = frozenset({"task_validate"})
DANGEROUS_MCP_PARAMS = frozenset({"attach", "output", "script", "file", "body_file"})


def cc_notes_mcp_tool(tool_name: str | None) -> str | None:
    if not tool_name:
        return None
    match tool_name.split("__", 2):
        case ["mcp", server, tool] if server in CC_NOTES_SERVERS:
            return tool
        case _:
            return None


class CcNotesMcp(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        tool = cc_notes_mcp_tool(evt.tool_name)
        if tool is None or tool in DANGEROUS_MCP_TOOLS:
            return False
        raw = evt.input.raw
        return not any(raw.get(param) for param in DANGEROUS_MCP_PARAMS)


def option_name(flag: str) -> str:
    return flag.partition("=")[0]


def contains_in_order(args: tuple[str, ...], run: tuple[str, ...]) -> bool:
    remaining = iter(args)
    return all(token in remaining for token in run)


def covers_source(call: Call) -> bool:
    rest = call.source.raw
    for word in call.source.words:
        rest = rest.replace(word.raw, "", 1)
    return not rest.strip()


def has_dangerous_cli(args: tuple[str, ...]) -> bool:
    flags = tuple(arg for arg in args if arg.startswith("-"))
    options = {option_name(flag) for flag in flags}
    first_word = next((arg for arg in args if not arg.startswith("-")), None)
    return (
        any(contains_in_order(args, verb) for verb in DANGEROUS_CLI_VERBS)
        or bool(options & DANGEROUS_CLI_FLAGS)
        or any(flag.startswith("-o") and not flag.startswith("--") for flag in flags)
        or (first_word == "mcp" and "--dir" in options)
    )


class CcNotesCli(CustomCondition):
    def check(self, evt: BaseHookEvent) -> bool:
        if not (cmd := evt.command) or not is_single_command(cmd.line):
            return False
        calls = cmd.calls()
        if len(calls) != 1:
            return False
        call = calls[0]
        words = call.source.words
        if any(word.value is None or word.expandable for word in words):
            return False
        args = tuple(word.value for word in words[1:] if word.value is not None)
        return (
            call.source.raw == cmd.line.raw.strip()
            and covers_source(call)
            and call.source.executable in CC_NOTES_EXECUTABLES
            and not call.source.env
            and not call.substituted
            and "--" not in args
            and not has_dangerous_cli(args)
        )


class McpTool(CustomCondition):

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
        Input(tool="mcp__evil__note_add", tool_input={"title": "t"}): Ask(),
        Input(tool="mcp__cc-notes-evil__status", tool_input={}): Ask(),
        Input(tool="mcp__xcc-notes__status", tool_input={}): Ask(),
        Input(tool="mcp__plugin_cc-notes_cc-notes_evil__status", tool_input={}): Ask(),
        Input(tool="note_add", tool_input={"title": "t"}): Ask(),
        Input(tool="mcp__cc-notes__task_validate", tool_input={"id": "a1b2", "yes": True}): Ask(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_validate", tool_input={"id": "a1b2"}): Ask(),
        Input(tool="mcp__cc-notes__task_criterion_script", tool_input={"id": "a1b2", "file": "/tmp/x.sh"}): Ask(),
        Input(tool="mcp__cc-notes__task_criterion_add", tool_input={"id": "a1b2", "text": "t", "script": "/x.sh"}): Ask(),
        Input(tool="mcp__cc-notes__attachment_get", tool_input={"id": "a1b2", "name": "x", "output": "/tmp/x"}): Ask(),
        Input(tool="mcp__cc-notes__note_add", tool_input={"title": "t", "attach": ["/etc/passwd"]}): Ask(),
        Input(tool="mcp__cc-notes__log_append", tool_input={"id": "a1b2", "attach": ["/etc/passwd"]}): Ask(),
        Input(tool="mcp__cc-notes__plan_add", tool_input={"title": "t", "body_file": "/etc/passwd"}): Ask(),
        Input(tool="mcp__cc-notes__plan_add", tool_input={"title": "t", "body": "the plan"}): Allow(explicit=True),
        Input(tool="mcp__cc-notes__note_add", tool_input={"title": "t", "attach": []}): Allow(explicit=True),
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
        Input(command="cc-notes note add $(whoami)"): Ask(),
        Input(command="cc-notes note add `whoami`"): Ask(),
        Input(command='cc-notes note add "$(whoami)"'): Ask(),
        Input(command="cc-notes note show ${ID}"): Ask(),
        Input(command="cc-notes note show $ID"): Ask(),
        Input(command='cc-notes note add "a {b} c"'): Allow(explicit=True),
        Input(command="cc-notes note add a{b,c}"): Ask(),
        Input(command=""): Ask(),
        Input(command="   "): Ask(),
        Input(command="cc-notes note list | tee /tmp/out"): Ask(),
        Input(command="cc-notes status && rm -rf x"): Ask(),
        Input(command="cc-notes status & rm -rf /"): Ask(),
        Input(command="cc-notes status ; rm -rf x"): Ask(),
        Input(command="cc-notes status || rm -rf x"): Ask(),
        Input(command="cc-notes status\nrm -rf /"): Ask(),
        Input(command="cc-notes note list > /tmp/out"): Ask(),
        Input(command="cc-notes note add t --body - <<'EOF'\nbody\nEOF"): Ask(),
        Input(command="echo hi | cc-notes note add t --body -"): Ask(),
        Input(command="CC_NOTES_DEBUG=1 cc-notes status"): Ask(),
        Input(command="/tmp/evil/cc-notes status"): Ask(),
        Input(command="./cc-notes status"): Ask(),
        Input(command="sudo cc-notes status"): Ask(),
        Input(command="env cc-notes status"): Ask(),
        Input(command="exec cc-notes status"): Ask(),
        Input(command="cc-notes note list -- --output=/tmp/pwned"): Ask(),
        Input(command="cc-notesx status"): Ask(),
        Input(command="cc-notes attachment get a1b2 secret -o /Users/v/.ssh/authorized_keys"): Ask(),
        Input(command="cc-notes attachment get a1b2 secret --output=/etc/x"): Ask(),
        Input(command="cc-notes note add pwn --attach /etc/passwd"): Ask(),
        Input(command="cc-notes log append a1b2 --attach /Users/v/.ssh/id_rsa"): Ask(),
        Input(command="cc-notes note add t --apply /tmp/x"): Ask(),
        Input(command="cc-notes doc add d --abort /tmp/x"): Ask(),
        Input(command="cc-notes task validate a1b2 --yes"): Ask(),
        Input(command="cc-notes task criterion script a1b2 /tmp/payload.sh"): Ask(),
        Input(command="cc-notes task criterion add a1b2 check --script /tmp/payload.sh"): Ask(),
        Input(command="cc-notes plan add p --body-file /Users/v/.ssh/id_rsa"): Ask(),
        Input(command="cc-notes plan add p --body-file=/etc/passwd"): Ask(),
        Input(command="cc-notes --unknown task validate a1b2"): Ask(),
        Input(command="cc-notes task --repo /tmp/foo validate a1b2 --yes"): Ask(),
        Input(command='cc-notes task "val"idate a1b2 --yes'): Ask(),
        Input(command='cc-notes note add t "--att"ach /etc/passwd'): Ask(),
        Input(command="cc-notes status <(id)"): Ask(),
        Input(command="cc-notes note add mcp --dir internal/auth"): Allow(explicit=True),
        Input(command="time cc-notes status"): Ask(),
        Input(command="command cc-notes status"): Ask(),
        Input(command="cc-notes workflows install --dest ../../../tmp/evil"): Ask(),
        Input(command="cc-notes mcp --dir /some/repo"): Ask(),
        Input(command="cc-notes mcp --dir=/some/repo"): Ask(),
        Input(command="cc-notes attachment get a1b2 secret"): Allow(explicit=True),
        Input(command="cc-notes task validate a1b2"): Ask(),
        Input(command='cc-notes note add x --dir internal/auth'): Allow(explicit=True),
        Input(command='cc-notes doc add d --body b --dir internal/api'): Allow(explicit=True),
        Input(command='cc-notes log list --dir internal/sync'): Allow(explicit=True),
        Input(tool="mcp__srv__Bash", tool_input={"command": "cc-notes status"}): Ask(),
    },
)
