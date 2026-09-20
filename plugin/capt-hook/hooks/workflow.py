"""Commit/claim/merge workflow nudges, with auto-sync and auto-reconcile run in the background."""

from __future__ import annotations

import os
import subprocess
from collections.abc import Callable
from typing import NamedTuple

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    HookResult,
    Input,
    Or,
    PostToolUseEvent,
    Prompt,
    SessionEndEvent,
    Tool,
    Warn,
    on,
)
from captain_hook.cmd import COMMAND_VALUE_FLAGS, INFO_OPTIONS
from captain_hook.util.paths import resolve_project_dir
from cc_transcript.command import Command, CommandLine
from pydantic import BaseModel, Field

from .common import (
    CC_NOTES_EXECUTABLES,
    CcNotesAvailable,
    ManyNativeTasks,
    MCP_TOOL_PREFIX,
    NATIVE_TASK_MIRROR_THRESHOLD,
    NUDGE_MAX_FIRES,
    RecordVerdict,
    ids_match,
    mcp_active,
    record_command,
    repo_root,
    run_cc_notes,
)

# Flags that turn an otherwise-matched leg into a no-op the pack must not react to: a dry run
# publishes/writes nothing, a help/usage invocation runs nothing. `-n` is git push's --dry-run short
# form, but `git commit -n` is --no-verify, so the -n form is scoped to the push family alone.
DRY_RUN_FLAGS = frozenset({"--dry-run"})
PUSH_DRY_RUN_FLAGS = DRY_RUN_FLAGS | {"-n"}
HELP_FLAGS = frozenset({"--help", "-h"})
_NULLIFYING_FLAGS = HELP_FLAGS | DRY_RUN_FLAGS

# The options that point a command at another repository. git only takes `-C` before its subcommand
# (`git commit -C <rev>` reuses a message), so its scan stops at the first other token; jj and
# cc-notes accept theirs anywhere.
REPO_OPTIONS: dict[str, frozenset[str]] = {
    "git": frozenset({"-C"}),
    "jj": frozenset({"-R", "--repository"}),
    "cc-notes": frozenset({"-R", "--repo"}),
    "ccn": frozenset({"-R", "--repo"}),
}


def repo_argv(cmd: Command) -> tuple[tuple[str, ...], tuple[str, ...]]:
    """The leg's argv with its repository options removed, and the directories those options named, in order."""
    argv = cmd.argv
    if not argv or (options := REPO_OPTIONS.get(argv[0])) is None:
        return argv, ()
    rest: list[str] = [argv[0]]
    dirs: list[str] = []
    i = 1
    while i < len(argv):
        name, eq, value = argv[i].partition("=")
        if argv[i] in options and i + 1 < len(argv):
            dirs.append(argv[i + 1])
            i += 2
        elif eq and name.startswith("--") and name in options:
            dirs.append(value)
            i += 1
        elif argv[0] == "git":
            rest.extend(argv[i:])
            break
        else:
            rest.append(argv[i])
            i += 1
    return tuple(rest), tuple(dirs)


def verb_argv(cmd: Command) -> tuple[str, ...]:
    """The leg's program plus its arguments from the verb on, every global option before the verb dropped.

    Mirrors ``Call.verb_argv`` over the same ``COMMAND_VALUE_FLAGS`` table — the fallback ``Runs`` itself
    takes for an option-led call — so ``git -C ../repo commit``, ``git --no-pager commit`` and
    ``git -c x=y push`` all read as their verb. An informational option ends the run, leaving
    ``git --help commit`` reading as a ``--help``, never as a ``commit``.
    """
    argv = repo_argv(cmd)[0]
    if not argv:
        return argv
    value_flags = COMMAND_VALUE_FLAGS.get(argv[0], ())
    args = argv[1:]
    taken = 0
    while taken < len(args) and args[taken].startswith("-") and args[taken] not in INFO_OPTIONS:
        taken += 2 if args[taken] in value_flags and taken + 1 < len(args) else 1
    return (argv[0], *args[taken:])


