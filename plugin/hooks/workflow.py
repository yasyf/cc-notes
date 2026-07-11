"""Commit/claim/merge workflow nudges with auto-sync and auto-reconcile side-effects."""

from __future__ import annotations

import subprocess

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
    nudge,
    on,
)
from cc_transcript.command import Command

from .common import (
    CcNotesAvailable,
    ManyNativeTasks,
    MCP_TOOL_PREFIX,
    NATIVE_TASK_MIRROR_THRESHOLD,
    NUDGE_MAX_FIRES,
    RecordVerdict,
    mcp_active,
    record_command,
    run_cc_notes,
)

# Flags that turn an otherwise-matched leg into a no-op the pack must not react to: a dry run
# publishes/writes nothing, a help/usage invocation runs nothing. `-n` is git push's --dry-run short
# form, but `git commit -n` is --no-verify, so the -n form is scoped to the push family alone.
DRY_RUN_FLAGS = frozenset({"--dry-run"})
PUSH_DRY_RUN_FLAGS = DRY_RUN_FLAGS | {"-n"}
HELP_FLAGS = frozenset({"--help", "-h"})
_NULLIFYING_FLAGS = HELP_FLAGS | DRY_RUN_FLAGS


class CommandFamily(CustomCondition):
    """Matches when any leg of the command line runs one of ``prefixes`` as a raw-argv prefix and that
    leg carries no effect-nullifying flag.

    Mirrors ``Runs`` — a literal argv prefix, matched against any leg of a compound line, so a quoted
    mention (``echo "git push"``) and a wrapper/flag-interleaved form (``git --no-pager commit``,
    ``sudo git push``) both miss — then drops a matched leg whose argv carries an ``exclude`` flag (a
    ``--dry-run`` push publishes nothing; a ``--help`` invocation runs nothing). Never widen to regex.
    """

    def __init__(self, prefixes: tuple[tuple[str, ...], ...], exclude: frozenset[str] = frozenset()) -> None:
        self.prefixes = prefixes
        self.exclude = exclude

    def check(self, evt: BaseHookEvent) -> bool:
        line = evt.command_line
        return line is not None and any(self._fires(cmd) for cmd in line.commands)

    def _fires(self, cmd: Command) -> bool:
        argv = cmd.argv
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
    "log": frozenset({"add", "append", "edit", "rm"}),
    "task": frozenset({"add", "edit", "done", "cancel", "claim", "start", "renew", "comment", "dep", "undep", "validate"}),
    "project": frozenset({"add", "edit", "comment", "complete", "cancel", "archive"}),
    "sprint": frozenset({"add", "edit", "comment", "complete", "cancel", "activate"}),
    "runbook": frozenset({"add", "edit", "comment", "activate", "archive"}),
}

# Two-level nouns: (noun, group) -> the read subcommands of that group. Any other sub writes (fail
# open — an unrecognized sub costs one idempotent sync). `runbook step` mutations (add/rm/edit/move)
# and `runbook run` lifecycle (start/done/skip/fail/finish) all write; only their listings/show read.
CC_NOTES_WRITE_SUBGROUP_READS: dict[tuple[str, str], frozenset[str]] = {
    ("task", "criterion"): frozenset({"list"}),
    ("runbook", "step"): frozenset({"list"}),
    ("runbook", "run"): frozenset({"list", "show"}),
}

# cc-notes MCP write surface (internal/mcpserver/tools_*.go): a deny-list of the READ tool suffixes, so
# the matcher fails OPEN — a future tool triggers one harmless idempotent sync until it is listed here.
MCP_READ_TOOLS = frozenset(
    {
        "note_list", "note_show", "note_search", "note_review",
        "doc_list", "doc_show", "doc_search", "doc_review",
        "log_list", "log_show", "log_search",
        "task_list", "task_show", "task_ready", "task_backlog", "task_stale", "task_archived", "task_criterion_list",
        "project_list", "project_show",
        "sprint_list", "sprint_show",
        "runbook_list", "runbook_show",
        "status", "blame", "history", "relevant", "attachment_get", "attachment_path", "sync",
    }
)


def is_cc_notes_write(cmd: Command) -> bool:
    # A parsed cc-notes / `ccn` leg that mutates state: a (noun, verb) in the write table, a bare
    # `reconcile`, or a subgroup sub that isn't a read. A help or dry-run leg writes nothing.
    if cmd.program not in ("cc-notes", "ccn") or not cmd.args:
        return False
    if not _NULLIFYING_FLAGS.isdisjoint(cmd.args):
        return False
    noun = cmd.args[0]
    if noun == "reconcile":
        return True
    verb = cmd.args[1] if len(cmd.args) > 1 else ""
    if (reads := CC_NOTES_WRITE_SUBGROUP_READS.get((noun, verb))) is not None:
        sub = cmd.args[2] if len(cmd.args) > 2 else ""
        return bool(sub) and sub not in reads
    return verb in CC_NOTES_WRITE_VERBS.get(noun, frozenset())


