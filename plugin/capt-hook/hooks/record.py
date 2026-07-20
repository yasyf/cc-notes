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
    PostToolUseFailureEvent,
    Prompt,
    StopEvent,
    Tool,
    Warn,
    on,
)
from cc_transcript.command import Command, CommandLine
from captain_hook.state import fired_this_turn, record_fire
from pydantic import BaseModel, Field

from .common import (
    CcNotesAvailable,
    CcNotesMcpToolCall,
    LLM_INPUT_CAP,
    MCP_TOOL_PREFIX,
    McpActive,
    NUDGE_MAX_FIRES,
    RECORD_KINDS,
    RecordVerdict,
    in_cc_pool_memory,
    mcp_active,
    record_command,
)

# DurableInternalWrite recall vocabulary: STRONG names look durable-internal on name
# alone; WEAK names only qualify when the body carries an internal signal. PUBLISHED/
# SOURCE/SECRET are the hard exclusions — never durable-internal knowledge.
STRONG_INTERNAL_GLOBS = ("*_VERIFICATION.md", "*HANDOFF*.md", "*STATUS*.md", "*-handoff.md", "HANDOFF.md", "STATUS.md", "NOTES.md")
WEAK_INTERNAL_GLOBS = (
    "TODO.md", "*-notes.md", "runbook*.md", "runbook*", "scratch*.md", "*memo*.md", "*decision*.md",
    "*investigation*.md", "*postmortem*.md", "*rca*.md", "*root-cause*.md", "*incident*.md",
)
PUBLISHED_GLOBS = ("README*", "CHANGELOG*", "LICENSE*", "CONTRIBUTING*", "*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg")
PUBLISHED_DIRS = ("docs/",)
SECRET_GLOBS = (".env", ".env.*", "*.env", "*secret*", "*credential*", "*.key", "*.pem")
SOURCE_GLOBS = (
    "*.py", "*.pyi", "*.ts", "*.tsx", "*.js", "*.mjs", "*.cjs", "*.jsx",
    "*.go", "*.rs", "*.java", "*.c", "*.h", "*.cpp", "*.rb", "*.sh",
    "*.json", "*.toml", "*.yaml", "*.yml",
)

# `(?im)`: case-insensitive, multiline. The investigation stems carry a trailing `\w*` so
# suffixes ("suspected", "exonerated", "postmortems") match too.
INTERNAL_BODY_RE = (
    r"(?im)^\s*- \[ \]"
    r"|\b(handoff|hand-off|remaining|next steps|runbook|verification|status|decisions?)\b"
    r"|\b(?:root cause|bisect|suspect|exonerat|falsified|postmortem)\w*"
)

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

# EphemeralRecordReference vocabulary: a durable cc-notes record must not lean on a
# purge-bound path. Each RUN_OUTPUT prefix gains a trailing "/" so "/var" can't match
# "variant"; "scratchpad" is the very segment EXEMPT_DEST_SEGMENTS treats as a fine
# landing tree, inverted here — pointing a durable record at one is the smell.
EPHEMERAL_MARKERS = (*(p + "/" for p in RUN_OUTPUT_PREFIXES), "scratchpad")
RECORD_SUBCOMMANDS = frozenset((noun, verb) for noun in ("note", "doc", "log") for verb in ("add", "edit", "append"))
# Noun-only record writes: a top-level noun that takes its prose as a bare positional, no verb
# (`cc-notes papercut "TEXT"`), mapped to its READ verbs — a first operand in that set (`papercut
# list`) is a read, not a record write. Consulted by the same record-command scanner as
# RECORD_SUBCOMMANDS, but the command prefix is one token, not a (noun, verb) pair.
RECORD_BARE_NOUNS: dict[str, frozenset[str]] = {"papercut": frozenset({"list"})}
# Flags whose value carries the record's own prose — the title/body surfaces a purge-bound
# path would betray, so their values are the only flag values worth scanning.
CONTENT_FLAGS = frozenset({"--title", "--body", "--when", "--entry"})
# Value-taking flags whose value is a label, anchor, attachment path, or model id — never record
# prose, so their value is skipped (`--label scratchpad`, `--branch eng/var/x`, `--model
# /tmp/local.gguf` must not false-fire).
SKIPPED_VALUE_FLAGS = frozenset({
    "--label", "--add-label", "--rm-label",
    "--branch", "--add-branch", "--rm-branch",
    "--path", "--add-path", "--rm-path",
    "--dir", "--add-dir", "--rm-dir",
    "--commit", "--add-commit", "--rm-commit",
    "--attach", "--rm-attachment",
    "--model",
})

# The MCP analog of the Bash ephemeral vocabulary: the record-write tools whose args carry
# prose, and the tool_input fields that prose flows through. The Bash-only condition above
# can't see MCP writes, so a sibling condition scans these fields instead.
MCP_RECORD_WRITE_TOOLS = ("note_add", "doc_add", "log_add", "log_append", "note_edit", "doc_edit", "papercut")
MCP_RECORD_WRITE_NAMES = tuple(MCP_TOOL_PREFIX + t for t in MCP_RECORD_WRITE_TOOLS)
# The prose-bearing input fields across those tools, per internal/mcpserver/tools_*.go: note/doc
# carry `body`, while log_add and log_append both carry `entry`. No write tool
# has a `message` field.
MCP_CONTENT_FIELDS = ("title", "body", "entry")