class CommandFamily(CustomCondition):
    """Matches when any leg of the command line runs one of ``prefixes`` as a verb-argv prefix and that
    leg carries no effect-nullifying flag.

    Mirrors ``Runs`` — a literal prefix over the leg's :func:`verb_argv`, matched against any leg of a
    compound line, so a global option before the verb (``git --no-pager commit``, ``git -c x=y push``)
    still matches while a quoted mention (``echo "git push"``) and a wrapper (``sudo git push``) miss —
    then drops a matched leg whose argv carries an ``exclude`` flag (a ``--dry-run`` push publishes
    nothing; a ``--help`` invocation runs nothing). Never widen to regex.
    """

    def __init__(self, prefixes: tuple[tuple[str, ...], ...], exclude: frozenset[str] = frozenset()) -> None:
        self.prefixes = prefixes
        self.exclude = exclude

    def check(self, evt: BaseHookEvent) -> bool:
        line = evt.cmd.line
        return bool(line) and any(self.fires(cmd) for cmd in line.commands)

    def fires(self, cmd: Command) -> bool:
        argv = verb_argv(cmd)
        return any(argv[: len(p)] == p for p in self.prefixes) and self.exclude.isdisjoint(argv)


COMMIT_COMMANDS = CommandFamily(
    (("git", "commit"), ("jj", "commit"), ("jj", "describe"), ("ccx", "vcs", "ship")), DRY_RUN_FLAGS
)
FETCH_MERGE_COMMANDS = CommandFamily((("git", "merge"), ("git", "pull"), ("jj", "git", "fetch")))
PUSH_COMMANDS = CommandFamily((("git", "push"), ("jj", "git", "push")), PUSH_DRY_RUN_FLAGS)
# `cc-notes` and its installed `ccn` shorthand (scripts/install.sh, cmd/cc-notes/main.go) are the same
# binary, so the claim family matches both program names.
CLAIM_COMMANDS = CommandFamily(
    (
        ("cc-notes", "task", "claim"),
        ("cc-notes", "task", "start"),
        ("ccn", "task", "claim"),
        ("ccn", "task", "start"),
    ),
    HELP_FLAGS,
)

# cc-notes CLI write surface (internal/cli): the (noun, verb) pairs that mutate refs/cc-notes/*. Reads
# — list/show/search/review/ready/backlog/stale/archived/status/history — never appear here, so they
# never sync. Two-level nouns (`task criterion`, `runbook step`, `runbook run`) live in the subgroup
# table below; runbook's own top-level verbs (add/edit/comment/activate/archive) sit here.
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
    # investigation `add` is the `open` alias; `root-cause` keeps its argv hyphen.
    "investigation": frozenset(
        {"open", "add", "append", "root-cause", "fix", "confirm", "exonerate", "abandon", "reopen", "edit", "rm"}
    ),
    "plan": frozenset(
        {"add", "edit", "rm", "approve", "start", "reopen", "done", "abandon", "comment", "supersede"}
    ),
}

# Two-level nouns: (noun, group) -> the read subcommands of that group. Any other sub writes (fail
# open — an unrecognized sub costs one idempotent sync). `runbook step` mutations (add/rm/edit/move)
# and `runbook run` lifecycle (start/done/skip/fail/finish) all write; only their listings/show read.
CC_NOTES_WRITE_SUBGROUP_READS: dict[tuple[str, str], frozenset[str]] = {
    ("task", "criterion"): frozenset({"list"}),
    ("runbook", "step"): frozenset({"list"}),
    ("runbook", "run"): frozenset({"list", "show"}),
    ("ledger", "row"): frozenset({"list"}),
    ("investigation", "finding"): frozenset({"list"}),
}

