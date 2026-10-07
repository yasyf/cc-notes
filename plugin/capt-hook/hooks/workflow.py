
from __future__ import annotations

import hashlib
import json
import os
import subprocess
from collections.abc import Callable
from typing import NamedTuple

from captain_hook import (
    Allow,
    BaseHookEvent,
    CommandSchema,
    CustomCondition,
    Event,
    HookResult,
    Input,
    Operand,
    Option,
    Or,
    PostToolUseEvent,
    Prompt,
    SessionEndEvent,
    Tool,
    Warn,
    nudge,
    on,
)
from captain_hook.cmd import Call, Cmd
from captain_hook.util.paths import resolve_project_dir
from pydantic import BaseModel, Field

from .common import (
    CC_NOTES_EXECUTABLES,
    CcNotesAvailable,
    ManyNativeTasks,
    MCP_TOOL_PREFIX,
    NATIVE_TASK_MIRROR_THRESHOLD,
    NUDGE_MAX_FIRES,
    RecordVerdict,
    current_branch,
    ids_match,
    record_command,
    repo_root,
    run_cc_notes,
)

DRY_RUN_FLAGS = frozenset({"--dry-run"})
PUSH_DRY_RUN_FLAGS = DRY_RUN_FLAGS | {"-n"}
HELP_FLAGS = frozenset({"--help", "-h"})
_NULLIFYING_FLAGS = HELP_FLAGS | DRY_RUN_FLAGS

WORDS = Operand("words", count="*")
SCHEMAS: dict[str, CommandSchema] = {
    "git": CommandSchema(
        "git",
        options=(
            Option("repo", ("-C",)),
            Option("config", ("-c", "--config-env")),
            Option("git_dir", ("--git-dir",)),
            Option("work_tree", ("--work-tree",)),
            Option("namespace", ("--namespace",)),
            Option("exec_path", ("--exec-path",)),
            Option("no_pager", ("--no-pager",), bool),
        ),
        operands=(WORDS,),
    ),
    "jj": CommandSchema(
        "jj",
        options=(Option("repo", ("-R", "--repository")), Option("no_pager", ("--no-pager",), bool), Option("quiet", ("--quiet",), bool)),
        operands=(WORDS,),
    ),
    **dict.fromkeys(
        sorted(CC_NOTES_EXECUTABLES),
        CommandSchema(
            "cc-notes",
            options=(
                Option("repo", ("-R", "--repo")),
                Option("branch", ("--branch",)),
                Option("json", ("--json",), bool),
                Option("help", ("--help", "-h"), bool),
                Option("dry_run", ("--dry-run",), bool),
                Option("steal", ("--steal",), bool),
                Option("sync", ("--sync",), bool),
                Option("force", ("--force",), bool),
            ),
            operands=(WORDS,),
        ),
    ),
}


def invocation(call: Call) -> tuple[tuple[str, ...], tuple[str | None, ...]]:
    if (schema := SCHEMAS.get(call.name)) is None:
        return call.verb_argv, ()
    bound = schema.bind(call)
    words = tuple(word.value if word.value is not None else word.raw for word in bound.words.get("words", ()))
    repos = tuple(value if isinstance(value, str) else None for value in bound.values.get("repo", ()))
    return (call.name, *words), repos


class CommandFamily(CustomCondition):
    def __init__(self, prefixes: tuple[tuple[str, ...], ...], exclude: frozenset[str] = frozenset()) -> None:
        self.prefixes = prefixes
        self.exclude = exclude

    def check(self, evt: BaseHookEvent) -> bool:
        return any(self.fires(call) for call in evt.cmd.calls())

    def fires(self, call: Call) -> bool:
        argv = invocation(call)[0]
        return any(argv[: len(prefix)] == prefix for prefix in self.prefixes) and self.exclude.isdisjoint(call.args)