@on(
    Event.PostToolUse,
    only_if=[CcNotesMcpToolCall()],
    tests={
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_add", tool_input={"title": "x"}): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def record_mcp_active(evt: PostToolUseEvent) -> HookResult | None:
    """Flip the session MCP-active flag on any cc-notes MCP tool call — the mcp_active fast path."""
    try:
        if not evt.ctx.s.load(McpActive).active:
            evt.ctx.s[McpActive].set(McpActive(active=True))
    except Exception:
        # No session (inline tests) or a store error must never disturb the tool call.
        pass
    return None


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
    "- doc: living, long-form guidance for the next agent that you keep fresh — a handoff brief, "
    "design rationale for an in-flight change, an investigation write-up. A doc is "
    "re-verified, drifts when the code moves, and carries a 'read this when…' trigger.\n"
    "- log: an immutable, append-only chronology — an incident timeline, a rollout log, a debugging "
    "session — or an evidence archive: machine-generated artifacts belong on log entries as "
    "`--attach <file>` attachments, never as files in the tree. Its value is the running record "
    "itself; entries are never edited and it has no freshness lifecycle.\n"
    "- task: actionable work still to be done — a TODO or checklist of follow-ups.\n"
    "- papercut: a one-paragraph complaint about friction hit during the work itself — a dead-end "
    "tool call, a broken link, a misleading doc — filed to the repo-wide papercuts journal. Not a "
    "task (there is nothing to do) and not knowledge worth curating; just a logged gripe.\n"
    "- runbook: a repeatable step-by-step operational procedure meant to be re-executed — deploy "
    "steps, a release checklist, an incident-response procedure. cc-notes has a first-class "
    "runbook primitive that tracks each execution's per-step status.\n"
    "- investigation: a debugging or root-cause arc that reaches a VERDICT — a falsifiable premise "
    "(the suspected cause or symptom), a bisect/triage timeline, suspects that get cleared or "
    "confirmed, a true root cause, a fix, and its confirmation (or a falsified premise / an "
    "abandoned hunt). cc-notes has a first-class investigation primitive: an immutable premise, an "
    "append-only evidence timeline, per-suspect findings, and verdict transitions (root_caused → "
    "fixed → confirmed, or exonerated / abandoned).\n"
    "\n"
    "doc vs log is the subtle call: choose doc when the content is guidance you would keep current, "
    "log when it is a dated record of what happened that you would only ever append to. doc vs "
    "runbook splits on execution: a doc describes and explains; a runbook is an ordered procedure "
    "an agent re-executes step by step. log vs investigation splits on the verdict: a log is a "
    "verdict-less chronicle you only append to; an investigation reaches a conclusion (this was the "
    "root cause; that suspect was cleared) through its findings and status.\n"
    "\n"
    "When record=true also return: title — a short title; when — for a doc, the free-text 'read "
    "this when…' trigger (leave empty for other kinds); area — the repo directory the record is "
    "about (e.g. internal/api), or '.' if unclear; reasoning — one line explaining the call."
)

# investigation is not a RECORD_KIND (immutable premise, no --when, verdict transitions), so it
# routes through these authoring lines rather than record_command, mirroring the runbook branch.
SECRET_WARNING = "(Don't put secrets in cc-notes — the refs sync to the remote.)"


def investigation_arc_lines(mcp: bool, title: str, premise: str, *, first_evidence: str | None = None) -> list[str]:
    """The open→append→verdict authoring lines for the investigation primitive (MCP tools or CLI)."""
    ev = first_evidence or "<evidence step>"
    if mcp:
        return [
            f'investigation_open — {{"title": "{title}", "premise": "{premise}"}}',
            f'investigation_append — {{"id": "<id>", "text": "{ev}"}}   # one call per evidence step or finding',
            "verdict via investigation_root_cause / investigation_confirm (or investigation_exonerate / "
            "investigation_abandon) — never edit the title to say RESOLVED/FIXED.",
        ]
    return [
        f'cc-notes investigation open "{title}" "{premise}"',
        f'cc-notes investigation append <id> "{ev}"   # one per evidence step or finding',
        "verdict via `cc-notes investigation root-cause` / `confirm` (or `exonerate` / `abandon`) — never the title.",
    ]


def investigation_resolve_lines(mcp: bool) -> list[str]:
    """The verdict-transition lines for an investigation already open this session (MCP tools or CLI)."""
    if mcp:
        return [
            "investigation_root_cause — record the true cause (the arc moves to root_caused).",
            "investigation_fix — link the fixing commit; investigation_confirm — record the proof it holds.",
            "or investigation_exonerate / investigation_abandon if the premise was falsified or dropped.",
        ]
    return [
        'cc-notes investigation root-cause <id> "<the true cause>"',
        'cc-notes investigation fix <id> --commit <sha>   ·   cc-notes investigation confirm <id> "<proof it holds>"',
        "or `cc-notes investigation exonerate` / `abandon` if the premise was falsified or dropped.",
    ]


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
        Input(tool="Write", file="experiments/e0-gate-memo.md", content="a plan sketch, nothing durable\n"): Allow(),
        Input(tool="Write", file="docs/design-memo.md", content="## Decision\n- [ ] x\n"): Allow(),
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
        .ask("Does this belong in cc-notes, and if so as which record (note/doc/log/task/papercut/runbook/investigation)?")
    )
    try:
        verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    except Exception:
        # Fail closed: a classifier error must never crash a nudge fire — the pack only warns.
        return None
    if verdict.record and verdict.kind == "runbook":
        # Routed to the runbook primitive, not through record_command: runbooks are not a
        # RECORD_KIND (no --when, no anchors) and the suggestion is the two-verb authoring flow.
        record_fire(evt)
        title = verdict.title or (evt.file.stem if evt.file else "untitled")
        if mcp_active(evt):
            lines = (
                f'runbook_add — {{"title": "{title}", "steps": ["<step one>", "<step two>", …]}}',
                "runbook_step_add — one call per later step, in order",
            )
        else:
            lines = (
                f'cc-notes runbook add "{title}" --body - --step "<step one>" --step "<step two>"   # description on stdin',
                'cc-notes runbook step add <id> "<step text>" --command "<cmd>"   # later steps, in order',
            )
        return evt.warn(
            f"{evt.file} reads like a repeatable procedure — cc-notes has a first-class runbook "
            f"primitive with per-run step tracking ({verdict.reasoning}). Record it, then delete "
            "the loose file:",
            *lines,
            "(Don't put secrets in cc-notes — the refs sync to the remote.)",
        )
    if verdict.record and verdict.kind == "investigation":
        record_fire(evt)
        title = verdict.title or (evt.file.stem if evt.file else "untitled")
        return evt.warn(
            f"{evt.file} reads like a debugging investigation — cc-notes has a first-class "
            f"investigation primitive with an immutable premise, an append-only evidence timeline, "
            f"and verdict transitions ({verdict.reasoning}). Record it, then delete the loose file:",
            *investigation_arc_lines(mcp_active(evt), title, "<the falsifiable suspicion or symptom>"),
            SECRET_WARNING,
        )
    if not verdict.record or verdict.kind not in RECORD_KINDS:
        return None
    record_fire(evt)
    title = verdict.title or (evt.file.stem if evt.file else "untitled")
    return evt.warn(
        f"{evt.file} reads like durable {verdict.kind} content for cc-notes, not a loose file in "
        f"the working tree ({verdict.reasoning}). Record it, then delete the loose file:",
        *record_command(verdict.kind, title, verdict.when, verdict.area, mcp=mcp_active(evt)),
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


def transfer_operands(cmd: Command) -> list[str]:
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
        if line := evt.cmd.line:
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
    if line := evt.cmd.line:
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
        ): Warn(pattern="Record a cc-notes log entry"),
        Input(command="mv crash-4821.panic docs/reports/crash-4821.panic"): Warn(pattern="Record a cc-notes log entry"),
        Input(command="rsync -av /var/log/fusekit/ evidence/latest/"): Warn(pattern="cc-notes sync"),
        Input(tool="Write", file="docs/reports/soak-test.log", content="I0621 vm boot ok\n"): Warn(pattern="Record a cc-notes log entry"),
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
    landed = "This copy lands" if evt.cmd.line else f"{evt.file} lands"
    weight = (
        " >1MB of machine-generated content in the tracked tree — git history is forever; an LFS attachment is one flag."
        if evidence_payload_bytes(evt) > EVIDENCE_TRIPWIRE_BYTES
        else " machine-generated evidence in the tracked tree, where git history carries it forever."
    )
    if mcp_active(evt):
        recipe = [
            "call the log_add tool for what ran, then the log_append tool with the verdict as the "
            "entry param and each artifact via the attach param (repeatable).",
        ]
    else:
        recipe = [
            'cc-notes log add "<what ran>"',
            'cc-notes log append <id> --entry "<verdict>" --attach <file>   # repeat --attach per artifact',
        ]
    return evt.warn(
        landed + weight + " Record a cc-notes log entry with the artifacts attached instead:",
        *recipe,
        "Attachments never touch the checkout, and only `cc-notes sync` uploads their content "
        "(a plain `git push` moves refs without it). Then delete the copied files.",
    )