# Bare-noun writes: a top-level noun that mutates without a (noun, verb) pair. The value is the noun's
# READ verbs — every other verb writes, including the empty verb and a bare positional like `papercut`'s
# complaint text. `reconcile` takes no verb and always writes (empty read set); `papercut TEXT` writes
# unless the verb is the `list` reader. Data-driven so the `reconcile` special case isn't a lone if.
CC_NOTES_BARE_NOUN_READS: dict[str, frozenset[str]] = {
    "reconcile": frozenset(),
    "papercut": frozenset({"list"}),
}

# cc-notes MCP write surface (internal/mcpserver/tools_*.go): a deny-list of the READ tool suffixes, so
# the matcher fails OPEN — a future tool triggers one harmless idempotent sync until it is listed here.
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


def _first_non_flag(tokens: tuple[str, ...]) -> str:
    # The first real (non-flag) token: `--` terminates the search with no token (everything after it is
    # positional text, so `papercut -- list` is a write), and `-`-prefixed flags are skipped. A flag
    # VALUE (`--model foo list` reads "foo") can be misread as the verb — accepted, the matcher fails
    # open to one idempotent sync.
    for tok in tokens:
        if tok == "--":
            return ""
        if not tok.startswith("-"):
            return tok
    return ""


def is_cc_notes_write(cmd: Command) -> bool:
    # A parsed cc-notes / `ccn` leg that mutates state: a bare-noun write (`reconcile`, `papercut TEXT`),
    # a (noun, verb) in the write table, or a subgroup sub that isn't a read. A help or dry-run leg
    # writes nothing.
    args = repo_argv(cmd)[0][1:]
    if cmd.program not in ("cc-notes", "ccn") or not args:
        return False
    if not _NULLIFYING_FLAGS.isdisjoint(args):
        return False
    noun = args[0]
    verb = args[1] if len(args) > 1 else ""
    if (bare_reads := CC_NOTES_BARE_NOUN_READS.get(noun)) is not None:
        # Resolve the verb flags-first (cobra allows `papercut --json list`), not positionally.
        return _first_non_flag(args[1:]) not in bare_reads
    if (reads := CC_NOTES_WRITE_SUBGROUP_READS.get((noun, verb))) is not None:
        sub = args[2] if len(args) > 2 else ""
        return bool(sub) and sub not in reads
    return verb in CC_NOTES_WRITE_VERBS.get(noun, frozenset())


class CcNotesCliWrite(CustomCondition):
    """Matches a Bash cc-notes subcommand that writes refs/cc-notes/* (a state change or reconcile)."""

    def check(self, evt: BaseHookEvent) -> bool:
        line = evt.cmd.line
        return bool(line) and any(is_cc_notes_write(cmd) for cmd in line.commands)


class CcNotesMcpWrite(CustomCondition):
    """Matches a cc-notes MCP write tool — any cc-notes tool whose suffix is not a known reader.

    A ``reconcile`` call with ``dry_run`` set only reports the plan (tools_repo.go), so it writes
    nothing and is not a sync trigger.
    """

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


def wired_remotes(evt: BaseHookEvent) -> list[str]:
    # Remotes wired for cc-notes by `sync/install.go`: their `remote.<name>.fetch` equals the tracking
    # refspec `+refs/cc-notes/*:refs/cc-notes-sync/<name>/*` or the pre-fix same-namespace form. One
    # `git config` read; config order, deduped (a name may carry dots). A git failure reads as zero
    # wired, so every caller falls back to a bare `cc-notes sync` (the autoInstall-origin bootstrap).
    try:
        out = evt.ctx.git("config", "--get-regexp", r"^remote\..*\.fetch$")
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
    # At most one sync per turn per target even when commit+claim co-occur. A scoped once-key, isolated
    # from the record router's shared fired_this_turn slot and the per-sha/per-plan once scopes. target=""
    # is the session repo and keeps the byte-identical key; each distinct cross-repo target gets its own
    # slot so a session sync never starves a foreign repo's sync.
    key = str(len(evt.ctx.t) - len(evt.ctx.turn))
    return evt.ctx.s.once(f"{key}:{target}" if target else key, scope="autosync")