class CcNotesCliWrite(CustomCondition):
    """Matches a Bash cc-notes subcommand that writes refs/cc-notes/* (a state change or reconcile)."""

    def check(self, evt: BaseHookEvent) -> bool:
        line = evt.command_line
        return line is not None and any(is_cc_notes_write(cmd) for cmd in line.commands)


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


def should_autosync(evt: PostToolUseEvent) -> bool:
    # At most one sync per turn even when commit+claim co-occur. A scoped once-key,
    # isolated from the record router's shared fired_this_turn slot and the per-sha/per-plan once scopes.
    return evt.ctx.s.once(str(len(evt.ctx.t) - len(evt.ctx.turn)), scope="autosync")


def do_sync(evt: BaseHookEvent) -> str | None:
    # call_cli(throw=False) discards stderr, so throw and inspect it: a no-remote/offline repo is
    # benign (silent); a real push rejection must surface or the agent believes its refs shipped.
    try:
        evt.ctx.call_cli(["cc-notes", "sync"], timeout=15)
    except subprocess.CalledProcessError as e:
        return None if "remote not configured" in (e.stderr or "") else "cc-notes sync failed — run `cc-notes sync` to retry."
    except (OSError, subprocess.SubprocessError):
        return None  # timeout / missing-or-unexecutable binary: silent (FileNotFoundError is an OSError, not SubprocessError)
    return "Synced cc-notes refs."


def auto_sync(evt: PostToolUseEvent) -> str | None:
    return do_sync(evt) if should_autosync(evt) else None


def auto_reconcile(evt: PostToolUseEvent) -> str | None:
    # Reconcile is local + idempotent (run_cc_notes is fail-closed). A detached HEAD (the colocated-jj
    # norm, exactly the state `jj git fetch` targets) or a fail-closed reconcile can't carry tasks onto a
    # branch, so it falls back to a plain auto_sync — the fetched refs still ship. On success the sync
    # rides the same per-turn token as every other trigger (via auto_sync), so a commit-then-merge turn
    # syncs once; the reconcile still ran, and do_sync's own line (success confirm or fail-closed retry
    # warn) rides along, so a failed push is never swallowed as "synced".
    branch = (evt.ctx.git("rev-parse", "--abbrev-ref", "HEAD") or "").strip()
    if not branch or branch == "HEAD" or run_cc_notes(evt, "reconcile", "--into", branch) is None:
        return auto_sync(evt)
    reconciled = f"Reconciled merged tasks onto {branch}."
    return f"{reconciled} {synced}" if (synced := auto_sync(evt)) else reconciled


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
    """After a commit, remind to link to its task, route any durable decision, and sync the new refs."""
    # Per-HEAD-sha dedup BEFORE any side-effect: each commit is judged once, an amend
    # (new sha) gets a fresh look, a sha-less git failure still fires the reminder.
    try:
        sha = (evt.ctx.git("rev-parse", "HEAD") or "").strip()
    except Exception:
        sha = ""
    if sha and not evt.ctx.s.once(sha, scope="commit"):
        return None
    if mcp_active(evt):
        link = (
            "Commit landed. Link it to its task with a `cc-task: <id>` trailer (queryable via "
            "`git log --grep`, the blame tool, and the history tool)."
        )
    else:
        link = (
            "Commit landed. Link it to its task with a `cc-task: <id>` trailer (queryable via "
            "`git log --grep`, `cc-notes blame <sha>`, and `cc-notes history <id>`)."
        )
    return evt.warn(
        link,
        *commit_decision(evt),
        *([line] if (line := auto_sync(evt)) else []),
    )


@on(
    Event.PostToolUse,
    only_if=[FETCH_MERGE_COMMANDS, CcNotesAvailable()],
    # Uncapped like every other pure side-effect: reconcile+sync must run after every merge/fetch,
    # not just the first three of a session. The per-turn token still bounds the sync itself.
    max_fires=None,
    tests={
        Input(command="git status"): Allow(),
        Input(command="git log --no-merges"): Allow(),
        Input(command="jj git remote list"): Allow(),
    },
)
def reconcile_after_merge(evt: PostToolUseEvent) -> HookResult | None:
    """After a merge/pull, carry the merged branch's open tasks onto this branch and sync."""
    return evt.warn(line) if (line := auto_reconcile(evt)) else None


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
    """After claiming/starting a task, teach lease upkeep and sync the new claim."""
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
    return evt.warn(
        lease,
        *([line] if (line := auto_sync(evt)) else []),
    )