def record_operands(args: tuple[str, ...]) -> tuple[str, ...] | None:
    """The operands after a cc-notes record-command prefix, or None if the leg isn't a record write.

    A bare-write noun (:data:`RECORD_BARE_NOUNS`, e.g. ``papercut "TEXT"``) strips one leading
    token, unless that noun's first operand is one of its READ verbs (``papercut list`` reads the
    journal, it is not a record write); a ``(noun, verb)`` pair (:data:`RECORD_SUBCOMMANDS`) strips
    two.
    """
    if args[:1] and (reads := RECORD_BARE_NOUNS.get(args[0])) is not None:
        return None if args[1:2] and args[1] in reads else args[1:]
    if args[:2] in RECORD_SUBCOMMANDS:
        return args[2:]
    return None


def _operand_refs(operands: tuple[str, ...]) -> list[str]:
    """Purge-bound tokens among a record command's content-bearing operands.

    Inspects only the surfaces a title or body flows through — the positional words
    (the title, log-append's ID+TEXT, or a bare ``papercut`` complaint) and the values of
    :data:`CONTENT_FLAGS` (both ``--flag value`` and ``--flag=value``). Every other
    value-taking flag (:data:`SKIPPED_VALUE_FLAGS` — labels, anchors, ``--attach``,
    ``--model``) has its value skipped, so ``--label scratchpad`` or ``--model /tmp/x``
    never false-fires; an unknown flag is treated as valueless.
    """
    refs: list[str] = []
    i = 0
    while i < len(operands):
        arg = operands[i]
        token: str | None = None
        if not arg.startswith("-"):
            token, i = arg, i + 1
        elif "=" in arg:
            flag, _, value = arg.partition("=")
            token, i = (value if flag in CONTENT_FLAGS else None), i + 1
        elif arg in CONTENT_FLAGS:
            token = operands[i + 1] if i + 1 < len(operands) else None
            i += 2
        elif arg in SKIPPED_VALUE_FLAGS:
            i += 2
        else:
            i += 1
        if token is not None and any(marker in token for marker in EPHEMERAL_MARKERS):
            refs.append(token)
    return refs