def run_sync(evt: BaseHookEvent, *, remote: str | None = None, cwd: str | None = None) -> bool | None:
    # One `cc-notes sync` invocation: True synced, False genuine failure, None benign-silent (no remote /
    # timeout / missing binary). cwd=None runs the session repo via call_cli (which surfaces stderr under
    # throw); a set cwd runs a foreign repo directly — the exact subprocess.run combination reproduces
    # call_cli's exception surface (CalledProcessError.stderr, TimeoutExpired ⊂ SubprocessError,
    # FileNotFoundError ⊂ OSError).
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


def do_sync(evt: BaseHookEvent) -> list[SyncOutcome]:
    # Sync the session repo to every cc-notes-wired remote (bare when none is wired), one outcome per remote.
    # A genuine failure names its own retry: a bare `cc-notes sync` on an older binary derives origin and may
    # never re-attempt the failed remote, so each failed remote gets its own `--remote <name>`.
    remotes = wired_remotes(evt)
    if not remotes:
        ok = run_sync(evt)
        return [SyncOutcome("", ok is True, "cc-notes sync failed — run `cc-notes sync` to retry." if ok is False else None)]
    outcomes = []
    for r in remotes:
        ok = run_sync(evt, remote=r)
        outcomes.append(SyncOutcome(r, ok is True, f"cc-notes sync failed for {r} — run `cc-notes sync --remote {r}` to retry." if ok is False else None))
    return outcomes


def cross_sync(evt: BaseHookEvent, cwd: str) -> list[SyncOutcome]:
    # A cc-notes write in a foreign repo (a resolved `cd` target) syncs THAT repo, bare — its own remote
    # derivation applies, never the session repo's wired remotes. A failure names the directory.
    ok = run_sync(evt, cwd=cwd)
    return [SyncOutcome("", ok is True, f"cc-notes sync failed in {cwd} — run `cc-notes sync` there to retry." if ok is False else None)]


def auto_sync(evt: PostToolUseEvent) -> list[SyncOutcome]:
    return do_sync(evt) if should_autosync(evt) else []


class SyncFailures(BaseModel):
    by_target: dict[str, str] = Field(default_factory=dict)


def record_sync(evt: BaseHookEvent, target: str, outcomes: list[SyncOutcome]) -> None:
    # Pending warnings are kept per target repo and remote: a failure replaces that remote's warning, a
    # success clears only that remote's, and a benign outcome (no remote, a timeout) leaves it standing.
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


def _resolve_dir(cwd: str | None, arg: str) -> str | None:
    # A literal directory argument against the running cwd: `-`, a `$var`, a `~` expansion, or a backtick
    # substitution is unresolvable (None); an absolute path replaces the walk (recovering a previously-lost
    # one); a relative path joins the running cwd, or is unresolvable when there is no base to join onto.
    if arg == "-" or arg.startswith("~") or "$" in arg or "`" in arg:
        return None
    if os.path.isabs(arg):
        return os.path.normpath(arg)
    return os.path.normpath(os.path.join(cwd, arg)) if cwd is not None else None


def _apply_cd(cwd: str | None, cmd: Command) -> str | None:
    # A literal `cd` leg's new working directory. A leading `--` (end-of-options) is dropped first, so
    # `cd -- /x` resolves to /x and a lone `cd --` stays unresolvable. Only an exactly-one-arg path resolves.
    args = cmd.args[1:] if cmd.args and cmd.args[0] == "--" else cmd.args
    return _resolve_dir(cwd, args[0]) if len(args) == 1 else None


def leg_dirs(line: CommandLine, base: str | None, matches: Callable[[Command], bool]) -> list[str | None]:
    # One entry per matching leg: the directory it runs against (None when unresolvable). A single pass
    # tracks a running cwd from `base`, advanced by each literal `cd`, then applies the leg's own repository
    # options (`git -C`, `jj -R`, `cc-notes -R`). pushd, subshells, and pipeline grouping are ignored — a
    # documented structural approximation, never regex.
    cwd = base
    targets: list[str | None] = []
    for cmd in line.commands:
        if cmd.executable == "cd":
            cwd = _apply_cd(cwd, cmd)
        elif matches(cmd):
            target = cwd
            for arg in repo_argv(cmd)[1]:
                target = _resolve_dir(target, arg)
            targets.append(target)
    return targets