COMMIT_COMMANDS = CommandFamily(
    (("git", "commit"), ("jj", "commit"), ("jj", "describe"), ("ccx", "vcs", "ship")), DRY_RUN_FLAGS
)
FETCH_MERGE_COMMANDS = CommandFamily((("git", "merge"), ("git", "pull"), ("jj", "git", "fetch")))
PUSH_COMMANDS = CommandFamily((("git", "push"), ("jj", "git", "push")), PUSH_DRY_RUN_FLAGS)
CLAIM_COMMANDS = CommandFamily(
    tuple((program, "task", verb) for program in sorted(CC_NOTES_EXECUTABLES) for verb in ("claim", "start")),
    HELP_FLAGS,
)

CC_NOTES_WRITE_VERBS: dict[str, frozenset[str]] = {
    "note": frozenset({"add", "edit", "rm", "verify", "supersede", "expire"}),
    "doc": frozenset({"add", "edit", "rm", "verify", "supersede", "expire"}),
    "answer": frozenset({"add", "edit", "rm", "verify", "supersede", "expire"}),
    "log": frozenset({"add", "append", "edit", "rm"}),
    "task": frozenset({"add", "edit", "done", "cancel", "claim", "start", "renew", "comment", "dep", "undep", "validate"}),
    "project": frozenset({"add", "edit", "comment", "complete", "cancel", "archive"}),
    "sprint": frozenset({"add", "edit", "comment", "complete", "cancel", "activate"}),
    "runbook": frozenset({"add", "edit", "comment", "activate", "archive"}),
    "ledger": frozenset({"add", "edit", "rm", "comment", "activate", "archive", "sync"}),
    "investigation": frozenset(
        {"open", "add", "append", "root-cause", "fix", "confirm", "exonerate", "abandon", "reopen", "edit", "rm"}
    ),
    "plan": frozenset(
        {"add", "edit", "rm", "approve", "start", "reopen", "done", "abandon", "comment", "supersede"}
    ),
}

CC_NOTES_WRITE_SUBGROUP_READS: dict[tuple[str, str], frozenset[str]] = {
    ("task", "criterion"): frozenset({"list"}),
    ("runbook", "step"): frozenset({"list"}),
    ("runbook", "run"): frozenset({"list", "show"}),
    ("ledger", "row"): frozenset({"list"}),
    ("investigation", "finding"): frozenset({"list"}),
}

CC_NOTES_BARE_NOUN_READS: dict[str, frozenset[str]] = {
    "reconcile": frozenset(),
    "papercut": frozenset({"list"}),
}

MCP_READ_TOOLS = frozenset(
    {
        "note_list", "note_show", "note_search", "note_review",
        "doc_list", "doc_show", "doc_search", "doc_review",
        "answer_list", "answer_show", "answer_search", "answer_review",
        "log_list", "log_show", "log_search",
        "task_list", "task_show", "task_ready", "task_backlog", "task_stale", "task_archived", "task_criterion_list",
        "project_list", "project_show",
        "sprint_list", "sprint_show",
        "runbook_list", "runbook_show",
        "investigation_list", "investigation_show", "investigation_search", "investigation_finding_list",
        "plan_list", "plan_show", "plan_search",
        "ledger_list", "ledger_show", "ledger_search", "ledger_row_list",
        "papercut_list",
        "status", "blame", "history", "relevant", "attachment_get", "attachment_path", "sync",
    }
)


def is_cc_notes_write(call: Call) -> bool:
    if call.name not in CC_NOTES_EXECUTABLES or not _NULLIFYING_FLAGS.isdisjoint(call.args):
        return False
    words = invocation(call)[0][1:]
    if not words:
        return False
    noun = words[0]
    verb = words[1] if len(words) > 1 else ""
    if (bare_reads := CC_NOTES_BARE_NOUN_READS.get(noun)) is not None:
        return verb not in bare_reads
    if (reads := CC_NOTES_WRITE_SUBGROUP_READS.get((noun, verb))) is not None:
        sub = words[2] if len(words) > 2 else ""
        return bool(sub) and sub not in reads
    return verb in CC_NOTES_WRITE_VERBS.get(noun, frozenset())