def ephemeral_record_refs(line: CommandLine) -> list[str]:
    """Content-bearing tokens of a cc-notes record command that name a purge-bound path.

    Walks each ``cc-notes`` leg that is a record write (:func:`record_operands`) and collects
    the purge-bound tokens among its content-bearing operands (:func:`_operand_refs`).
    """
    refs: list[str] = []
    for cmd in line.commands:
        if cmd.program != "cc-notes":
            continue
        operands = record_operands(cmd.args)
        if operands is not None:
            refs.extend(_operand_refs(operands))
    return refs


def ephemeral_papercut(line: CommandLine) -> bool:
    """True when a firing cc-notes record leg is a ``papercut`` — its fix lines differ (text-only)."""
    return any(
        cmd.program == "cc-notes"
        and cmd.args[:1] == ("papercut",)
        and (operands := record_operands(cmd.args)) is not None
        and bool(_operand_refs(operands))
        for cmd in line.commands
    )


class EphemeralRecordReference(CustomCondition):
    """Matches a cc-notes note/doc/log/papercut record whose title or body text points at a purge-bound path (/tmp, /var, a session scratchpad)."""

    def check(self, evt: BaseHookEvent) -> bool:
        line = evt.cmd.line
        return bool(line) and bool(ephemeral_record_refs(line))


@on(
    Event.PostToolUse,
    only_if=[Tool("Bash"), EphemeralRecordReference(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command='cc-notes doc add "Handoff — full detail in session scratchpad steering-handoff.md" --when w'): Warn(pattern="purge-bound path"),
        Input(command='cc-notes note add "Fact" --body "see /private/tmp/c-1/scratch.md"'): Warn(),
        # A verb-less `papercut TEXT` whose complaint leans on a purge-bound path fires with
        # papercut-appropriate fix lines (the journal, not --checkout/--body which papercut lacks).
        Input(command='cc-notes papercut "full repro saved at /tmp/repro.md"'): Warn(pattern="papercuts journal"),
        # Benign neighbors that must stay silent.
        Input(command='cc-notes doc add "Handoff" --when w --body -'): Allow(),  # content already in the record
        Input(command="cc-notes log append abc123 --attach /tmp/out.log"): Allow(),  # attaching the file IS the fix
        Input(command='cc-notes note add "Fact" --body "content inline" --label scratchpad'): Allow(),  # --label value is not content
        Input(command='cc-notes papercut "the search tool kept returning stale results"'): Allow(),  # clean papercut prose
        Input(command="cc-notes papercut list"): Allow(),  # reading the journal is not a record write
        Input(command="cc-notes papercut list /tmp/repro.md"): Allow(),  # a read leg is never scanned, even with a purge path arg
        Input(command='cc-notes papercut --model /tmp/local.gguf "clean text"'): Allow(),  # --model value is a model id, not content
        Input(command='cc-notes papercut --model=/tmp/local.gguf "clean text"'): Allow(),  # equals form skipped the same way
        Input(command="cc-notes task list"): Allow(),  # not a record write
        Input(command="cat /tmp/scratch.md"): Allow(),  # not a cc-notes command
    },
)
def nudge_ephemeral_record_reference(evt: PostToolUseEvent) -> HookResult | None:
    """Nudge a cc-notes record that leans on a purge-bound path to carry its content in the record itself."""
    if fired_this_turn(evt):
        return None
    record_fire(evt)
    line = evt.cmd.line
    papercut = bool(line) and ephemeral_papercut(line)
    return evt.warn(EPHEMERAL_REFERENCE_LEDE, *_carry_content_fixes(mcp_active(evt), papercut=papercut))


EPHEMERAL_REFERENCE_LEDE = (
    "This cc-notes record leans on a purge-bound path (/tmp, /var, or a session scratchpad) — "
    "those are gone by the next session, so a durable record that points at one outlives its own "
    "content. Carry the content in the record itself:"
)


def _carry_content_fixes(mcp: bool, papercut: bool = False) -> list[str]:
    if papercut:
        # papercut is text-only (positional TEXT / MCP `body`) — it has no --checkout/--body/--attach
        # of its own, so route a durable artifact to the papercuts journal, which is an ordinary log.
        inline = "inline the load-bearing detail directly in the complaint text, not a path to it — a papercut is text-only."
        if mcp:
            return [inline, "to keep an artifact, attach it to the papercuts journal (an ordinary log) with log_append's attach param."]
        return [inline, "to keep an artifact, attach it durably to the papercuts journal (an ordinary log): `cc-notes log append <papercuts-journal-id> --attach <file>`."]
    if mcp:
        return [
            "the body param (note_add/doc_add) or entry param (log_append) carries the content — pass the full text there, not a path.",
            "the attach param stores an artifact file — its bytes land in the git ODB and sync with the repo.",
        ]
    return [
        "--checkout prints a prefilled buffer; write the body into it, then --apply — the body lives in the record, not a loose file.",
        "--body - reads a short body from stdin instead.",
        "--attach <file> stores an artifact — its bytes land in the git ODB and sync with the repo.",
    ]