def write_targets(line: CommandLine, base: str | None) -> list[str | None]:
    return leg_dirs(line, base, is_cc_notes_write)


def target_repos(evt: PostToolUseEvent, matches: Callable[[Command], bool]) -> list[str]:
    # The repositories the matching legs ran against, deduped in order. A leg whose directory is unresolvable
    # or missing (a failed `cd`) ran in the event's cwd. A tool call with no command line (an MCP write)
    # targets the event's cwd. Nothing outside a git repository is a target, found by stat alone.
    base = str(evt.cwd) if evt.cwd else resolve_project_dir()
    line = evt.cmd.line
    dirs = leg_dirs(line, base, matches) if line else [base]
    roots: list[str] = []
    for target in dirs:
        start = target if target is not None and os.path.isdir(target) else base
        if start is not None and (root := repo_root(start)) is not None and root not in roots:
            roots.append(root)
    return roots


def cc_notes_repo(evt: BaseHookEvent, root: str) -> bool:
    try:
        out = evt.ctx.git("-C", root, "for-each-ref", "--count=1", "--format=%(refname)", "refs/cc-notes/")
    except (OSError, subprocess.SubprocessError):
        return False
    return bool(out and out.strip())


def session_root() -> str | None:
    base = resolve_project_dir()
    return repo_root(base) if base is not None else None


def sync_targets(evt: PostToolUseEvent, matches: Callable[[Command], bool], *, reconcile: bool = False) -> None:
    # Sync every cc-notes repository the command touched, and nothing else. The session repo syncs through
    # the session's own wired remotes; any other repo syncs bare in its own directory. Each target claims
    # its own per-turn slot and keeps its own pending failure.
    session = session_root()
    for root in target_repos(evt, matches):
        if not cc_notes_repo(evt, root):
            continue
        if root == session:
            record_sync(evt, "", auto_reconcile(evt) if reconcile else auto_sync(evt))
            continue
        if reconcile:
            branch = (evt.ctx.git("-C", root, "rev-parse", "--abbrev-ref", "HEAD") or "").strip()
            if branch and branch != "HEAD":
                run_cc_notes(evt, "-R", root, "reconcile", "--into", branch)
        if should_autosync(evt, target=root):
            record_sync(evt, root, cross_sync(evt, root))


def auto_reconcile(evt: PostToolUseEvent) -> list[SyncOutcome]:
    # Reconcile is local + idempotent (run_cc_notes is fail-closed) and runs before the sync so the
    # carried tasks ship with it. A detached HEAD (the colocated-jj norm, exactly the state `jj git fetch`
    # targets) has no branch to reconcile onto, so only the fetched refs ship. The sync rides the shared
    # per-turn token, so a commit-then-merge turn syncs once.
    branch = (evt.ctx.git("rev-parse", "--abbrev-ref", "HEAD") or "").strip()
    if branch and branch != "HEAD":
        run_cc_notes(evt, "reconcile", "--into", branch)
    return auto_sync(evt)


TASK_MCP_PREFIX = MCP_TOOL_PREFIX + "task_"
# The task verbs that move a lease, on both surfaces. `renew` keeps a hold rather than taking one,
# so it never arms; `cancel` and `done` both end the work the hold covered.
TASK_CLAIM_VERBS = frozenset({"claim", "start"})
TASK_RELEASE_VERBS = frozenset({"done", "cancel"})
TASK_LEASE_VERBS = TASK_CLAIM_VERBS | TASK_RELEASE_VERBS