class CcNotesCliWrite(CustomCondition):
    def check(self, evt: BaseHookEvent) -> bool:
        return any(is_cc_notes_write(call) for call in evt.cmd.calls())


class CcNotesMcpWrite(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        name = evt.tool_name
        if not name or not name.startswith(MCP_TOOL_PREFIX):
            return False
        suffix = name[len(MCP_TOOL_PREFIX) :]
        if suffix in MCP_READ_TOOLS:
            return False
        if suffix == "reconcile" and bool(evt.input.raw.get("dry_run")):
            return False
        return True


_LOCAL_REF_PREFIX = "refs/cc-notes/"
_TRACKING_NS = "refs/cc-notes-sync/"
_OLD_FETCH_REFSPEC = "+refs/cc-notes/*:refs/cc-notes/*"


def _fetch_refspec(name: str) -> str:
    return f"+refs/cc-notes/*:refs/cc-notes-sync/{name}/*"


def records_git(evt: BaseHookEvent, root: str | None = None) -> tuple[str, ...]:
    here = ("-C", root) if root is not None else ()
    binding = evt.ctx.git(*here, "config", "--local", "--get", "cc-notes.storage")
    return (f"--git-dir={json.loads(binding)['commonDir']}",) if binding else here


def wired_remotes(evt: BaseHookEvent, records: tuple[str, ...]) -> list[str]:
    try:
        out = evt.ctx.git(*records, "config", "--get-regexp", r"^remote\..*\.fetch$")
    except (OSError, subprocess.SubprocessError):
        return []
    if not out:
        return []
    remotes: list[str] = []
    seen: set[str] = set()
    for row in out.splitlines():
        key, _, value = row.partition(" ")
        if not key.startswith("remote.") or not key.endswith(".fetch"):
            continue
        name = key[len("remote.") : -len(".fetch")]
        if name not in seen and value in (_fetch_refspec(name), _OLD_FETCH_REFSPEC):
            seen.add(name)
            remotes.append(name)
    return remotes


def should_autosync(evt: PostToolUseEvent, target: str = "") -> bool:
    key = str(len(evt.ctx.t) - len(evt.ctx.turn))
    return evt.ctx.s.once(f"{key}:{target}" if target else key, scope="autosync")


def run_sync(evt: BaseHookEvent, *, remote: str | None = None, cwd: str | None = None) -> bool | None:
    args = ["cc-notes", "sync", *(("--remote", remote) if remote is not None else ())]
    try:
        if cwd is None:
            evt.ctx.call_cli(args, timeout=15)
        else:
            subprocess.run(args, cwd=cwd, env=os.environ, check=True, capture_output=True, text=True, timeout=15)
    except subprocess.CalledProcessError as e:
        return None if "remote not configured" in (e.stderr or "") else False
    except (OSError, subprocess.SubprocessError):
        return None
    return True


class SyncOutcome(NamedTuple):
    remote: str
    synced: bool
    failure: str | None


def do_sync(evt: BaseHookEvent, records: tuple[str, ...]) -> list[SyncOutcome]:
    remotes = wired_remotes(evt, records)
    if not remotes:
        ok = run_sync(evt)
        return [SyncOutcome("", ok is True, "cc-notes sync failed — run `cc-notes sync` to retry." if ok is False else None)]
    outcomes = []
    for r in remotes:
        ok = run_sync(evt, remote=r)
        outcomes.append(SyncOutcome(r, ok is True, f"cc-notes sync failed for {r} — run `cc-notes sync --remote {r}` to retry." if ok is False else None))
    return outcomes


def cross_sync(evt: BaseHookEvent, cwd: str) -> list[SyncOutcome]:
    ok = run_sync(evt, cwd=cwd)
    return [SyncOutcome("", ok is True, f"cc-notes sync failed in {cwd} — run `cc-notes sync` there to retry." if ok is False else None)]


def auto_sync(evt: PostToolUseEvent, records: tuple[str, ...]) -> list[SyncOutcome]:
    return do_sync(evt, records) if should_autosync(evt) else []


class SyncFailures(BaseModel):
    by_target: dict[str, str] = Field(default_factory=dict)


def record_sync(evt: BaseHookEvent, target: str, outcomes: list[SyncOutcome]) -> None:
    pending = evt.ctx.s.load(SyncFailures).by_target
    changes = [(f"{target}#{o.remote}", o) for o in outcomes if o.failure or (o.synced and f"{target}#{o.remote}" in pending)]
    if not changes:
        return
    with evt.ctx.s[SyncFailures].mutate() as state:
        for key, outcome in changes:
            if outcome.failure:
                state.by_target[key] = outcome.failure
            else:
                state.by_target.pop(key, None)


def _resolve_dir(cwd: str | None, arg: str | None) -> str | None:
    if arg is None or arg == "-" or arg.startswith("~") or "$" in arg or "`" in arg:
        return None
    if os.path.isabs(arg):
        return os.path.normpath(arg)
    return os.path.normpath(os.path.join(cwd, arg)) if cwd is not None else None


def command_dirs(cmd: Cmd, base: str | None, matches: Callable[[Call], bool]) -> list[str | None]:
    dirs: list[str | None] = []
    for call in cmd.calls():
        if matches(call):
            target = str(call.cwd) if call.cwd is not None else base
            for repo in invocation(call)[1]:
                target = _resolve_dir(target, repo)
            dirs.append(target)
    return dirs


def target_repos(evt: PostToolUseEvent, matches: Callable[[Call], bool]) -> list[str]:
    base = str(evt.cwd) if evt.cwd else resolve_project_dir()
    dirs = command_dirs(evt.cmd, base, matches) if evt.cmd else [base]
    roots: list[str] = []
    for target in dirs:
        start = target if target is not None and os.path.isdir(target) else base
        if start is not None and (root := repo_root(start)) is not None and root not in roots:
            roots.append(root)
    return roots


def cc_notes_repo(evt: BaseHookEvent, records: tuple[str, ...]) -> bool:
    try:
        out = evt.ctx.git(*records, "for-each-ref", "--count=1", "--format=%(refname)", "refs/cc-notes/")
    except (OSError, subprocess.SubprocessError):
        return False
    return bool(out and out.strip())


def session_root() -> str | None:
    base = resolve_project_dir()
    return repo_root(base) if base is not None else None


def sync_targets(evt: PostToolUseEvent, matches: Callable[[Call], bool], *, reconcile: bool = False) -> None:
    session = session_root()
    for root in target_repos(evt, matches):
        records = records_git(evt, root)
        if not cc_notes_repo(evt, records):
            continue
        if root == session:
            record_sync(evt, "", auto_reconcile(evt, records) if reconcile else auto_sync(evt, records))
            continue
        if reconcile:
            branch = (evt.ctx.git("-C", root, "rev-parse", "--abbrev-ref", "HEAD") or "").strip()
            if branch and branch != "HEAD":
                run_cc_notes(evt, "-R", root, "reconcile", "--into", branch)
        if should_autosync(evt, target=root):
            record_sync(evt, root, cross_sync(evt, root))


def auto_reconcile(evt: PostToolUseEvent, records: tuple[str, ...]) -> list[SyncOutcome]:
    if branch := current_branch(evt):
        run_cc_notes(evt, "reconcile", "--into", branch)
    return auto_sync(evt, records)


TASK_MCP_PREFIX = MCP_TOOL_PREFIX + "task_"
TASK_CLAIM_VERBS = frozenset({"claim", "start"})
TASK_RELEASE_VERBS = frozenset({"done", "cancel"})
TASK_LEASE_VERBS = TASK_CLAIM_VERBS | TASK_RELEASE_VERBS


class ClaimedTasks(BaseModel):
    ids: list[str] = Field(default_factory=list)


def _task_lease_call(evt: BaseHookEvent) -> tuple[str, str] | None:
    name = evt.tool_name or ""
    if name.startswith(TASK_MCP_PREFIX):
        task_id = evt.input.raw.get("id")
        return (name[len(TASK_MCP_PREFIX) :], task_id) if isinstance(task_id, str) and task_id else None
    for call in evt.cmd.calls():
        if call.name not in CC_NOTES_EXECUTABLES or not HELP_FLAGS.isdisjoint(call.args):
            continue
        match invocation(call)[0][1:]:
            case ["task", verb, task_id, *_]:
                return verb, task_id
    return None


class TaskLeaseCall(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        call = _task_lease_call(evt)
        return call is not None and call[0] in TASK_LEASE_VERBS


@on(
    Event.PostToolUse,
    only_if=[TaskLeaseCall()],
    max_fires=None,
    tests={
        Input(command="cc-notes task claim abc1234"): Allow(),
        Input(command="cc-notes task renew abc1234"): Allow(),
        Input(command="cc-notes task list"): Allow(),
        Input(command="cc-notes task claim abc1234 --help"): Allow(),
        Input(command="echo 'cc-notes task claim abc1234'"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_claim", tool_input={"id": "abc1234"}): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_list"): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def record_task_claims(evt: PostToolUseEvent) -> HookResult | None:
    call = _task_lease_call(evt)
    if call is None or call[0] not in TASK_LEASE_VERBS:
        return None
    verb, task_id = call
    with evt.ctx.s[ClaimedTasks].mutate() as held:
        held.ids = [existing for existing in held.ids if not ids_match(existing, task_id)]
        if verb in TASK_CLAIM_VERBS:
            held.ids.append(task_id)
    return None


def held_task(evt: BaseHookEvent) -> str | None:
    held = evt.ctx.s.load(ClaimedTasks).ids
    return held[0] if len(held) == 1 else None


def commits_to_session_repo(evt: PostToolUseEvent) -> bool:
    return (session := session_root()) is not None and session in target_repos(evt, COMMIT_COMMANDS.fires)


def link_commit(evt: PostToolUseEvent) -> None:
    if (task := held_task(evt)) is None:
        return
    sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    if sha and not evt.ctx.s.once(sha, scope="commit-link"):
        return
    if run_cc_notes(evt, "task", "link", task, "HEAD") is None:
        record_sync(
            evt,
            f"link:{sha}",
            [
                SyncOutcome(
                    "",
                    False,
                    f"cc-notes could not link commit {sha[:7] or 'HEAD'} onto task {task} — run `cc-notes task link {task} {sha or 'HEAD'}` to retry.",
                )
            ],
        )


COMMIT_DECISION_SYSTEM = (
    "An agent just landed a git commit. Decide whether the change embodies a durable DECISION worth "
    "capturing as a cc-notes record — a design choice, a tradeoff, a non-obvious rationale a future "
    "agent would want explained — as opposed to routine, self-explanatory work (a rename, a "
    "dependency bump, a formatting pass, a mechanical fix) that the diff and message already cover.\n"
    "\n"
    "Set record=false for routine or self-explanatory commits — that is most commits. Only a commit "
    "that encodes a decision worth preserving records. When record=true choose the kind: note for a "
    "single durable fact or decision (one verifiable claim), doc for longer living rationale a future "
    "agent should read before touching this area."
)


def commit_decision(evt: PostToolUseEvent) -> str | None:
    if not (diff := evt.ctx.diff(commit="HEAD")):
        return None
    prompt = (
        Prompt()
        .system(COMMIT_DECISION_SYSTEM)
        .context("commit", diff)
        .ask("Does this commit encode a durable decision worth a cc-notes note or doc?")
    )
    verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    if not verdict.record or verdict.kind not in ("note", "doc"):
        return None
    return f"This commit encodes a durable decision. Capture it with `{record_command(verdict.kind)}`."


@on(
    Event.PostToolUse,
    only_if=[COMMIT_COMMANDS, CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="git status"): Allow(),
        Input(command="jj diff"): Allow(),
        Input(command="echo 'jj commit now'"): Allow(),
        Input(command="git commit --dry-run"): Allow(),
    },
)
def nudge_commit_decision(evt: PostToolUseEvent) -> HookResult | None:
    sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    if sha and not evt.ctx.s.once(sha, scope="commit-decision"):
        return None
    message = commit_decision(evt)
    return evt.warn(message) if message else None


@on(
    Event.PostToolUse,
    only_if=[COMMIT_COMMANDS, CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="git status"): Allow(),
        Input(command="git commit --dry-run"): Allow(),
    },
)
def nudge_commit_task_link(evt: PostToolUseEvent) -> HookResult | None:
    sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    if sha and not evt.ctx.s.once(sha, scope="commit-task-link"):
        return None
    if not commits_to_session_repo(evt) or (task := held_task(evt)) is None:
        return None
    return evt.warn(f"This commit is being linked onto the task you hold, `{task}`. Undo with `cc-notes task unlink {task}`.")


nudge(
    "Link each commit to its task with a `cc-task: <id>` trailer. Query the link with `cc-notes history <id>`.",
    events=Event.PostToolUse,
    only_if=[COMMIT_COMMANDS, CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="git commit -m x"): Warn(pattern="cc-task"),
        Input(command="git commit --dry-run"): Allow(),
    },
)


@on(
    Event.PostToolUse,
    only_if=[FETCH_MERGE_COMMANDS, CcNotesAvailable()],
    max_fires=None,
    async_=True,
    tests={
        Input(command="git status"): Allow(),
        Input(command="git log --no-merges"): Allow(),
        Input(command="jj git remote list"): Allow(),
    },
)
def reconcile_after_merge(evt: PostToolUseEvent) -> None:
    sync_targets(evt, FETCH_MERGE_COMMANDS.fires, reconcile=True)


nudge(
    "You hold a lease now. Run `cc-notes task renew <id>` on long silent stretches and `cc-notes task done <id>` when finished.",
    events=Event.PostToolUse,
    only_if=[CLAIM_COMMANDS, CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="cc-notes task claim abc1234"): Warn(pattern="task renew"),
        Input(command="cc-notes task list"): Allow(),
        Input(command="cc-notes task show abc1234"): Allow(),
        Input(command="cc-notes task claim abc --help"): Allow(),
    },
)


