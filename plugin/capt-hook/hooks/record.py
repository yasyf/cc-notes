
from __future__ import annotations

import hashlib
import re
from pathlib import Path, PurePosixPath

from captain_hook import (
    Allow,
    Arguments,
    BaseHookEvent,
    CommandMatches,
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
    Runs,
    StopEvent,
    Tool,
    Warn,
    on,
)
from captain_hook.cmd import Call
from cc_transcript.command import Word
from captain_hook.conditions import check_condition
from captain_hook.state import fired_this_turn, record_fire
from pydantic import BaseModel, Field

from .common import (
    CC_NOTES_EXECUTABLES,
    CcNotesAvailable,
    LLM_INPUT_CAP,
    MCP_TOOL_PREFIX,
    NUDGE_MAX_FIRES,
    RECORD_KINDS,
    RecordVerdict,
    clamp_title,
    ids_match,
    in_cc_pool_memory,
    json_field,
    record_command,
    repo_root,
    run_cc_notes,
    tool_output,
)
from .surface import repo_path
from .workflow import invocation

STRONG_INTERNAL_GLOBS = ("*_VERIFICATION.md", "*HANDOFF*.md", "*STATUS*.md", "*-handoff.md", "HANDOFF.md", "STATUS.md", "NOTES.md")
WEAK_INTERNAL_GLOBS = (
    "TODO.md", "*-notes.md", "runbook*.md", "runbook*", "scratch*.md", "*memo*.md", "*decision*.md",
    "*investigation*.md", "*postmortem*.md", "*rca*.md", "*root-cause*.md", "*incident*.md",
)
PUBLISHED_GLOBS = ("README*", "CHANGELOG*", "LICENSE*", "CONTRIBUTING*", "*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg")
PUBLISHED_DIRS = ("docs/",)
SECRET_GLOBS = (".env", ".env.*", "*.env", "*secret*", "*credential*", "*.key", "*.pem")

INTERNAL_BODY_RE = (
    r"(?im)^\s*- \[ \]"
    r"|\b(handoff|hand-off|remaining|next steps|runbook|verification|status|decisions?)\b"
    r"|\b(?:root cause|bisect|suspect|exonerat|falsified|postmortem)\w*"
)

EVIDENCE_SUFFIXES = frozenset({".log", ".panic", ".dump", ".core", ".trace", ".crash"})
EVIDENCE_DIR_SEGMENTS = frozenset({"panics", "crashes", "cores", "coredumps", "diagnosticreports"})
RUN_OUTPUT_PREFIXES = ("/tmp", "/private/tmp", "/var", "/private/var")
EXEMPT_DEST_PREFIXES = (*RUN_OUTPUT_PREFIXES, "/dev", "/proc", "/sys")
EXEMPT_DEST_SEGMENTS = frozenset(
    {
        ".git", "testdata", "fixtures", "node_modules", "scratchpad", "__pycache__",
        "bin", "dist", "build", "out", "target",
    }
)

EPHEMERAL_MARKERS = (*(p + "/" for p in RUN_OUTPUT_PREFIXES), "scratchpad")
RECORD_SUBCOMMANDS = frozenset((noun, verb) for noun in ("note", "doc", "log", "answer") for verb in ("add", "edit", "append"))
RECORD_BARE_NOUNS: dict[str, frozenset[str]] = {"papercut": frozenset({"list"})}
MCP_RECORD_WRITE_TOOLS = ("note_add", "doc_add", "answer_add", "log_add", "log_append", "note_edit", "doc_edit", "answer_edit", "papercut")
MCP_RECORD_WRITE_NAMES = tuple(MCP_TOOL_PREFIX + t for t in MCP_RECORD_WRITE_TOOLS)
MCP_CONTENT_FIELDS = ("title", "body", "entry")