def mcp_ephemeral_refs(evt: PostToolUseEvent) -> list[str]:
    """Content-field values of an MCP record-write tool call that name a purge-bound path.

    The MCP analog of :func:`ephemeral_record_refs`: the Bash-only condition can't see a
    record written through the ``mcp__plugin_cc-notes_cc-notes__*`` tools, so scan the
    tool_input fields (:data:`MCP_CONTENT_FIELDS`) a title or body flows through.
    """
    ti = evt._tool_input
    return [
        value
        for field in MCP_CONTENT_FIELDS
        if isinstance(value := ti.get(field), str) and any(marker in value for marker in EPHEMERAL_MARKERS)
    ]


class McpEphemeralReference(CustomCondition):
    """Matches an MCP note/doc/log/papercut record write whose title or body text points at a purge-bound path."""

    def check(self, evt: BaseHookEvent) -> bool:
        return bool(mcp_ephemeral_refs(evt))


@on(
    Event.PostToolUse,
    only_if=[Tool(*MCP_RECORD_WRITE_NAMES), McpEphemeralReference(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(
            tool="mcp__plugin_cc-notes_cc-notes__doc_add",
            tool_input={"title": "Handoff", "when": "w", "body": "full detail in session scratchpad steering-handoff.md"},
        ): Warn(pattern="attach"),
        # The papercut tool carries its complaint in the `body` field; a purge-bound path there fires
        # with papercut-appropriate fix lines (route the artifact to the journal, no CLI body/attach).
        Input(
            tool="mcp__plugin_cc-notes_cc-notes__papercut",
            tool_input={"body": "full repro saved at /tmp/repro.md"},
        ): Warn(pattern="papercuts journal"),
        # Inline content with no purge-bound path stays silent.
        Input(
            tool="mcp__plugin_cc-notes_cc-notes__note_add",
            tool_input={"title": "Fact", "body": "the backoff caps at 30s"},
        ): Allow(),
        Input(
            tool="mcp__plugin_cc-notes_cc-notes__papercut",
            tool_input={"body": "the search tool kept returning stale results"},
        ): Allow(),
    },
)
def nudge_mcp_ephemeral_reference(evt: PostToolUseEvent) -> HookResult | None:
    """Nudge an MCP record write that leans on a purge-bound path to carry its content in the record."""
    if fired_this_turn(evt):
        return None
    record_fire(evt)
    papercut = (evt.tool_name or "").endswith("papercut")
    return evt.warn(EPHEMERAL_REFERENCE_LEDE, *_carry_content_fixes(mcp=True, papercut=papercut))


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
    "`--backlog` for shared work any agent can claim, plain `cc-notes task add` for your branch; each "
    "needs a `--criterion \"<how to verify>\"` (or `--no-validation-criteria` when acceptance can't be "
    "stated). "
    "(A decision or durable fact is a `cc-notes note add`; living guidance for the next agent, with a "
    "`--when` read-trigger, is a `cc-notes doc add` — short title, and for a long body `--checkout` "
    "prints a prefilled buffer you write the guidance into and `--apply`, or `--body -` reads a short "
    "one from stdin; an append-only chronology whose entries are never edited is a `cc-notes log add`; "
    "and friction you hit along the way — a dead-end tool call, a broken link, a misleading doc — is a "
    "one-paragraph `cc-notes papercut`.)"
)