@on(
    Event.PostToolUse,
    only_if=[Or(COMMIT_COMMANDS, CLAIM_COMMANDS, PUSH_COMMANDS), CcNotesAvailable()],
    max_fires=None,
    async_=True,
    tests={
        Input(command="git status"): Allow(),
        Input(command="git log --grep 'git push'"): Allow(),
        Input(command="echo 'jj git push'"): Allow(),
        Input(command="jj rebase -d main"): Allow(),
        Input(command="git push --dry-run"): Allow(),
        Input(command="git push -n"): Allow(),
        Input(command="git commit --dry-run"): Allow(),
        Input(command="cc-notes task claim abc --help"): Allow(),
    },
)
def sync_after_ref_move(evt: PostToolUseEvent) -> None:
    if commits_to_session_repo(evt):
        link_commit(evt)
    sync_targets(evt, lambda call: COMMIT_COMMANDS.fires(call) or CLAIM_COMMANDS.fires(call) or PUSH_COMMANDS.fires(call))


@on(
    Event.PostToolUse,
    only_if=[Or(CcNotesCliWrite(), CcNotesMcpWrite()), CcNotesAvailable()],
    max_fires=None,
    async_=True,
    tests={
        Input(command="cc-notes note list --json"): Allow(),
        Input(command="cc-notes task criterion list abc"): Allow(),
        Input(command="cc-notes runbook run show abc"): Allow(),
        Input(command="echo 'cc-notes note add'"): Allow(),
        Input(command="cc-notes note add x --help"): Allow(),
        Input(command="cc-notes reconcile --dry-run"): Allow(),
        Input(command="cc-notes papercut list"): Allow(),
        Input(command="cc-notes papercut --json list"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_list"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__sync"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__runbook_list"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__papercut_list"): Allow(),
        Input(command="cd /other && cc-notes note list"): Allow(),
        Input(command="cd /other && cc-notes papercut list"): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def sync_after_record_write(evt: PostToolUseEvent) -> None:
    sync_targets(evt, is_cc_notes_write)


@on(
    Event.PostToolUse | Event.PostToolUseFailure | Event.UserPromptSubmit,
    max_fires=None,
    tests={
        Input(command="git status"): Allow(),
        Input(prompt="keep going"): Allow(),
    },
)
def surface_sync_failures(evt: BaseHookEvent) -> HookResult | None:
    if not evt.ctx.s.load(SyncFailures).by_target:
        return None
    with evt.ctx.s[SyncFailures].mutate() as state:
        failed, state.by_target = list(state.by_target.values()), {}
    by_digest = {hashlib.sha256(message.encode()).hexdigest(): message for message in failed}
    fresh = evt.ctx.s.unseen(list(by_digest), scope="sync-failures")
    return evt.warn(*(by_digest[digest] for digest in fresh)) if fresh else None


def cc_notes_refs_dirty(evt: BaseHookEvent, records: tuple[str, ...]) -> bool:
    out = evt.ctx.git(*records, "for-each-ref", "--format=%(refname) %(objectname)", _LOCAL_REF_PREFIX, _TRACKING_NS)
    if not out:
        return False
    local: dict[str, str] = {}
    tracking: list[tuple[str, str]] = []
    for row in out.splitlines():
        refname, _, oid = row.partition(" ")
        if refname.startswith(_LOCAL_REF_PREFIX):
            local[refname[len(_LOCAL_REF_PREFIX) :]] = oid
        elif refname.startswith(_TRACKING_NS):
            tracking.append((refname[len(_TRACKING_NS) :], oid))
    if not local:
        return False
    remotes = wired_remotes(evt, records)
    if not remotes:
        return True
    per_remote: dict[str, dict[str, str]] = {r: {} for r in remotes}
    for path, oid in tracking:
        for r in sorted(remotes, key=len, reverse=True):
            if path.startswith(f"{r}/"):
                per_remote[r][path[len(r) + 1 :]] = oid
                break
    return any(per_remote[r].get(suffix) != oid for r in remotes for suffix, oid in local.items())


@on(Event.SessionEnd, only_if=[CcNotesAvailable()], async_=True)
def sync_at_session_end(evt: SessionEndEvent) -> None:
    records = records_git(evt)
    if cc_notes_refs_dirty(evt, records):
        do_sync(evt, records)


@on(
    Event.PostToolUse,
    only_if=[Tool("TaskCreate"), ManyNativeTasks(), CcNotesAvailable()],
    max_fires=1,
    tests={
        Input(
            tool="TaskCreate",
            tasks=[{"id": str(i), "subject": f"t{i}", "status": "pending"} for i in range(NATIVE_TASK_MIRROR_THRESHOLD)],
        ): Warn(pattern="Native tasks vanish at session end"),
        Input(tool="TaskCreate", tasks=[{"id": "1", "subject": "t1", "status": "in_progress"}]): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def nudge_mirror_native_tasks(evt: PostToolUseEvent) -> HookResult:
    return evt.warn(
        "Native tasks vanish at session end and are private to this agent. Mirror durable or cross-agent ones with `cc-notes task add --criterion <how to verify it is done>`."
    )