class ClaimedTasks(BaseModel):
    """Session-durable trace of the task ids this session took a lease on.

    A claim/start arms an id and a done/cancel disarms it, so exactly one armed id names the task a
    commit implements; zero or several make that attribution a guess. Defaults to the pre-claim
    state, so a fresh session (or a null session slot in inline tests) holds nothing.
    """

    ids: list[str] = Field(default_factory=list)


def _task_lease_call(evt: BaseHookEvent) -> tuple[str, str] | None:
    """The (verb, task id) of a task call that moves a lease — an MCP task_* tool or a CLI leg."""
    name = evt.tool_name or ""
    if name.startswith(TASK_MCP_PREFIX):
        task_id = evt.input.raw.get("id")
        return (name[len(TASK_MCP_PREFIX) :], task_id) if isinstance(task_id, str) and task_id else None
    line = evt.cmd.line
    if not line:
        return None
    for cmd in line.commands:
        if cmd.program not in CC_NOTES_EXECUTABLES or len(cmd.args) < 2 or cmd.args[0] != "task":
            continue
        # A `--help` leg moves no lease, so it must not arm a phantom claim.
        if not HELP_FLAGS.isdisjoint(cmd.args):
            continue
        if task_id := _first_non_flag(tuple(cmd.args[2:])):
            return cmd.args[1], task_id
    return None


class TaskLeaseCall(CustomCondition):
    """Matches a task call that takes or releases a claim: claim/start/done/cancel, on either surface."""

    def check(self, evt: BaseHookEvent) -> bool:
        call = _task_lease_call(evt)
        return call is not None and call[0] in TASK_LEASE_VERBS


