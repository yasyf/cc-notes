"""The answer mirror: capture every AskUserQuestion answer as a cc-notes answer, and resurface the durable ones."""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any, Literal, NamedTuple

from captain_hook import (
    Allow,
    Event,
    HookResult,
    Input,
    PostToolUseEvent,
    Prompt,
    SessionStartEvent,
    Tool,
    UserPromptSubmitEvent,
    on,
)
from pydantic import BaseModel

from .common import (
    LLM_INPUT_CAP,
    CcNotesAvailable,
    SessionAnswers,
    answer_line,
    clamp_title,
    durable_answers,
    ids_match,
    json_field,
    mcp_active,
    remember_answers,
    run_cc_notes,
    short_id,
    unseen_answers,
)
from .compact import CompactResume
from .surface import SurfacePick

MAX_ANSWER_PATHS = 10
RESTORE_ANSWER_CAP = 30

SECRET_RE = re.compile(
    r"-----BEGIN [A-Z ]*PRIVATE KEY-----"
    r"|\b(?:sk|pk|rk)-[A-Za-z0-9_-]{20,}"
    r"|\bgh[pousr]_[A-Za-z0-9]{36,}"
    r"|\bxox[abprs]-[A-Za-z0-9-]{10,}"
    r"|\bAKIA[0-9A-Z]{16}\b"
    r"|\bAIza[0-9A-Za-z_-]{35}"
    r"|(?i:\b(?:password|passwd|secret|token|api[_-]?key)\s*[:=]\s*\S{8,})"
)

ANSWER_TRIAGE_SYSTEM = (
    "A user just answered questions a coding agent asked. Classify each answer's scope.\n"
    "\n"
    "- durable: a preference, convention, or decision that should hold beyond this task — how the "
    "user wants things done, a design choice later work must respect.\n"
    "- ephemeral: a one-off pick that only steers the task at hand — which of two files to open "
    "first, whether to proceed now.\n"
    "\n"
    "Earlier durable answers are listed as candidates. When an answer replaces a candidate that asks "
    "the same question, name that candidate's id in supersedes; otherwise leave supersedes empty. "
    "Return one verdict per answer, keyed by its index."
)

PROMPT_ANSWERS_SYSTEM = (
    "You are a precision filter. The user just sent a coding agent a prompt. The candidates are durable "
    "answers the user gave to earlier questions: preferences, conventions, and decisions. Keep only the "
    "answers the agent should honor while acting on this prompt, and drop the ones unrelated to it. "
    "Return an empty list when none bear on the prompt.\n"
    "\n"
    "Return the ids to surface, as a subset of the candidate ids given."
)


class AnsweredQuestion(NamedTuple):
    """One AskUserQuestion question paired with the user's answer."""

    question: str
    header: str
    answer: str
    options: list[str]
    notes: str


class AnswerVerdict(BaseModel):
    """The triage verdict for the answer at ``index``: its scope, and the candidate id it replaces."""

    index: int
    scope: Literal["durable", "ephemeral"] = "durable"
    supersedes: str = ""


class AnswerTriage(BaseModel):
    """The triage's verdicts; an answer with no verdict records as durable with no supersede."""

    verdicts: list[AnswerVerdict] = []


def answered_questions(evt: PostToolUseEvent) -> list[AnsweredQuestion]:
    """The questions the user answered, matched to their answers by exact question text.

    The result carries ``answers`` keyed by question text (a multiSelect answer joins its labels
    with ", ") and ``annotations`` holding the user's ``notes``. An answer or note that looks like
    a secret never records, because the refs sync to the remote.
    """
    response = evt.tool_response
    if isinstance(response, str):
        try:
            response = json.loads(response)
        except json.JSONDecodeError:
            return []
    if not isinstance(response, dict) or not isinstance(answers := response.get("answers"), dict):
        return []
    annotations = response.get("annotations") or {}
    pairs = []
    for q in evt._tool_input.get("questions", []):
        answer = answers.get(q["question"])
        if not isinstance(answer, str) or SECRET_RE.search(answer):
            continue
        notes = (annotations.get(q["question"]) or {}).get("notes") or ""
        pairs.append(
            AnsweredQuestion(
                question=q["question"],
                header=q.get("header", ""),
                answer=answer,
                options=[o["label"] for o in q.get("options", [])],
                notes="" if SECRET_RE.search(notes) else notes.strip(),
            )
        )
    return pairs