class DurableInternalWrite(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return self.durable_shape(evt) and repo_path(evt) is not None

    def durable_shape(self, evt: BaseHookEvent) -> bool:
        file = evt.file
        if file is None:
            return False
        if in_cc_pool_memory(Path(str(file))):
            return False
        if file.under("memory/") and not file.matches(*SECRET_GLOBS):
            return True
        if file.suffix.lower() != ".md":
            return False
        if file.matches(*SECRET_GLOBS):
            return False
        if file.matches(*PUBLISHED_GLOBS) or file.under(*PUBLISHED_DIRS):
            return False
        if file.matches(*STRONG_INTERNAL_GLOBS):
            return True
        if file.matches(*WEAK_INTERNAL_GLOBS):
            return bool(evt.content) and bool(re.search(INTERNAL_BODY_RE, evt.content))
        return False


DURABLE_VERBS = {
    **{kind: record_command(kind) for kind in RECORD_KINDS},
    "runbook": "cc-notes runbook add",
    "investigation": "cc-notes investigation open",
    "plan": "cc-notes plan add",
}

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
    "- plan: an approved plan for work about to be done — context, an ordered approach, pitfalls, "
    "and how it will be verified — recorded verbatim so the next agent can execute it. cc-notes has "
    "a first-class plan primitive with a draft → approved → executing → done/abandoned lifecycle and "
    "tasks that point back at it; a plan goes stale through that lifecycle, not through re-verification.\n"
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
    "root cause; that suspect was cleared) through its findings and status. plan vs runbook splits "
    "on repetition: a plan is one approved approach to work being done now, executed once; a runbook "
    "is a standing procedure re-executed on every deploy or incident. plan vs doc splits on shape: a "
    "plan is work-shaped and closes as done or abandoned; a doc is guidance you keep fresh."
)


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit|MultiEdit"), DurableInternalWrite(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="Write", file="HANDOFF.md", content="## Status\nHandoff\n## Remaining\n- [ ] x\n"): Allow(),
        Input(tool="Write", file="README.md", content="# Readme\nsome prose\n"): Allow(),
        Input(tool="Write", file="src/foo.ts", content="export const x = 1\n"): Allow(),
        Input(tool="Write", file=".env", content="API_KEY=secret\n"): Allow(),
        Input(tool="Write", file="/n/.cc-pool/p/memory/x.md", content="---\ntype: feedback\n---\nbody\n"): Allow(),
        Input(tool="Write", file="experiments/e0-gate-memo.md", content="a plan sketch, nothing durable\n"): Allow(),
        Input(tool="Write", file="docs/design-memo.md", content="## Decision\n- [ ] x\n"): Allow(),
        Input(tool="Read", file="HANDOFF.md"): Allow(),
        Input(
            tool="Write",
            file="STATUS.md",
            content="## Status\nHandoff\n## Remaining\n- [ ] x\n",
            llm={"record": True, "kind": "doc"},
        ): Warn(pattern="durable doc content"),
        Input(
            tool="Write",
            file="/n/.cc-state/p/memory/when-the-owner-says-it-exists-find-it.md",
            content="---\nname: when the owner says it exists, find it\ndescription: search before denying\nmetadata:\n  type: feedback\n---\nbody\n",
            llm={"record": True, "kind": "doc"},
        ): Allow(),
        Input(
            tool="Write",
            file="/Users/yasyf/.claude/plans/gateway-plan-memo.md",
            content="## Decision\n## Approach\n1. do it\n",
            llm={"record": True, "kind": "plan"},
        ): Allow(),
    },
)
def nudge_record_durable(evt: PostToolUseEvent) -> HookResult | None:
    if fired_this_turn(evt):
        return None
    prompt = (
        Prompt()
        .system(RECORD_ROUTER_SYSTEM)
        .context("path", str(evt.file))
        .context("content", (evt.content or "")[:LLM_INPUT_CAP])
        .ask("Does this belong in cc-notes, and if so as which record (note/doc/log/task/papercut/runbook/investigation/plan)?")
    )
    verdict = evt.ctx.call_llm(prompt, response_model=RecordVerdict, model="small", agent=False, transcript=False)
    if not verdict.record or verdict.kind not in DURABLE_VERBS:
        return None
    record_fire(evt)
    return evt.warn(
        f"This file reads like durable {verdict.kind} content, not a loose file in the working tree. "
        f"Run `{DURABLE_VERBS[verdict.kind]}`, then delete the file."
    )


def under_prefix(path: str, prefixes: tuple[str, ...]) -> bool:
    return any(path == p or path.startswith(p + "/") for p in prefixes)


def run_output_source(path: str) -> bool:
    return under_prefix(path, RUN_OUTPUT_PREFIXES) or "results" in PurePosixPath(path).parts


def evidence_path(path: str) -> bool:
    p = PurePosixPath(path)
    return p.suffix.lower() in EVIDENCE_SUFFIXES or any(part.lower() in EVIDENCE_DIR_SEGMENTS for part in p.parts)


