"""The Record/push routers that flag durable internal writes and evidence archives, plus the plan-tasks nudge."""

from __future__ import annotations

import re
from pathlib import Path, PurePosixPath

from captain_hook import (
    Allow,
    BaseHookEvent,
    CustomCondition,
    Event,
    HookResult,
    Input,
    PostToolUseEvent,
    Prompt,
    Tool,
    Warn,
    on,
)
from captain_hook.command import CommandLine, ParsedCommand
from captain_hook.state import fired_this_turn, record_fire
from pydantic import BaseModel

from .common import (
    CcNotesAvailable,
    LLM_INPUT_CAP,
    NUDGE_MAX_FIRES,
    RECORD_KINDS,
    RecordVerdict,
    in_cc_pool_memory,
    record_command,
)

# DurableInternalWrite recall vocabulary: STRONG names look durable-internal on name
# alone; WEAK names only qualify when the body carries an internal signal. PUBLISHED/
# SOURCE/SECRET are the hard exclusions — never durable-internal knowledge.
STRONG_INTERNAL_GLOBS = ("*_VERIFICATION.md", "*HANDOFF*.md", "*STATUS*.md", "*-handoff.md", "HANDOFF.md", "STATUS.md", "NOTES.md")
WEAK_INTERNAL_GLOBS = ("TODO.md", "*-notes.md", "runbook*.md", "runbook*", "scratch*.md")
PUBLISHED_GLOBS = ("README*", "CHANGELOG*", "LICENSE*", "CONTRIBUTING*", "*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg")
PUBLISHED_DIRS = ("docs/",)
SECRET_GLOBS = (".env", ".env.*", "*.env", "*secret*", "*credential*", "*.key", "*.pem")
SOURCE_GLOBS = (
    "*.py", "*.pyi", "*.ts", "*.tsx", "*.js", "*.mjs", "*.cjs", "*.jsx",
    "*.go", "*.rs", "*.java", "*.c", "*.h", "*.cpp", "*.rb", "*.sh",
    "*.json", "*.toml", "*.yaml", "*.yml",
)

# The leading `(?im)` makes the pattern case-insensitive and multiline.
INTERNAL_BODY_RE = r"(?im)^\s*- \[ \]|\b(handoff|hand-off|remaining|next steps|runbook|verification|status|decisions?)\b"

# EvidenceArchive vocabulary: machine-generated artifacts belong on a cc-notes log entry as
# `--attach` (git-lfs) attachments, never as bytes in git history. TRANSFER programs move
# them; RUN_OUTPUT roots mark a source as run output; EXEMPT destinations are trees where
# landing them is fine (temp, scratch, fixtures, git internals).
TRANSFER_PROGRAMS = frozenset({"cp", "mv", "rsync"})
EVIDENCE_SUFFIXES = frozenset({".log", ".panic", ".dump", ".core", ".trace", ".crash"})
# Unambiguous dump-dir names only. A source-code package named `crash`/`panic` (singular)
# is not an evidence dir, so those ambiguous segments are out — a `.go` under internal/crash/
# no longer counts. These signal a machine-generated origin wherever they sit on a path.
EVIDENCE_DIR_SEGMENTS = frozenset({"panics", "crashes", "cores", "coredumps", "diagnosticreports"})
# rsync flags whose value is a SEPARATE following token — consume that token so an exclusion
# glob (`--exclude '*.log'`) or a `results` value can't be read as a source. Equals-form
# (`--exclude=*.log`) is one `-`-prefixed token, already skipped. cp/mv on macOS take no such
# two-token flags, so this is rsync-only.
RSYNC_VALUE_FLAGS = frozenset(
    {
        "--exclude", "--exclude-from", "--include", "--include-from", "--filter",
        "--files-from", "-e", "--rsh", "--chmod", "--log-file", "--compare-dest",
        "--copy-dest", "--link-dest", "--backup-dir", "--partial-dir", "--temp-dir",
        "-T", "--out-format", "--password-file",
    }
)
RUN_OUTPUT_PREFIXES = ("/tmp", "/private/tmp", "/var", "/private/var")
EXEMPT_DEST_PREFIXES = (*RUN_OUTPUT_PREFIXES, "/dev", "/proc", "/sys")
# Conventional build-output dirs join the exempt trees: landing artifacts there is a build,
# not evidence archival (gitignore parsing is out of scope).
EXEMPT_DEST_SEGMENTS = frozenset(
    {
        ".git", "testdata", "fixtures", "node_modules", "scratchpad", "__pycache__",
        "bin", "dist", "build", "out", "target",
    }
)
EVIDENCE_TRIPWIRE_BYTES = 1 << 20