def answer_body(pair: AnsweredQuestion) -> str:
    lines = [pair.answer]
    if clamp_title(pair.question) != pair.question:
        lines.append(f"Question: {pair.question}")
    if pair.options:
        lines.append("Options: " + " | ".join(pair.options))
    if pair.notes:
        lines.append(f"Notes: {pair.notes}")
    return "\n".join(lines)


def triage_answers(evt: PostToolUseEvent, pairs: list[AnsweredQuestion], candidates: list[dict[str, Any]]) -> dict[int, AnswerVerdict]:
    """Classify each answer's scope and name the candidate it replaces; every answer durable on LLM failure."""
    prompt = (
        Prompt()
        .system(ANSWER_TRIAGE_SYSTEM)
        .context("answers", "\n".join(f"{i}\t{p.question} → {p.answer}" for i, p in enumerate(pairs))[:LLM_INPUT_CAP])
        .context("candidates", "\n".join(f"{a['id']}\t{answer_line(a)}" for a in candidates)[:LLM_INPUT_CAP])
        .ask("For each answer index: is it durable or ephemeral, and which candidate id, if any, does it supersede?")
    )
    try:
        triage = evt.ctx.call_llm(prompt, response_model=AnswerTriage, model="small", agent=False, transcript=False)
    except Exception:
        return {}
    return {v.index: v for v in triage.verdicts}


def superseded_id(verdict: AnswerVerdict | None, candidates: list[dict[str, Any]]) -> str:
    """The full id of the candidate the verdict names, "" when it names none of them."""
    if verdict is None or not verdict.supersedes:
        return ""
    return next((a["id"] for a in candidates if ids_match(a["id"], verdict.supersedes)), "")


def session_paths(evt: PostToolUseEvent) -> list[str]:
    """Up to :data:`MAX_ANSWER_PATHS` repo-relative paths this session touched, most recent first."""
    root = (evt.ctx.git("rev-parse", "--show-toplevel") or "").strip()
    if not root:
        return []
    base = Path(root).resolve()
    paths: list[str] = []
    for ref in reversed(evt.ctx.transcript.files_touched):
        path = Path(str(ref))
        if not path.is_absolute():
            path = (evt.cwd or base) / path
        try:
            rel = path.resolve().relative_to(base).as_posix()
        except ValueError:
            continue
        if rel not in paths:
            paths.append(rel)
        if len(paths) == MAX_ANSWER_PATHS:
            break
    return paths


def anchor_args(evt: PostToolUseEvent) -> list[str]:
    args: list[str] = []
    branch = (evt.ctx.git("rev-parse", "--abbrev-ref", "HEAD") or "").strip()
    if branch and branch != "HEAD":
        args += ["--branch", branch]
    for path in session_paths(evt):
        args += ["--path", path]
    return args


def add_answer(evt: PostToolUseEvent, pair: AnsweredQuestion, scope: str, anchors: list[str]) -> str:
    """Record one answer; its id, or "" if the write failed."""
    labels = ["--label", f"scope:{scope}"]
    if pair.header:
        labels += ["--label", f"header:{pair.header}"]
    out = run_cc_notes(evt, "answer", "add", "--json", *labels, *anchors, f"--body={answer_body(pair)}", "--", clamp_title(pair.question))
    return json_field(out, "id")