def durable_dest(path: str) -> bool:
    if path.startswith("$") or ":" in path.split("/", 1)[0]:
        return False
    if under_prefix(path, EXEMPT_DEST_PREFIXES):
        return False
    if any(part.lower() in EXEMPT_DEST_SEGMENTS for part in PurePosixPath(path).parts):
        return False
    return in_git_worktree(path)


def in_git_worktree(path: str) -> bool:
    return repo_root(path) is not None


def word_text(word: Word) -> str:
    return word.value if word.value is not None else word.raw


def flag_options(*flags: str) -> tuple[Option, ...]:
    return tuple(Option(flag.lstrip("-"), (flag,), bool) for flag in flags)


def value_options(*flags: str) -> tuple[Option, ...]:
    return tuple(Option(flag.lstrip("-"), (flag,)) for flag in flags)


PATHS = Operand("paths", count="*")
COPY_SCHEMA = CommandSchema(
    "cp",
    operands=(PATHS,),
    options=flag_options(
        "-r", "-R", "-a", "-v", "-f", "-p", "-n", "-i", "-l", "-L", "-P", "-H", "-u", "-x", "-c", "-s", "-d", "-T",
        "--recursive", "--archive", "--verbose", "--force", "--preserve", "--no-clobber", "--interactive", "--update",
    ),
)
RSYNC_SCHEMA = CommandSchema(
    "rsync",
    operands=(PATHS,),
    options=(
        *flag_options(
            "-a", "-v", "-r", "-z", "-h", "-P", "-n", "-u", "-c", "-l", "-t", "-p", "-o", "-g", "-D", "-H", "-x", "-q",
            "-i", "-R", "-W", "-S", "-L", "-K", "-O", "-U", "-X", "-A",
            "--archive", "--verbose", "--recursive", "--compress", "--human-readable", "--progress", "--dry-run",
            "--delete", "--stats", "--itemize-changes", "--partial", "--times", "--perms", "--owner", "--group",
            "--links", "--copy-links", "--hard-links", "--one-file-system", "--checksum", "--relative",
            "--whole-file", "--quiet", "--update", "--inplace", "--mkpath",
        ),
        *value_options(
            "--exclude", "--exclude-from", "--include", "--include-from", "--filter", "--files-from", "-e", "--rsh",
            "--chmod", "--log-file", "--compare-dest", "--copy-dest", "--link-dest", "--backup-dir", "--partial-dir",
            "--temp-dir", "-T", "--out-format", "--password-file",
        ),
    ),
)
TRANSFER_SCHEMAS = {"cp": COPY_SCHEMA, "mv": COPY_SCHEMA, "rsync": RSYNC_SCHEMA}


def evidence_transfers(evt: BaseHookEvent) -> list[str]:
    dests: list[str] = []
    staged_from_durable: set[str] = set()
    for call in evt.cmd.calls():
        if (schema := TRANSFER_SCHEMAS.get(call.name)) is None:
            continue
        paths = [word_text(word) for word in schema.bind(call).words.get("paths", ())]
        if len(paths) < 2:
            continue
        sources, dest = paths[:-1], paths[-1]
        if len(sources) == 1 and durable_dest(sources[0]):
            staged_from_durable.add(dest)
        if not durable_dest(dest):
            continue
        dest_parent = PurePosixPath(dest).parent
        if all(PurePosixPath(s).parent == dest_parent for s in sources):
            continue
        if (
            any(run_output_source(s) and s not in staged_from_durable for s in sources)
            or any(evidence_path(p) for p in paths)
        ):
            dests.append(dest)
    return dests