class DurableInternalWrite(CustomCondition):
    """Matches a write of durable INTERNAL knowledge that belongs out of the public tree."""

    def check(self, evt: BaseHookEvent) -> bool:
        file = evt.file
        if file is None:
            return False
        # The mirror owns the cc-pool tree — guard first so a memory slug literally
        # named "handoff" can't leak into the STRONG branch.
        if in_cc_pool_memory(Path(str(file))):
            return False
        # A `memory/` write of any extension is durable-internal, unless secret-shaped.
        if file.under("memory/") and not file.matches(*SECRET_GLOBS):
            return True
        if file.suffix.lower() != ".md":
            return False
        if file.matches(*SECRET_GLOBS):
            return False
        if file.matches(*PUBLISHED_GLOBS) or file.under(*PUBLISHED_DIRS):
            return False
        if file.matches(*SOURCE_GLOBS):
            return False
        if file.matches(*STRONG_INTERNAL_GLOBS):
            return True
        if file.matches(*WEAK_INTERNAL_GLOBS):
            return bool(evt.content) and bool(re.search(INTERNAL_BODY_RE, evt.content))
        return False


RECORD_ROUTER_SYSTEM = (
    "You are a precision filter. A cheap static rule has already flagged a file an agent just "
    "wrote as POSSIBLY durable internal knowledge — content that belongs in cc-notes (git objects "
    "on refs/cc-notes/*, synced with the repo but never in the working tree) rather than as a loose "
    "file in the public tree. The static rule over-selects on purpose; your job is to confirm the "
    "write is genuinely durable internal knowledge and, when it is, route it to the right cc-notes "
    "record.\n"
    "\n"
    "Set record=false when the file is genuinely human-facing or published project documentation "
    "that belongs in the repo tree — a README, a user guide, a tutorial, API reference, a released "
    "changelog, a blog post, release notes, or a spec written for people — or when it is throwaway "
    "scratch with no durable value. Machine-generated evidence preserved for the record (a crash "
    "log, a panic dump, captured run output) is NOT scratch — it records as a log with the artifact "
    "attached. When it could plausibly be either, answer record=false. Only a clear case records.\n"
    "\n"
    "When record=true, choose exactly one kind:\n"
    "- note: a single durable fact or decision — one verifiable claim about the code (e.g. 'retry "
    "backoff caps at 30s because the server drops connections past it').\n"
    "- doc: living, long-form guidance for the next agent that you keep fresh — a handoff brief, a "
    "runbook, design rationale for an in-flight change, an investigation write-up. A doc is "
    "re-verified, drifts when the code moves, and carries a 'read this when…' trigger.\n"
    "- log: an immutable, append-only chronology — an incident timeline, a rollout log, a debugging "
    "session — or an evidence archive: machine-generated artifacts belong on log entries as "
    "`--attach <file>` attachments, never as files in the tree. Its value is the running record "
    "itself; entries are never edited and it has no freshness lifecycle.\n"
    "- task: actionable work still to be done — a TODO or checklist of follow-ups.\n"
    "\n"
    "doc vs log is the subtle call: choose doc when the content is guidance you would keep current, "
    "log when it is a dated record of what happened that you would only ever append to.\n"
    "\n"
    "When record=true also return: title — a short title; when — for a doc, the free-text 'read "
    "this when…' trigger (leave empty for other kinds); area — the repo directory the record is "
    "about (e.g. internal/api), or '.' if unclear; reasoning — one line explaining the call."
)


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit|MultiEdit"), DurableInternalWrite(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        # Each case is silent under the default call_llm stub (record=False): a rejected
        # path never reaches the LLM, a matched path's stubbed verdict doesn't record. The
        # firing / kind-routing split needs a record=True stub, in tests/test_cc_notes.py.
        Input(tool="Write", file="HANDOFF.md", content="## Status\nHandoff\n## Remaining\n- [ ] x\n"): Allow(),
        Input(tool="Write", file="README.md", content="# Readme\nsome prose\n"): Allow(),
        Input(tool="Write", file="src/foo.ts", content="export const x = 1\n"): Allow(),
        Input(tool="Write", file=".env", content="API_KEY=secret\n"): Allow(),
        Input(tool="Write", file="/n/.cc-pool/p/memory/x.md", content="---\ntype: feedback\n---\nbody\n"): Allow(),
        Input(tool="Read", file="HANDOFF.md"): Allow(),
    },
)
def nudge_record_durable(evt: PostToolUseEvent) -> HookResult | None:
    """Record-route a write the static gate flagged as possibly durable internal knowledge."""
    if fired_this_turn(evt):
        return None
    prompt = (
        Prompt()
        .system(RECORD_ROUTER_SYSTEM)
        .context("path", str(evt.file))
        .context("content", (evt.content or "")[:LLM_INPUT_CAP])
        .ask("Does this belong in cc-notes, and if so as which record (note/doc/log/task)?")
    )
    try:
        verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    except Exception:
        # Fail closed: a classifier error must never crash a nudge fire — the pack only warns.
        return None
    if not verdict.record or verdict.kind not in RECORD_KINDS:
        return None
    record_fire(evt)
    title = verdict.title or (evt.file.stem if evt.file else "untitled")
    return evt.warn(
        f"{evt.file} reads like durable {verdict.kind} content for cc-notes, not a loose file in "
        f"the working tree ({verdict.reasoning}). Record it, then delete the loose file:",
        *record_command(verdict.kind, title, verdict.when, verdict.area),
        "(Don't put secrets in cc-notes — the refs sync to the remote.)",
    )