@on(
    Event.PostToolUse,
    only_if=[TaskLeaseCall()],
    # Uncapped: this records state rather than advising, so a session that claims and closes four
    # tasks must trace all four, not the first three.
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
    """Trace claim and release calls into session state, so a commit knows which task it implements."""
    call = _task_lease_call(evt)
    if call is None or call[0] not in TASK_LEASE_VERBS:
        return None
    verb, task_id = call
    try:
        with evt.ctx.s[ClaimedTasks].mutate() as held:
            held.ids = [existing for existing in held.ids if not ids_match(existing, task_id)]
            if verb in TASK_CLAIM_VERBS:
                held.ids.append(task_id)
    except Exception:
        pass
    return None


def held_task(evt: BaseHookEvent) -> str | None:
    """The one task this session holds a lease on, or None when it holds none or several."""
    held = evt.ctx.s.load(ClaimedTasks).ids
    return held[0] if len(held) == 1 else None


def commits_to_session_repo(evt: PostToolUseEvent) -> bool:
    return (session := session_root()) is not None and session in target_repos(evt, COMMIT_COMMANDS.fires)


def link_claimed_task(evt: PostToolUseEvent, *, mcp: bool) -> list[str]:
    """The line announcing the `task link` edge the background sync writes for the one task this session holds."""
    try:
        if not commits_to_session_repo(evt) or (task := held_task(evt)) is None:
            return []
    except Exception:
        # Fail closed on an unreadable session slot: a raised hook costs the agent a landed commit.
        return []
    undo = f"the task_unlink tool with id={task}" if mcp else f"`cc-notes task unlink {task}`"
    return [f"Linking this commit onto task {task}, the one you hold, before it syncs — undo with {undo}."]


def link_commit(evt: PostToolUseEvent) -> None:
    # Runs ahead of the sync in the same background hook, so the pushed refs carry the edge. Each HEAD
    # links once; a failed write queues its retry like a failed sync.
    try:
        if (task := held_task(evt)) is None:
            return
        sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    except Exception:
        return
    if sha and not evt.ctx.s.once(sha, scope="commit-link"):
        return
    try:
        linked = run_cc_notes(evt, "task", "link", task, "HEAD") is not None
    except Exception:
        linked = False
    if not linked:
        record_sync(evt, f"link:{sha}", [SyncOutcome("", False, f"cc-notes could not link commit {sha[:7] or 'HEAD'} onto task {task} — run `cc-notes task link {task} {sha or 'HEAD'}` to retry.")])


COMMIT_DECISION_SYSTEM = (
    "An agent just landed a git commit. Decide whether the change embodies a durable DECISION worth "
    "capturing as a cc-notes record — a design choice, a tradeoff, a non-obvious rationale a future "
    "agent would want explained — as opposed to routine, self-explanatory work (a rename, a "
    "dependency bump, a formatting pass, a mechanical fix) that the diff and message already cover.\n"
    "\n"
    "Set record=false for routine or self-explanatory commits — that is most commits. Only a commit "
    "that encodes a decision worth preserving records. When record=true choose the kind: note for a "
    "single durable fact or decision (one verifiable claim), doc for longer living rationale a future "
    "agent should read before touching this area. Return title (short), when (for a doc, the 'read "
    "this when…' trigger; empty for a note), area (the repo directory, or '.'), reasoning (one line)."
)


def commit_decision(evt: PostToolUseEvent) -> list[str]:
    try:
        diff = evt.ctx.diff(commit="HEAD")
    except Exception:
        return []
    if not diff:
        return []
    prompt = (
        Prompt()
        .system(COMMIT_DECISION_SYSTEM)
        .context("commit", diff)
        .ask("Does this commit encode a durable decision worth a cc-notes note or doc?")
    )
    try:
        verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    except Exception:
        # Deliberate fail-closed exception: a classifier error drops only the suggestion.
        return []
    if not verdict.record or verdict.kind not in ("note", "doc"):
        return []
    title = verdict.title or "the decision behind this commit"
    return [
        f"This commit encodes a durable {verdict.kind} ({verdict.reasoning}) — capture it:",
        *record_command(verdict.kind, title, verdict.when, verdict.area, mcp=mcp_active(evt)),
    ]


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
def nudge_commit_record(evt: PostToolUseEvent) -> HookResult | None:
    """After a commit, remind to link to its task and route any durable decision."""
    # Per-HEAD-sha dedup BEFORE any side-effect: each commit is judged once, an amend
    # (new sha) gets a fresh look, a sha-less git failure still fires the reminder.
    try:
        sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    except Exception:
        sha = ""
    if sha and not evt.ctx.s.once(sha, scope="commit"):
        return None
    mcp = mcp_active(evt)
    if mcp:
        trailer = (
            "Commit landed. Link it to its task with a `cc-task: <id>` trailer (queryable via "
            "`git log --grep`, the blame tool, and the history tool)."
        )
    else:
        trailer = (
            "Commit landed. Link it to its task with a `cc-task: <id>` trailer (queryable via "
            "`git log --grep`, `cc-notes blame <sha>`, and `cc-notes history <id>`)."
        )
    return evt.warn(
        trailer,
        *link_claimed_task(evt, mcp=mcp),
        *commit_decision(evt),
    )


@on(
    Event.PostToolUse,
    only_if=[FETCH_MERGE_COMMANDS, CcNotesAvailable()],
    # Uncapped like every other pure side-effect: reconcile+sync must run after every merge/fetch,
    # not just the first three of a session. The per-turn token still bounds the sync itself.
    max_fires=None,
    async_=True,
    tests={
        Input(command="git status"): Allow(),
        Input(command="git log --no-merges"): Allow(),
        Input(command="jj git remote list"): Allow(),
    },
)
def reconcile_after_merge(evt: PostToolUseEvent) -> None:
    """After a merge/pull/fetch, carry the merged branch's open tasks onto the branch of the repo it ran in, and sync that repo, in the background."""
    sync_targets(evt, FETCH_MERGE_COMMANDS.fires, reconcile=True)


@on(
    Event.PostToolUse,
    only_if=[CLAIM_COMMANDS, CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="cc-notes task list"): Allow(),
        Input(command="cc-notes task show abc1234"): Allow(),
        Input(command="cc-notes task claim abc --help"): Allow(),
    },
)
def nudge_claim(evt: PostToolUseEvent) -> HookResult | None:
    """After claiming/starting a task, teach lease upkeep."""
    if mcp_active(evt):
        lease = (
            "You hold a lease now. Call the task_renew tool on long silent stretches, and the "
            "task_done tool when finished. A crashed hold whose lease expired is reclaimable with "
            "the task_claim tool (steal=true)."
        )
    else:
        lease = (
            "You hold a lease now. `cc-notes task renew <id>` on long silent stretches, "
            "`cc-notes task done <id>` when finished. A crashed hold whose lease expired is "
            "reclaimable with `cc-notes task claim <id> --steal`."
        )
    return evt.warn(lease)


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
    """After a commit, a task claim, or a git/jj push (which moves only refs/heads/*), sync the cc-notes refs of the repo it ran in, in the background.

    A commit to the session repo first writes the `task link` edge onto the one task the session holds,
    so the sync that follows carries it.
    """
    if commits_to_session_repo(evt):
        link_commit(evt)
    sync_targets(evt, lambda cmd: COMMIT_COMMANDS.fires(cmd) or CLAIM_COMMANDS.fires(cmd) or PUSH_COMMANDS.fires(cmd))


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
    """After a cc-notes write (CLI subcommand or MCP tool), sync the repo it wrote in the background so the new refs reach the remote.

    An MCP write targets the event's working repo. A Bash write leg runs wherever its ``cd`` prefix and
    ``-R`` option land; the session repo syncs through its wired remotes, any other repo syncs directly,
    named in any failure.
    """
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
    """Surface, once, each background sync failure recorded since the last event."""
    if not evt.ctx.s.load(SyncFailures).by_target:
        return None
    with evt.ctx.s[SyncFailures].mutate() as state:
        failed, state.by_target = list(state.by_target.values()), {}
    return evt.warn(*failed) if failed else None