# The one-line MCP variant: routing lives in the MCP tools' own descriptions, so the teach only
# needs to point durable/shared work at the task_add tool.
PLAN_TEACH_MCP = (
    "Plan approved. Native TaskCreate/TaskUpdate is your private in-session scratchpad; durable work "
    "that outlives the session or coordinates agents goes to the task_add tool with acceptance criteria "
    "(backlog=true for shared work any agent can claim). Friction you hit along the way — a dead-end "
    "tool call, a broken link, a misleading doc — is a one-paragraph complaint to the papercut tool."
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


def plan_task_commands(evt: PostToolUseEvent, text: str | None, *, mcp: bool = False) -> list[str]:
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
        if not title:
            continue
        if mcp:
            commands.append(f'task_add tool: title="{title}", criteria=["<how to verify it is done>"]' + (", backlog=true" if task.shared else ""))
        else:
            commands.append(f'cc-notes task add "{title}" --criterion "<how to verify it is done>"' + (" --backlog" if task.shared else ""))
    return commands


@on(
    Event.PostToolUse,
    only_if=[Tool("ExitPlanMode"), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="ExitPlanMode"): Warn(pattern="Native TaskCreate/TaskUpdate is your private"),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def nudge_plan_tasks(evt: PostToolUseEvent) -> HookResult | None:
    """On plan approval, teach the native-vs-durable line and route the plan's durable items to tasks."""
    text = plan_text(evt)
    path = evt._tool_input.get("planFilePath")
    if isinstance(path, str) and path and not evt.ctx.s.once(path, scope="plan"):
        return None
    mcp = mcp_active(evt)
    lines = [PLAN_TEACH_MCP if mcp else PLAN_TEACH]
    if commands := plan_task_commands(evt, text, mcp=mcp):
        lines.append("These items from your plan look like durable work — capture them:")
        lines.extend(commands)
    return evt.warn(*lines)


INVESTIGATION_MCP_PREFIX = MCP_TOOL_PREFIX + "investigation_"
# Investigation verbs (MCP underscore / CLI hyphen), canonicalized to underscore before classifying.
INVESTIGATION_READ_VERBS = frozenset({"list", "show", "search", "history", "finding_list"})
INVESTIGATION_OPEN_VERBS = frozenset({"open", "add"})
INVESTIGATION_TERMINAL_VERBS = frozenset({"confirm", "exonerate", "abandon"})
INVESTIGATION_UNRESOLVED_VERBS = INVESTIGATION_OPEN_VERBS | frozenset({"append", "root_cause", "fix", "reopen"})

# --log-failed prints only failures; a gh run watch / ccx vcs ship is red on a failed exit or conclusion.
GH_RUN_RE = re.compile(r"\bgh\s+run\b")
GH_LOG_FAILED_RE = re.compile(r"--log-failed\b")
GH_WATCH_RE = re.compile(r"\bgh\s+run\s+watch\b")
SHIP_RE = re.compile(r"\bccx\s+vcs\s+ship\b")
# A real failed conclusion (status glyph or "completed with 'failure'"), never the bare word "failure".
GH_FAILED_CONCLUSION_RE = re.compile(
    r"(?im)^\s*[X✗✘](?:\s|$)"
    r"|completed with ['\"]?(?:failure|timed_out|cancelled|startup_failure|action_required)"
    r"|conclusion['\"]?\s*[:=]\s*['\"]?(?:failure|timed_out|cancelled|startup_failure)"
)
RUN_URL_RE = re.compile(r"https?://github\.com/\S+?/actions/runs/\d+")

# Debugging/forensics subagent shapes, verdict language in a synthesis result, and close-out language.
CI_TRIAGE_AGENT_MARKERS = ("ci-triage",)
SUBAGENT_INVESTIGATION_MARKERS = ("ci-triage", "debug", "forensic", "bug")
SUBAGENT_INVESTIGATION_RE = re.compile(
    r"(?i)\b(?:investigat|root[\s-]?cause|bisect|debug|forensic|triage|repro|suspect|regression)\w*"
)
VERDICT_LANGUAGE_RE = re.compile(r"(?i)\b(?:root[\s-]?cause|confirmed|falsified|exonerat)\w*")
# Affirmative close-out verdicts only, excluding negated ("not resolved") and unknown ("still unknown").
CLOSE_LANGUAGE_RE = re.compile(
    r"(?im)(?<!not\s)\bRESOLVED\b"
    r"|\broot[\s-]?cause[\s-]?(?:was|is)\s+(?!still\b|unknown\b|unclear\b|not\b|yet\b|tbd\b|undetermined\b)"
    r"|(?<!not\s)\bfixed[\s-]?by\b"
)


class InvestigationActivity(BaseModel):
    """Session-durable trace of investigation activity behind the four surfacing nudges.

    ``written`` flips on any investigation WRITE verb (reads stay state-neutral). ``unresolved`` holds
    the ids opened/grown but not terminally resolved — terminal verdicts remove an id, reopen restores
    it, and root-cause/fix leave it unresolved. ``subagents`` counts debugging lanes only within the
    turn named by ``subagents_turn``, so lanes from distant turns never accumulate into one arc.

    Fields default to the pre-activity state, so a fresh session (or a null session slot in inline
    tests) reads as "nothing opened yet".
    """

    written: bool = False
    unresolved: list[str] = Field(default_factory=list)
    subagents: int = 0
    subagents_turn: str = ""


def _cli_investigation_verb(cmd: Command) -> str | None:
    if cmd.program not in ("cc-notes", "ccn") or not cmd.args or cmd.args[0] != "investigation":
        return None
    verb = cmd.args[1] if len(cmd.args) > 1 else ""
    if verb == "finding":
        # `finding` is a subgroup: fold its subcommand into the MCP suffix form (finding_add, finding_list)
        # so both surfaces share one read/write vocabulary.
        sub = cmd.args[2] if len(cmd.args) > 2 else ""
        return f"finding_{sub}" if sub else "finding"
    return verb


def _investigation_verb(evt: BaseHookEvent) -> str | None:
    """The verb an investigation call performs — an MCP suffix (open, finding_clear, root_cause) or a CLI verb (open, root-cause)."""
    name = evt.tool_name or ""
    if name.startswith(INVESTIGATION_MCP_PREFIX):
        return name[len(INVESTIGATION_MCP_PREFIX) :]
    line = evt.cmd.line
    if line:
        for cmd in line.commands:
            if (verb := _cli_investigation_verb(cmd)) is not None:
                return verb
    return None


def _tool_output(evt: BaseHookEvent) -> str:
    return getattr(evt, "tool_response", None) or ""


def _event_error(evt: BaseHookEvent) -> str:
    return getattr(evt, "error", None) or ""


def _output_investigation_id(evt: BaseHookEvent) -> str | None:
    # An `open` mints an id in its output: MCP/--json leads with {"id":"..."}, the CLI lean line with
    # the short id as its first token.
    text = _tool_output(evt) or _event_error(evt)
    if not text:
        return None
    if m := re.search(r'"id"\s*:\s*"([^"]+)"', text):
        return m.group(1)
    stripped = text.strip()
    return stripped.split()[0] if stripped else None


def _cli_positional_id(evt: BaseHookEvent) -> str | None:
    line = evt.cmd.line
    if not line:
        return None
    for cmd in line.commands:
        if _cli_investigation_verb(cmd) is not None and len(cmd.args) > 2:
            return cmd.args[2]
    return None


def _investigation_id(evt: BaseHookEvent, verb: str) -> str | None:
    """The investigation id a verb acts on: minted from output for a create, else the id positional (CLI)
    or the ``id`` input field (MCP)."""
    if verb in INVESTIGATION_OPEN_VERBS:
        return _output_investigation_id(evt)
    name = evt.tool_name or ""
    if name.startswith(INVESTIGATION_MCP_PREFIX):
        raw = evt.input.raw
        return raw["id"] if isinstance(raw.get("id"), str) and raw["id"] else None
    return _cli_positional_id(evt)


def _ids_match(a: str, b: str) -> bool:
    # cc-notes ids resolve by unique prefix, so a stored full id and a short prefix (or the reverse)
    # name the same investigation.
    return a == b or a.startswith(b) or b.startswith(a)


def _arm_id(unresolved: list[str], inv_id: str | None) -> None:
    if inv_id and not any(_ids_match(existing, inv_id) for existing in unresolved):
        unresolved.append(inv_id)


def _resolve_id(unresolved: list[str], inv_id: str | None) -> None:
    if inv_id:
        unresolved[:] = [existing for existing in unresolved if not _ids_match(existing, inv_id)]


def _load_activity(evt: BaseHookEvent) -> InvestigationActivity:
    try:
        return evt.ctx.s.load(InvestigationActivity)
    except Exception:
        return InvestigationActivity()


def investigation_touched(evt: BaseHookEvent) -> bool:
    return _load_activity(evt).written


def _turn_key(evt: BaseHookEvent) -> str:
    return str(len(evt.ctx.t) - len(evt.ctx.turn))


def _bump_subagents(evt: BaseHookEvent) -> int:
    turn = _turn_key(evt)
    try:
        with evt.ctx.s[InvestigationActivity].mutate() as act:
            if act.subagents_turn != turn:
                act.subagents_turn = turn
                act.subagents = 0
            act.subagents += 1
            return act.subagents
    except Exception:
        return 1


def _run_url(evt: BaseHookEvent) -> str | None:
    for text in (evt.cmd.raw, _tool_output(evt), _event_error(evt)):
        if m := RUN_URL_RE.search(text):
            return m.group(0)
    return None


class InvestigationCall(CustomCondition):
    """Matches an investigation call — an MCP investigation_* tool or a `cc-notes/ccn investigation <verb>` CLI leg."""

    def check(self, evt: BaseHookEvent) -> bool:
        return _investigation_verb(evt) is not None


@on(
    Event.PostToolUse,
    only_if=[InvestigationCall()],
    tests={
        Input(tool="mcp__plugin_cc-notes_cc-notes__investigation_open", tool_input={"title": "x", "premise": "y"}): Allow(),
        Input(command="cc-notes investigation append abc 'bisect reproduces earlier'"): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),  # not an investigation call — the condition misses
    },
)
def record_investigation_activity(evt: PostToolUseEvent) -> HookResult | None:
    """Trace investigation calls into session state for the CI-triage / synthesis / close nudges."""
    verb = _investigation_verb(evt)
    if verb is None:
        return None
    canon = verb.replace("-", "_")
    if canon in INVESTIGATION_READ_VERBS:
        return None
    try:
        with evt.ctx.s[InvestigationActivity].mutate() as act:
            act.written = True
            if canon in INVESTIGATION_TERMINAL_VERBS:
                _resolve_id(act.unresolved, _investigation_id(evt, canon))
            elif canon in INVESTIGATION_UNRESOLVED_VERBS:
                _arm_id(act.unresolved, _investigation_id(evt, canon))
    except Exception:
        pass
    return None


def _ci_run_failed(evt: BaseHookEvent) -> bool:
    # A watch/ship is red when its exit failed (the PostToolUseFailure envelope) or its output reports a
    # failed conclusion — never on a job merely NAMED with "failure".
    if isinstance(evt, PostToolUseFailureEvent):
        return True
    return bool(GH_FAILED_CONCLUSION_RE.search(_tool_output(evt)))


class CiTriageMoment(CustomCondition):
    """Matches a red-CI triage moment: a `gh run … --log-failed`, a failed `gh run watch` / `ccx vcs ship`, or a ci-triage subagent spawn."""

    def check(self, evt: BaseHookEvent) -> bool:
        if any(m in (evt.agent_type or "") for m in CI_TRIAGE_AGENT_MARKERS):
            return True
        cmd = evt.cmd.raw
        if GH_RUN_RE.search(cmd) and GH_LOG_FAILED_RE.search(cmd):
            return True
        if GH_WATCH_RE.search(cmd) or SHIP_RE.search(cmd):
            return _ci_run_failed(evt)
        return False


@on(
    Event.PostToolUse | Event.PostToolUseFailure,
    only_if=[Tool("Bash|Task|Agent"), CiTriageMoment(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="gh run view 12 --log-failed", output="FAIL build\nstep failed"): Warn(pattern="investigation"),
        Input(tool="Task", agent_type="cc-context:ci-triage", prompt="triage the red CI run"): Warn(pattern="investigation"),
        # A green watch whose output merely NAMES a failure-handling job stays silent (no failed conclusion).
        Input(command="gh run watch 12", output="✓ CI · main completed with 'success'\n✓ failure-handling-tests"): Allow(),
        # Benign neighbors stay silent.
        Input(command="gh run list", output="completed success"): Allow(),
        Input(command="gh run watch 12", output="✓ CI · main completed"): Allow(),
        Input(tool="Task", agent_type="general-purpose", prompt="write the docs"): Allow(),
    },
)
def nudge_ci_triage_investigation(evt: PostToolUseEvent) -> HookResult | None:
    """A red CI run with no open investigation → open one with the failing run as the first evidence."""
    if investigation_touched(evt) or fired_this_turn(evt):
        return None
    record_fire(evt)
    url = _run_url(evt)
    first = f"first evidence: {url}" if url else "first evidence: <the failing run URL>"
    return evt.warn(
        "This red CI run has no cc-notes investigation yet. Triaging a failure is exactly the arc the "
        "investigation kind records (an immutable premise, an append-only evidence timeline, a "
        "verdict), so the root cause outlives this session. Open one, citing the run as first evidence:",
        *investigation_arc_lines(mcp_active(evt), "CI: <what failed>", "<the failing job/step and why it went red>", first_evidence=first),
    )


class InvestigationSubagent(CustomCondition):
    """Matches a Task/Agent spawn that looks like a debugging/forensics lane, by agent type or prompt."""

    def check(self, evt: BaseHookEvent) -> bool:
        if any(m in (evt.agent_type or "") for m in SUBAGENT_INVESTIGATION_MARKERS):
            return True
        ti = evt._tool_input
        text = " ".join(str(ti.get(k, "")) for k in ("prompt", "description"))
        return bool(SUBAGENT_INVESTIGATION_RE.search(text))


@on(
    Event.PostToolUse,
    only_if=[Tool("Task|Agent"), InvestigationSubagent(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        # Firing needs >=2 investigation subagents persisted in session state — the FIRE proof lives
        # in tests/test_cc_notes.py; inline proves the single-lane / non-investigation silence.
        Input(tool="Task", agent_type="general-purpose", prompt="bisect the deadlock", output="root cause: unbuffered chan"): Allow(),
        Input(tool="Task", agent_type="general-purpose", prompt="write the release notes"): Allow(),
    },
)
def nudge_multiagent_synthesis(evt: PostToolUseEvent) -> HookResult | None:
    """Several debugging subagents + a verdict-bearing result + no open investigation → capture it as one."""
    n = _bump_subagents(evt)
    if investigation_touched(evt) or n < 2:
        return None
    if not VERDICT_LANGUAGE_RE.search(_tool_output(evt)) or fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(
        "Multiple debugging subagents have run and this result carries a verdict, but no cc-notes "
        "investigation holds it — the forensic arc lives only in chat and dies at session end. Capture "
        "it as one investigation: open it, append each lane's finding, then record the verdict:",
        *investigation_arc_lines(mcp_active(evt), "<what you were debugging>", "<the suspicion the lanes tested>", first_evidence="<lane 1's finding>"),
    )


class InvestigationCloseLanguage(CustomCondition):
    """Matches a write carrying investigation close-out language (RESOLVED / root-cause-was / fixed-by)."""

    def check(self, evt: BaseHookEvent) -> bool:
        content = evt.content
        return bool(content) and bool(CLOSE_LANGUAGE_RE.search(content))


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit|MultiEdit"), InvestigationCloseLanguage(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        # Firing needs an unresolved investigation id in session state — the FIRE proof lives in
        # tests/test_cc_notes.py; both stay silent here (nothing unresolved).
        Input(tool="Write", file="notes.md", content="RESOLVED: the deadlock was the pool rewrite\n"): Allow(),
        Input(tool="Write", file="notes.md", content="just some ordinary notes\n"): Allow(),
    },
)
def nudge_investigation_close(evt: PostToolUseEvent) -> HookResult | None:
    """A verdict written into a loose file while an investigation sits open → record it via the transition verbs."""
    activity = _load_activity(evt)
    if not activity.unresolved or fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(
        "You're writing a verdict into a loose file, but an investigation you opened this session is "
        "still open. Record the verdict on it — the timeline keeps the wrong first suspicion visible "
        "and the title stays clean — instead of a loose file:",
        *investigation_resolve_lines(mcp_active(evt)),
    )


@on(
    Event.Stop,
    only_if=[CcNotesAvailable()],
    max_fires=1,
    tests={
        # Fires only when an investigation id is still unresolved this session (persisted state) —
        # proven in tests/test_cc_notes.py. With default state the sweep is silent.
        Input(): Allow(),
    },
)
def nudge_investigation_stop_sweep(evt: StopEvent) -> HookResult | None:
    """At Stop, one gentle nudge to close an investigation left unresolved this session."""
    activity = _load_activity(evt)
    if not activity.unresolved:
        return None
    return evt.warn(
        "Before you stop: an investigation you appended to this session hasn't reached a verdict. Close "
        "the arc so the record stands on its own — record the root cause and confirm the fix, or "
        "exonerate/abandon it:",
        *investigation_resolve_lines(mcp_active(evt)),
    )