def under_prefix(path: str, prefixes: tuple[str, ...]) -> bool:
    return any(path == p or path.startswith(p + "/") for p in prefixes)


def run_output_source(path: str) -> bool:
    return under_prefix(path, RUN_OUTPUT_PREFIXES) or "results" in PurePosixPath(path).parts


def evidence_path(path: str) -> bool:
    p = PurePosixPath(path)
    return p.suffix.lower() in EVIDENCE_SUFFIXES or any(part.lower() in EVIDENCE_DIR_SEGMENTS for part in p.parts)


def durable_dest(path: str) -> bool:
    # A remote (host:path) or variable-rooted destination is unknowable statically; only a
    # concrete local path outside temp/scratch/fixture/build trees, and sitting inside a git
    # worktree, counts as a durable tracked tree.
    if path.startswith("$") or ":" in path.split("/", 1)[0]:
        return False
    if under_prefix(path, EXEMPT_DEST_PREFIXES):
        return False
    if any(part.lower() in EXEMPT_DEST_SEGMENTS for part in PurePosixPath(path).parts):
        return False
    return in_git_worktree(path)


def in_git_worktree(path: str) -> bool:
    # A dest outside any repo isn't a tracked tree, so evidence landing there carries no
    # git-history hazard and stays silent. Walk up from the dest for a `.git` dir OR file
    # (a worktree/submodule `.git` is a file); a not-yet-created dest just contributes a
    # miss and the walk continues to its nearest existing ancestor. os.path stat walk only,
    # no subprocess — `~` is expanded so `~/Downloads` resolves to the real home path.
    resolved = Path(path).expanduser().resolve()
    return any((d / ".git").exists() for d in (resolved, *resolved.parents))


def transfer_operands(cmd: ParsedCommand) -> list[str]:
    # Non-flag operands of a transfer command. rsync alone has space-separated value flags
    # (`--exclude PAT`, `-e ssh`, …) whose value token is not a source/dest; consume it so an
    # exclusion glob or a `results` value can't masquerade as run output. cp/mv have none.
    if cmd.program != "rsync":
        return [a for a in cmd.args if not a.startswith("-")]
    operands: list[str] = []
    skip_value = False
    for a in cmd.args:
        if skip_value:
            skip_value = False
            continue
        if a.startswith("-"):
            skip_value = a in RSYNC_VALUE_FLAGS
            continue
        operands.append(a)
    return operands


def evidence_transfers(line: CommandLine) -> list[str]:
    """Destinations of cp/mv/rsync legs that land run output or evidence in a durable tree.

    A leg qualifies only when a source itself looks like run output (a temp/var root or a
    ``results`` segment) or when any path carries an evidence suffix or a dump-dir segment.
    Bulk (``-R``) and multi-source no longer qualify on their own — copying a bulk of source
    files is not evidence. A same-parent rename/move (``mv app.log app.log.1``) lands no new
    evidence and is exempt, as is any leg whose destination isn't a durable tracked tree.
    """
    dests: list[str] = []
    for cmd in line.commands:
        if cmd.program not in TRANSFER_PROGRAMS:
            continue
        paths = transfer_operands(cmd)
        if len(paths) < 2 or not durable_dest(paths[-1]):
            continue
        sources, dest = paths[:-1], paths[-1]
        dest_parent = PurePosixPath(dest).parent
        if all(PurePosixPath(s).parent == dest_parent for s in sources):
            continue
        if any(run_output_source(s) for s in sources) or any(evidence_path(p) for p in paths):
            dests.append(dest)
    return dests