class EvidenceArchive(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        if evt.cmd:
            return bool(evidence_transfers(evt))
        file = evt.file
        return file is not None and file.suffix.lower() in EVIDENCE_SUFFIXES and durable_dest(str(file))


@on(
    Event.PostToolUse,
    only_if=[Tool("Bash|Write|Edit|MultiEdit"), EvidenceArchive(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(
            command="mkdir -p docs/reports/assets/vm-repro && "
            "cp -R /tmp/fusekit-vm/results/run-42 docs/reports/assets/vm-repro/phase2-forced-unmount"
        ): Warn(pattern="log append"),
        Input(command="mv crash-4821.panic docs/reports/crash-4821.panic"): Warn(pattern="log append"),
        Input(command="rsync -av /var/log/fusekit/ evidence/latest/"): Warn(pattern="log append"),
        Input(tool="Write", file="docs/reports/soak-test.log", content="I0621 vm boot ok\n"): Warn(pattern="log append"),
        Input(command="cp /tmp/run-99/output.log docs/reports/output.log"): Warn(pattern="log append"),
        Input(command="cp /tmp/run/out.log /tmp/keep/out.log"): Allow(),
        Input(command="cp README.md /tmp/civ2-stack-yaml.tmp && cp /tmp/civ2-stack-yaml.tmp docs/config/committed.yaml"): Allow(),
        Input(command="cp fixtures/batch.json internal/lfs/testdata/batch.json"): Allow(),
        Input(command="mv .git/objects/tmp_pack .git/objects/pack/pack-1.pack"): Allow(),
        Input(command="cp README.md docs/index.md"): Allow(),
        Input(command="cp -R docs/assets docs/assets-v2"): Allow(),
        Input(command="cp -R /Users/y/internal/store /Users/y/internal/store.bak"): Allow(),
        Input(command="cp /tmp/build/cc-notes /usr/local/bin/"): Allow(),
        Input(command="rsync -av --exclude '*.log' src/ docs/mirror/"): Allow(),
        Input(command="go build -o bin/cc-notes ./cmd/cc-notes"): Allow(),
        Input(tool="Write", file="docs/guide.md", content="# Guide\n"): Allow(),
    },
)
def nudge_record_evidence(evt: PostToolUseEvent) -> HookResult | None:
    if fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(
        "Machine-generated evidence belongs on a cc-notes log, not in the tracked tree. "
        "Run `cc-notes log append <id> --attach <file>`, then delete the copy."
    )


CC_NOTES_RECORD_SCHEMA = CommandSchema(
    "cc-notes",
    operands=(Operand("words", count="*"),),
    options=(
        *value_options(
            "--title", "--body", "--when", "--entry",
            "--label", "--add-label", "--rm-label", "--branch", "--add-branch", "--rm-branch",
            "--path", "--add-path", "--rm-path", "--dir", "--add-dir", "--rm-dir",
            "--commit", "--add-commit", "--rm-commit", "--attach", "--rm-attachment",
            "--model", "--repo", "--by", "--reason", "--author", "--stale-after", "--limit", "--remote",
        ),
        *flag_options(
            "--json", "--backlog", "--checkout", "--apply", "--abort", "--replace", "--all", "--clear", "--drift",
            "--expired", "--include-superseded", "--unverified", "--help", "--dry-run", "--global", "--force",
        ),
    ),
)
CONTENT_OPTIONS = ("title", "body", "when", "entry")


def record_operands(words: tuple[str, ...]) -> tuple[str, ...] | None:
    if words[:1] and (reads := RECORD_BARE_NOUNS.get(words[0])) is not None:
        return None if words[1:2] and words[1] in reads else words[1:]
    if words[:2] in RECORD_SUBCOMMANDS:
        return words[2:]
    return None


def ephemeral_refs(call: Call) -> list[str]:
    bound = CC_NOTES_RECORD_SCHEMA.bind(call)
    operands = record_operands(tuple(word_text(word) for word in bound.words.get("words", ())))
    if operands is None:
        return []
    content = [*operands, *(value for name in CONTENT_OPTIONS for value in bound.values.get(name, ()) if isinstance(value, str))]
    return [text for text in content if any(marker in text for marker in EPHEMERAL_MARKERS)]


class EphemeralRecordReference(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return any(ephemeral_refs(call) for call in evt.cmd.calls() if call.name in CC_NOTES_EXECUTABLES)


@on(
    Event.PostToolUse,
    only_if=[Tool("Bash"), EphemeralRecordReference(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command='cc-notes doc add "Handoff — full detail in session scratchpad steering-handoff.md" --when w'): Warn(pattern="purge-bound"),
        Input(command='cc-notes note add "Fact" --body "see /private/tmp/c-1/scratch.md"'): Warn(),
        Input(command='ccn note add "Fact" --body "see /tmp/c-1/scratch.md"'): Warn(pattern="purge-bound"),
        Input(command='cc-notes papercut "full repro saved at /tmp/repro.md"'): Warn(pattern="purge-bound"),
        Input(command='cc-notes doc add "Handoff" --when w --body -'): Allow(),
        Input(command="cc-notes log append abc123 --attach /tmp/out.log"): Allow(),
        Input(command='cc-notes note add "Fact" --body "content inline" --label scratchpad'): Allow(),
        Input(command='cc-notes papercut "the search tool kept returning stale results"'): Allow(),
        Input(command="cc-notes papercut list"): Allow(),
        Input(command="cc-notes papercut list /tmp/repro.md"): Allow(),
        Input(command='cc-notes papercut --model /tmp/local.gguf "clean text"'): Allow(),
        Input(command='cc-notes papercut --model=/tmp/local.gguf "clean text"'): Allow(),
        Input(command="cc-notes task list"): Allow(),
        Input(command="cat /tmp/scratch.md"): Allow(),
    },
)
def nudge_ephemeral_record_reference(evt: PostToolUseEvent) -> HookResult | None:
    if fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(
        "This record cites a purge-bound path (`/tmp`, `/var`, or a scratchpad). "
        "Put the content in the record text and store artifacts with `cc-notes log append <id> --attach <file>`."
    )


def mcp_ephemeral_refs(evt: PostToolUseEvent) -> list[str]:
    return [
        value
        for field in MCP_CONTENT_FIELDS
        if isinstance(value := evt.input.raw.get(field), str) and any(marker in value for marker in EPHEMERAL_MARKERS)
    ]


class McpEphemeralReference(CustomCondition):

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
        ): Warn(pattern="purge-bound"),
        Input(
            tool="mcp__plugin_cc-notes_cc-notes__papercut",
            tool_input={"body": "full repro saved at /tmp/repro.md"},
        ): Warn(pattern="purge-bound"),
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
    if fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(
        "This record cites a purge-bound path (`/tmp`, `/var`, or a scratchpad). "
        "Put the content in the `body` param and store artifacts with the `attach` param of `log_append`."
    )


def plan_text(evt: PostToolUseEvent) -> str | None:
    ti = evt.input.raw
    path = ti.get("planFilePath")
    if isinstance(path, str) and Path(path).is_file():
        text = Path(path).read_text(encoding="utf-8").strip()
        if text:
            return text
    inline = ti.get("plan")
    return inline.strip() if isinstance(inline, str) and inline.strip() else None


PLAN_HEADING_RE = re.compile(r"(?m)^#[ \t]+(\S.*?)[ \t]*$")


def plan_title(evt: PostToolUseEvent, text: str) -> str:
    if m := PLAN_HEADING_RE.search(text):
        return clamp_title(m.group(1))
    path = evt.input.raw.get("planFilePath")
    return Path(path).stem if isinstance(path, str) and path else "Approved plan"


def capture_plan(evt: PostToolUseEvent, text: str) -> str:
    out = run_cc_notes(evt, "plan", "add", "--json", "--approved", f"--body={text}", "--", plan_title(evt, text))
    return json_field(out, "id")


def revise_plan(evt: PostToolUseEvent, plan_id: str, text: str) -> str:
    out = run_cc_notes(evt, "plan", "edit", plan_id, "--json", f"--body={text}")
    return json_field(out, "id")


class CapturedPlan(BaseModel):
    id: str
    title: str


class PlanCaptures(BaseModel):
    by_path: dict[str, CapturedPlan] = Field(default_factory=dict)
    advisories: int = 0


def plan_key(evt: PostToolUseEvent) -> str:
    path = evt.input.raw.get("planFilePath")
    return path if isinstance(path, str) else ""


def record_plan(evt: PostToolUseEvent, text: str) -> tuple[str, bool]:
    title = plan_title(evt, text)
    prior = evt.ctx.s.load(PlanCaptures).by_path.get(plan_key(evt))
    if prior and prior.title == title:
        return revise_plan(evt, prior.id, text), True
    if not (plan_id := capture_plan(evt, text)):
        return "", False
    with evt.ctx.s[PlanCaptures].mutate() as state:
        state.by_path[plan_key(evt)] = CapturedPlan(id=plan_id, title=title)
    return plan_id, False


def advisory_budget(evt: PostToolUseEvent) -> bool:
    with evt.ctx.s[PlanCaptures].mutate() as state:
        if state.advisories >= NUDGE_MAX_FIRES:
            return False
        state.advisories += 1
        return True


PLAN_RECORDED = "Plan recorded as a cc-notes plan."
PLAN_REVISED = "Plan revision written to its cc-notes plan in place."
PLAN_TASK_RULE = (
    "Native tasks vanish at session end, so file durable work with `cc-notes task add --criterion "
    '"<how to verify>"{plan_link}`.'
)


@on(
    Event.PostToolUse,
    only_if=[Tool("ExitPlanMode"), CcNotesAvailable()],
    max_fires=None,
    tests={
        Input(tool="ExitPlanMode"): Warn(pattern="Native tasks vanish at session end"),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def nudge_plan_capture(evt: PostToolUseEvent) -> HookResult | None:
    text = plan_text(evt)
    digest = hashlib.sha256((text or "").encode()).hexdigest()[:16]
    if not evt.ctx.s.once(f"{plan_key(evt)}:{digest}", scope="plan"):
        return None
    plan_id, revised = record_plan(evt, text) if text else ("", False)
    lines = [PLAN_REVISED if revised else PLAN_RECORDED] if plan_id else []
    if advisory_budget(evt):
        lines.append(PLAN_TASK_RULE.format(plan_link=" --plan <id>" if plan_id else ""))
    return evt.warn(*lines) if lines else None


CI_TRIAGE_RULE = (
    'A red CI run needs a cc-notes investigation. Open one with `cc-notes investigation open "<title>" '
    '"<premise>"` and cite the run as first evidence.'
)
SYNTHESIS_RULE = (
    "Debugging subagents returned a verdict that no cc-notes investigation holds. "
    'Capture it with `cc-notes investigation open "<title>" "<premise>"`.'
)
CLOSE_RULE = (
    "A verdict belongs on the open investigation, not a loose file. "
    'Record it with `cc-notes investigation root-cause <id> "<the true cause>"`.'
)
STOP_SWEEP_RULE = (
    "An investigation opened this session has no verdict yet. "
    'Close it with `cc-notes investigation root-cause <id> "<the true cause>"`.'
)

INVESTIGATION_MCP_PREFIX = MCP_TOOL_PREFIX + "investigation_"
INVESTIGATION_READ_VERBS = frozenset({"list", "show", "search", "history", "finding_list"})
INVESTIGATION_OPEN_VERBS = frozenset({"open", "add"})
INVESTIGATION_TERMINAL_VERBS = frozenset({"confirm", "exonerate", "abandon"})
INVESTIGATION_UNRESOLVED_VERBS = INVESTIGATION_OPEN_VERBS | frozenset({"append", "root_cause", "fix", "reopen"})

GH_SCHEMA = CommandSchema(
    "gh",
    operands=(Operand("noun"),),
    options=(Option("repo", ("--repo", "-R")), Option("log_failed", ("--log-failed",), type=bool)),
)


def reads_failed_logs(args: Arguments) -> bool:
    noun = args.words.get("noun", ())
    return bool(noun) and noun[0].value == "run" and True in args.values.get("log_failed", ())


GH_RUN_LOG_FAILED = CommandMatches(GH_SCHEMA, only_if=(reads_failed_logs,))
GH_RUN_WATCH = Runs("gh", "run", "watch")
CCX_VCS_SHIP = Runs("ccx", "vcs", "ship")
GH_FAILED_CONCLUSION_RE = re.compile(
    r"(?im)^\s*[X✗✘](?:\s|$)"
    r"|completed with ['\"]?(?:failure|timed_out|cancelled|startup_failure|action_required)"
    r"|conclusion['\"]?\s*[:=]\s*['\"]?(?:failure|timed_out|cancelled|startup_failure)"
)

CI_TRIAGE_AGENT_MARKERS = ("ci-triage",)
SUBAGENT_INVESTIGATION_MARKERS = ("ci-triage", "debug", "forensic", "bug")
SUBAGENT_INVESTIGATION_RE = re.compile(
    r"(?i)\b(?:investigat|root[\s-]?cause|bisect|debug|forensic|triage|repro|suspect|regression)\w*"
)
VERDICT_LANGUAGE_RE = re.compile(r"(?i)\b(?:root[\s-]?cause|confirmed|falsified|exonerat)\w*")
CLOSE_LANGUAGE_RE = re.compile(
    r"(?im)(?<!not\s)\bRESOLVED\b"
    r"|\broot[\s-]?cause[\s-]?(?:was|is)\s+(?!still\b|unknown\b|unclear\b|not\b|yet\b|tbd\b|undetermined\b)"
    r"|(?<!not\s)\bfixed[\s-]?by\b"
)


class InvestigationActivity(BaseModel):
    written: bool = False
    unresolved: list[str] = Field(default_factory=list)
    subagents: int = 0
    subagents_turn: str = ""


def cc_notes_words(evt: BaseHookEvent) -> list[tuple[str, ...]]:
    return [invocation(call)[0][1:] for call in evt.cmd.calls() if call.name in CC_NOTES_EXECUTABLES]


def _cli_investigation(evt: BaseHookEvent) -> tuple[str, str | None] | None:
    for words in cc_notes_words(evt):
        if words[:1] != ("investigation",):
            continue
        verb = words[1] if len(words) > 1 else ""
        if verb == "finding":
            verb = f"finding_{words[2]}" if len(words) > 2 else "finding"
        return verb, words[2] if len(words) > 2 else None
    return None


def _investigation_verb(evt: BaseHookEvent) -> str | None:
    name = evt.tool_name or ""
    if name.startswith(INVESTIGATION_MCP_PREFIX):
        return name[len(INVESTIGATION_MCP_PREFIX) :]
    cli = _cli_investigation(evt)
    return cli[0] if cli else None


def _event_error(evt: BaseHookEvent) -> str:
    return getattr(evt, "error", None) or ""


def _minted_id(evt: BaseHookEvent) -> str | None:
    text = tool_output(evt) or _event_error(evt)
    if not text:
        return None
    if m := re.search(r'"id"\s*:\s*"([^"]+)"', text):
        return m.group(1)
    stripped = text.strip()
    return stripped.split()[0] if stripped else None


def _investigation_id(evt: BaseHookEvent, verb: str) -> str | None:
    if verb in INVESTIGATION_OPEN_VERBS:
        return _minted_id(evt)
    name = evt.tool_name or ""
    if name.startswith(INVESTIGATION_MCP_PREFIX):
        raw = evt.input.raw
        return raw["id"] if isinstance(raw.get("id"), str) and raw["id"] else None
    cli = _cli_investigation(evt)
    return cli[1] if cli else None


def _arm_id(unresolved: list[str], inv_id: str | None) -> None:
    if inv_id and not any(ids_match(existing, inv_id) for existing in unresolved):
        unresolved.append(inv_id)


def _resolve_id(unresolved: list[str], inv_id: str | None) -> None:
    if inv_id:
        unresolved[:] = [existing for existing in unresolved if not ids_match(existing, inv_id)]


def _load_activity(evt: BaseHookEvent) -> InvestigationActivity:
    return evt.ctx.s.load(InvestigationActivity)


def investigation_touched(evt: BaseHookEvent) -> bool:
    return _load_activity(evt).written


def _turn_key(evt: BaseHookEvent) -> str:
    return str(len(evt.ctx.t) - len(evt.ctx.turn))


def _bump_subagents(evt: BaseHookEvent) -> int:
    turn = _turn_key(evt)
    with evt.ctx.s[InvestigationActivity].mutate() as act:
        if act.subagents_turn != turn:
            act.subagents_turn = turn
            act.subagents = 0
        act.subagents += 1
        return act.subagents


class InvestigationCall(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        return _investigation_verb(evt) is not None


@on(
    Event.PostToolUse,
    only_if=[InvestigationCall()],
    tests={
        Input(tool="mcp__plugin_cc-notes_cc-notes__investigation_open", tool_input={"title": "x", "premise": "y"}): Allow(),
        Input(command="cc-notes investigation append abc 'bisect reproduces earlier'"): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def record_investigation_activity(evt: PostToolUseEvent) -> HookResult | None:
    verb = _investigation_verb(evt)
    if verb is None:
        return None
    canon = verb.replace("-", "_")
    if canon in INVESTIGATION_READ_VERBS:
        return None
    with evt.ctx.s[InvestigationActivity].mutate() as act:
        act.written = True
        if canon in INVESTIGATION_TERMINAL_VERBS:
            _resolve_id(act.unresolved, _investigation_id(evt, canon))
        elif canon in INVESTIGATION_UNRESOLVED_VERBS:
            _arm_id(act.unresolved, _investigation_id(evt, canon))
    return None


TASK_CREATED = Or(
    Tool(MCP_TOOL_PREFIX + "task_add"),
    *(Runs(program, "task", "add") for program in sorted(CC_NOTES_EXECUTABLES)),
)


@on(
    Event.PostToolUse,
    only_if=[TASK_CREATED, CcNotesAvailable()],
    max_fires=None,
    tests={
        Input(command="cc-notes task list"): Allow(),
        Input(command="cc-notes task show abc1234"): Allow(),
        Input(tool="mcp__plugin_cc-notes_cc-notes__task_list"): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
        Input(command="cc-notes task add 'ship the fix'", output="abc1234\topen\tP2\t-\tship the fix"): Allow(),
    },
)
def link_task_to_investigation(evt: PostToolUseEvent) -> HookResult | None:
    unresolved = _load_activity(evt).unresolved
    if len(unresolved) != 1:
        return None
    task_id = _minted_id(evt)
    if not task_id or ids_match(task_id, unresolved[0]):
        return None
    if run_cc_notes(evt, "investigation", "follow-up", unresolved[0], task_id) is None:
        return None
    return evt.warn("Linked the new task to the open investigation as a follow-up.")


def _ci_run_reported_failure(evt: BaseHookEvent) -> bool:
    return bool(GH_FAILED_CONCLUSION_RE.search(f"{tool_output(evt)}\n{_event_error(evt)}"))


class CiTriageMoment(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        if any(m in (evt.agent_type or "") for m in CI_TRIAGE_AGENT_MARKERS):
            return True
        if check_condition(GH_RUN_LOG_FAILED, evt):
            return True
        watched = check_condition(GH_RUN_WATCH, evt) or check_condition(CCX_VCS_SHIP, evt)
        return watched and _ci_run_reported_failure(evt)


@on(
    Event.PostToolUse | Event.PostToolUseFailure,
    only_if=[Tool("Bash|Task|Agent"), CiTriageMoment(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(command="gh run view 12 --log-failed", output="FAIL build\nstep failed"): Warn(pattern="investigation"),
        Input(tool="Task", agent_type="cc-context:ci-triage", prompt="triage the red CI run"): Warn(pattern="investigation"),
        Input(command="gh run watch 12", output="✓ CI · main completed with 'success'\n✓ failure-handling-tests"): Allow(),
        Input(command="gh run list", output="completed success"): Allow(),
        Input(command="gh run watch 12", output="✓ CI · main completed"): Allow(),
        Input(tool="Task", agent_type="general-purpose", prompt="write the docs"): Allow(),
    },
)
def nudge_ci_triage_investigation(evt: PostToolUseEvent) -> HookResult | None:
    if investigation_touched(evt) or fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(CI_TRIAGE_RULE)


class InvestigationSubagent(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        if any(m in (evt.agent_type or "") for m in SUBAGENT_INVESTIGATION_MARKERS):
            return True
        ti = evt.input.raw
        text = " ".join(str(ti.get(k, "")) for k in ("prompt", "description"))
        return bool(SUBAGENT_INVESTIGATION_RE.search(text))


@on(
    Event.PostToolUse,
    only_if=[Tool("Task|Agent"), InvestigationSubagent(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="Task", agent_type="general-purpose", prompt="bisect the deadlock", output="root cause: unbuffered chan"): Allow(),
        Input(tool="Task", agent_type="general-purpose", prompt="write the release notes"): Allow(),
    },
)
def nudge_multiagent_synthesis(evt: PostToolUseEvent) -> HookResult | None:
    n = _bump_subagents(evt)
    if investigation_touched(evt) or n < 2:
        return None
    if not VERDICT_LANGUAGE_RE.search(tool_output(evt)) or fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(SYNTHESIS_RULE)


class InvestigationCloseLanguage(CustomCondition):

    def check(self, evt: BaseHookEvent) -> bool:
        content = evt.content
        return bool(content) and bool(CLOSE_LANGUAGE_RE.search(content))


@on(
    Event.PostToolUse,
    only_if=[Tool("Write|Edit|MultiEdit"), InvestigationCloseLanguage(), CcNotesAvailable()],
    max_fires=NUDGE_MAX_FIRES,
    tests={
        Input(tool="Write", file="notes.md", content="RESOLVED: the deadlock was the pool rewrite\n"): Allow(),
        Input(tool="Write", file="notes.md", content="just some ordinary notes\n"): Allow(),
    },
)
def nudge_investigation_close(evt: PostToolUseEvent) -> HookResult | None:
    activity = _load_activity(evt)
    if not activity.unresolved or fired_this_turn(evt):
        return None
    record_fire(evt)
    return evt.warn(CLOSE_RULE)


@on(
    Event.Stop,
    only_if=[CcNotesAvailable()],
    max_fires=1,
    tests={
        Input(): Allow(),
    },
)
def nudge_investigation_stop_sweep(evt: StopEvent) -> HookResult | None:
    activity = _load_activity(evt)
    if not activity.unresolved:
        return None
    return evt.warn(STOP_SWEEP_RULE)
