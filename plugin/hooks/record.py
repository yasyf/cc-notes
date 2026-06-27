"""The Record/push router that flags durable internal writes, plus the plan-tasks nudge."""

from __future__ import annotations

import re
from pathlib import Path

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
    "scratch with no durable value. When it could plausibly be either, answer record=false. Only a "
    "clear case records.\n"
    "\n"
    "When record=true, choose exactly one kind:\n"
    "- note: a single durable fact or decision — one verifiable claim about the code (e.g. 'retry "
    "backoff caps at 30s because the server drops connections past it').\n"
    "- doc: living, long-form guidance for the next agent that you keep fresh — a handoff brief, a "
    "runbook, design rationale for an in-flight change, an investigation write-up. A doc is "
    "re-verified, drifts when the code moves, and carries a 'read this when…' trigger.\n"
    "- log: an immutable, append-only chronology — an incident timeline, a rollout log, a debugging "
    "session. Its value is the running record itself; entries are never edited and it has no "
    "freshness lifecycle.\n"
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