class EvidenceArchive(CustomCondition):
    """Matches machine-generated evidence landing in a durable tree — a Bash cp/mv/rsync of run output, or a Write/Edit of an evidence-suffixed file."""

    def check(self, evt: BaseHookEvent) -> bool:
        if (line := evt.command_line) is not None:
            return bool(evidence_transfers(line))
        file = evt.file
        return file is not None and file.suffix.lower() in EVIDENCE_SUFFIXES and durable_dest(str(file))


def tree_bytes(path: Path) -> int:
    # Best-effort stat of what actually landed; a missing or unreadable path counts 0 —
    # the tripwire only ever strengthens wording, so failing small is safe.
    try:
        if path.is_file():
            return path.stat().st_size
        if not path.is_dir():
            return 0
        total = 0
        for child in path.rglob("*"):
            if child.is_file():
                total += child.stat().st_size
                if total > EVIDENCE_TRIPWIRE_BYTES:
                    return total
        return total
    except OSError:
        return 0


def evidence_payload_bytes(evt: PostToolUseEvent) -> int:
    if (line := evt.command_line) is not None:
        return sum(tree_bytes(Path(dest)) for dest in evidence_transfers(line))
    return len((evt.content or "").encode())


@on(
    Event.PostToolUse,
    only_if=[Tool("Bash|Write|Edit|MultiEdit"), EvidenceArchive(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        # The archetype miss: run output cp -R'd into the repo's docs/ tree via Bash — dest in
        # the tracked tree (relative to the repo cwd), non-.md payload, under docs/. The
        # other-repo-with-.git variant needs a real second repo, proven in tests/test_cc_notes.py.
        Input(
            command="mkdir -p docs/reports/assets/vm-repro && "
            "cp -R /tmp/fusekit-vm/results/run-42 docs/reports/assets/vm-repro/phase2-forced-unmount"
        ): Warn(pattern="--attach"),
        Input(command="mv crash-4821.panic docs/reports/crash-4821.panic"): Warn(pattern="cc-notes log add"),
        Input(command="rsync -av /var/log/fusekit/ evidence/latest/"): Warn(pattern="cc-notes sync"),
        Input(tool="Write", file="docs/reports/soak-test.log", content="I0621 vm boot ok\n"): Warn(pattern="--attach"),
        # Benign neighbors that must stay silent.
        Input(command="cp /tmp/run/out.log /tmp/keep/out.log"): Allow(),  # entirely inside /tmp
        Input(command="cp fixtures/batch.json internal/lfs/testdata/batch.json"): Allow(),  # fixture into testdata/
        Input(command="mv .git/objects/tmp_pack .git/objects/pack/pack-1.pack"): Allow(),  # git internals
        Input(command="cp README.md docs/index.md"): Allow(),  # no run-output or evidence signal
        Input(command="cp -R docs/assets docs/assets-v2"): Allow(),  # same-parent copy, no evidence signal
        Input(command="cp -R /Users/y/internal/store /Users/y/internal/store.bak"): Allow(),  # absolute bulk, no run-output signal
        Input(command="cp /tmp/build/cc-notes /usr/local/bin/"): Allow(),  # build install into bin/ (exempt segment)
        Input(command="rsync -av --exclude '*.log' src/ docs/mirror/"): Allow(),  # exclude glob is a flag value, not a source
        Input(command="go build -o bin/cc-notes ./cmd/cc-notes"): Allow(),  # build artifact, not a transfer
        Input(tool="Write", file="docs/guide.md", content="# Guide\n"): Allow(),  # .md docs stay with writing-docs
    },
)
def nudge_record_evidence(evt: PostToolUseEvent) -> HookResult | None:
    """Route machine-generated evidence landing in a durable tree to a log entry with attachments."""
    if fired_this_turn(evt):
        return None
    record_fire(evt)
    landed = "This copy lands" if evt.command_line is not None else f"{evt.file} lands"
    weight = (
        " >1MB of machine-generated content in the tracked tree — git history is forever; an LFS attachment is one flag."
        if evidence_payload_bytes(evt) > EVIDENCE_TRIPWIRE_BYTES
        else " machine-generated evidence in the tracked tree, where git history carries it forever."
    )
    return evt.warn(
        landed + weight + " Record a cc-notes log entry with the artifacts attached instead:",
        'cc-notes log add "<what ran>"',
        'cc-notes log append <id> -m "<verdict>" --attach <file>   # repeat --attach per artifact',
        "Attachments never touch the checkout, and only `cc-notes sync` uploads their content "
        "(a plain `git push` moves refs without it). Then delete the copied files.",
    )


PLAN_TASKS_SYSTEM = (
    "An agent just had a plan approved. Extract only the work items that are DURABLE — work that "
    "outlives this session, or that another agent might pick up or coordinate on — and worth tracking "
    "as a cc-notes task. Skip the moment-to-moment implementation steps the agent does right now and "
    "checks off as it goes; those belong in the private native todo list, not cc-notes.\n"
    "\n"
    "Prefer a few high-value items over a long list, and return an empty list when the plan is "
    "throwaway or entirely in-session mechanics. For each item set title (a short imperative) and "
    "shared=true when any agent could pick it up — it belongs on the shared backlog — rather than "
    "being tied to this agent's current branch."
)

# The canonical native-vs-durable teaching, in the same terms as the README table and SKILL.md.
PLAN_TEACH = (
    "Plan approved. Native TaskCreate/TaskUpdate is your private scratchpad — it vanishes at session "
    "end. Durable work that outlives the session or coordinates agents goes in `cc-notes task`: "
    "`--backlog` for shared work any agent can claim, plain `cc-notes task add` for your branch. "
    "(A decision or durable fact is a `cc-notes note add`; living guidance for the next agent, with a "
    "`--when` read-trigger, is a `cc-notes doc add`; an append-only chronology whose entries are never "
    "edited is a `cc-notes log add`.)"
)


class PlanTask(BaseModel):
    """One durable work item the plan router lifts out of an approved plan."""

    title: str = ""
    shared: bool = False


class PlanTasks(BaseModel):
    """The plan router's verdict: the few durable work items worth a cc-notes task.

    Defaults to an empty list so a degenerate parse or a throwaway plan suggests
    nothing — the deterministic teach still stands on its own.
    """

    tasks: list[PlanTask] = []


def plan_text(evt: PostToolUseEvent) -> str | None:
    ti = evt._tool_input
    path = ti.get("planFilePath")
    if isinstance(path, str) and path:
        try:
            text = Path(path).read_text(encoding="utf-8").strip()
        except OSError:
            text = ""
        if text:
            return text
    inline = ti.get("plan")
    return inline.strip() if isinstance(inline, str) and inline.strip() else None


def plan_task_commands(evt: PostToolUseEvent, text: str | None) -> list[str]:
    if not text:
        return []
    prompt = (
        Prompt()
        .system(PLAN_TASKS_SYSTEM)
        .context("plan", text[:LLM_INPUT_CAP])
        .ask("Which few items from this plan are durable work worth a cc-notes task? None if it is all in-session steps.")
    )
    try:
        extracted = evt.ctx.call_llm(prompt, response_model=PlanTasks, model="small", agent=False, transcript=False)
    except Exception:
        return []
    commands = []
    for task in extracted.tasks[:5]:
        title = task.title.strip()
        if title:
            commands.append(f'cc-notes task add "{title}"' + (" --backlog" if task.shared else ""))
    return commands


@on(
    Event.PostToolUse,
    only_if=[Tool("ExitPlanMode"), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="ExitPlanMode"): Warn(pattern="cc-notes task add"),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def nudge_plan_tasks(evt: PostToolUseEvent) -> HookResult | None:
    """On plan approval, teach the native-vs-durable line and route the plan's durable items to tasks."""
    text = plan_text(evt)
    path = evt._tool_input.get("planFilePath")
    if isinstance(path, str) and path and not evt.ctx.s.once(path, scope="plan"):
        return None
    lines = [PLAN_TEACH]
    if commands := plan_task_commands(evt, text):
        lines.append("These items from your plan look like durable work — capture them:")
        lines.extend(commands)
    return evt.warn(*lines)