@on(
    Event.PostToolUse,
    only_if=[PUSH_COMMANDS, CcNotesAvailable()],
    max_fires=None,
    tests={
        Input(command="git status"): Allow(),
        Input(command="git log --grep 'git push'"): Allow(),
        Input(command="echo 'jj git push'"): Allow(),
        Input(command="jj rebase -d main"): Allow(),
        Input(command="git push --dry-run"): Allow(),
        Input(command="git push -n"): Allow(),
    },
)
def sync_after_push(evt: PostToolUseEvent) -> HookResult | None:
    """After a git/jj push — which moves only refs/heads/* — sync cc-notes refs and attachment content."""
    return evt.warn(line) if (line := auto_sync(evt)) else None


@on(
    Event.PostToolUse,
    only_if=[Or(CcNotesCliWrite(), CcNotesMcpWrite()), CcNotesAvailable()],
    max_fires=None,
    tests={
        Input(command="cc-notes note list --json"): Allow(),
        Input(command="cc-notes task criterion list abc"): Allow(),
        Input(command="cc-notes runbook run show abc"): Allow(),
        Input(command="echo 'cc-notes note add'"): Allow(),
        Input(command="cc-notes note add x --help"): Allow(),
        Input(command="cc-notes reconcile --dry-run"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_list"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__sync"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__runbook_list"): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def sync_after_record_write(evt: PostToolUseEvent) -> HookResult | None:
    """After a cc-notes write (CLI subcommand or MCP tool), sync so the new refs reach the remote."""
    return evt.warn(line) if (line := auto_sync(evt)) else None


_LOCAL_REF_PREFIX = "refs/cc-notes/"
_TRACKING_REF_PREFIX = "refs/cc-notes-sync/origin/"


def cc_notes_refs_dirty(evt: BaseHookEvent) -> bool:
    # Zero-network dirty check: local refs/cc-notes/* vs their fetched tracking copies under
    # refs/cc-notes-sync/origin/* (the fetchspec maps them byte-for-byte by suffix). Dirty when a local
    # ref is missing from tracking or its sha differs; a tracking-only ref (remote ahead) is no push
    # moment, so it doesn't count. No local refs or a git failure reads clean, staying silent.
    out = evt.ctx.git("for-each-ref", "--format=%(refname) %(objectname)", _LOCAL_REF_PREFIX, _TRACKING_REF_PREFIX)
    if not out:
        return False
    local: dict[str, str] = {}
    tracking: dict[str, str] = {}
    for row in out.splitlines():
        refname, _, oid = row.partition(" ")
        if refname.startswith(_LOCAL_REF_PREFIX):
            local[refname[len(_LOCAL_REF_PREFIX) :]] = oid
        elif refname.startswith(_TRACKING_REF_PREFIX):
            tracking[refname[len(_TRACKING_REF_PREFIX) :]] = oid
    return any(tracking.get(suffix) != oid for suffix, oid in local.items())


@on(Event.SessionEnd, only_if=[CcNotesAvailable()], async_=True)
def sync_at_session_end(evt: SessionEndEvent) -> None:
    """SessionEnd backstop: push cc-notes refs a write-only session never synced, when local diverges from tracking."""
    if cc_notes_refs_dirty(evt):
        do_sync(evt)  # output ignored by the harness; do_sync's silent-vs-warn taxonomy is already correct


nudge(
    "Your native task list is getting large. Native tasks vanish at session end "
    "and are private to this agent — mirror any that are durable or cross-agent "
    "into `cc-notes task add` with a `--criterion` (`--backlog` if it's shared work "
    "anyone can claim). Keep the purely in-session steps as native todos.",
    only_if=[Tool("TaskCreate"), ManyNativeTasks(), CcNotesAvailable()],
    events=Event.PostToolUse,
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(
            tool="TaskCreate",
            tasks=[{"id": str(i), "subject": f"t{i}", "status": "pending"} for i in range(NATIVE_TASK_MIRROR_THRESHOLD)],
        ): Warn(pattern="cc-notes task add"),
        Input(
            tool="TaskCreate",
            tasks=[{"id": "1", "subject": "t1", "status": "in_progress"}],
        ): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