@on(
    Event.PostToolUse,
    only_if=[Tool("AskUserQuestion"), CcNotesAvailable()],
    max_fires=None,
    tests={
        Input(tool="AskUserQuestion", tool_input={"questions": [{"question": "Q?", "header": "h", "multiSelect": False, "options": [{"label": "A"}]}]}): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def record_user_answers(evt: PostToolUseEvent) -> HookResult | None:
    """Record every answer the user gave to an AskUserQuestion as a cc-notes answer.

    Uncapped, like the plan capture: ``max_fires`` would silently drop every answer past the cap.
    """
    pairs = answered_questions(evt)
    if not pairs:
        return None
    candidates = durable_answers(evt)
    verdicts = triage_answers(evt, pairs, candidates)
    anchors = anchor_args(evt)
    recorded: list[dict[str, Any]] = []
    acks: list[str] = []
    for i, pair in enumerate(pairs):
        verdict = verdicts.get(i)
        scope = verdict.scope if verdict else "durable"
        if not (answer_id := add_answer(evt, pair, scope, anchors)):
            continue
        ack = f"{short_id(answer_id)} ({scope}"
        if (old := superseded_id(verdict, candidates)) and run_cc_notes(evt, "answer", "supersede", old, "--by", answer_id, "--json") is not None:
            ack += f", supersedes {short_id(old)}"
            with evt.ctx.s[SessionAnswers].mutate() as state:
                state.lines.pop(old, None)
        acks.append(ack + ")")
        recorded.append({"id": answer_id, "title": clamp_title(pair.question), "body": answer_body(pair)})
    if not recorded:
        return None
    remember_answers(evt, recorded)
    return evt.warn(f"Recorded the user's answers in cc-notes: {', '.join(acks)}.")


def pick_prompt_answers(evt: UserPromptSubmitEvent, fresh: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The unseen durable answers that bear on the prompt; none when the filter fails."""
    lines = {a["id"]: answer_line(a) for a in fresh}
    prompt = (
        Prompt()
        .system(PROMPT_ANSWERS_SYSTEM)
        .context("prompt", (evt.user_prompt or "")[:LLM_INPUT_CAP])
        .context("candidates", "\n".join(f"{aid}\t{line}" for aid, line in lines.items()))
        .ask("Which candidate ids should the agent honor while acting on this prompt?")
    )
    try:
        pick = evt.ctx.call_llm(prompt, response_model=SurfacePick, model="small", agent=False, transcript=False)
    except Exception:
        return []
    chosen = set(pick.ids) & set(lines)
    return [a for a in fresh if a["id"] in chosen]


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
)
def float_prompt_answers(evt: UserPromptSubmitEvent) -> HookResult | None:
    """Surface the unseen durable answers relevant to this prompt, each once per session.

    The session's first prompt belongs to the digest in session.py. Only surfaced answers are
    marked seen, so an answer unrelated to this prompt stays a candidate for a later one.
    """
    if evt.ctx.s.once("first", scope="prompt-answers"):
        return None
    fresh = unseen_answers(evt, durable_answers(evt))
    if not fresh:
        return None
    picked = pick_prompt_answers(evt, fresh)
    if not picked:
        return None
    show = "the answer_show tool" if mcp_active(evt) else "`cc-notes answer show <id>`"
    return evt.warn(
        f"Durable answers the user gave that bear on this prompt — honor them ({show} has the full record):",
        *remember_answers(evt, picked),
    )


@on(
    Event.SessionStart,
    only_if=[CompactResume()],
    tests={
        Input(source="startup"): Allow(),
        Input(source="compact"): Allow(),
    },
)
def restore_answers_after_compact(evt: SessionStartEvent) -> HookResult | None:
    """After a compaction, re-inject the answers this session captured or surfaced, most recent last."""
    lines = list(evt.ctx.s.load(SessionAnswers).lines.values())[-RESTORE_ANSWER_CAP:]
    if not lines:
        return None
    return evt.warn("Context was just compacted. Answers the user gave, captured or surfaced this session:", *lines)