def cc_notes_refs_dirty(evt: BaseHookEvent) -> bool:
    # Zero-network dirty check across every wired remote: local refs/cc-notes/* vs their fetched tracking
    # copies under refs/cc-notes-sync/<remote>/* (the fetchspec maps them by suffix). Dirty when any wired
    # remote is missing a local suffix or holds a differing sha. No local refs (or a git failure) reads
    # clean; local refs with zero wired remotes read dirty (nothing tracks them). A remote-ahead
    # tracking-only ref is no push moment, so it never forces dirty.
    out = evt.ctx.git("for-each-ref", "--format=%(refname) %(objectname)", _LOCAL_REF_PREFIX, _TRACKING_NS)
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
    remotes = wired_remotes(evt)
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
    """SessionEnd backstop: push cc-notes refs a write-only session never synced, when local diverges from tracking."""
    if cc_notes_refs_dirty(evt):
        do_sync(evt)


@on(
    Event.PostToolUse,
    only_if=[Tool("TaskCreate"), ManyNativeTasks(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(
            tool="TaskCreate",
            tasks=[{"id": str(i), "subject": f"t{i}", "status": "pending"} for i in range(NATIVE_TASK_MIRROR_THRESHOLD)],
        ): Warn(pattern="Native tasks vanish at session end"),
        Input(
            tool="TaskCreate",
            tasks=[{"id": "1", "subject": "t1", "status": "in_progress"}],
        ): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def nudge_mirror_native_tasks(evt: PostToolUseEvent) -> HookResult | None:
    """When the native task list grows, mirror its durable/cross-agent items into cc-notes — MCP tools when active."""
    if mcp_active(evt):
        return evt.warn(
            "Your native task list is getting large. Native tasks vanish at session end and are private "
            "to this agent — mirror any that are durable or cross-agent into the task_add tool with criteria "
            "(backlog=true if it's shared work anyone can claim). Keep the purely in-session steps as native todos."
        )
    return evt.warn(
        "Your native task list is getting large. Native tasks vanish at session end "
        "and are private to this agent — mirror any that are durable or cross-agent "
        "into `cc-notes task add` with a `--criterion` (`--backlog` if it's shared work "
        "anyone can claim). Keep the purely in-session steps as native todos."
    )
