
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
    COMPACT_ANSWER_BUDGET,
    LLM_INPUT_CAP,
    CcNotesAvailable,
    SessionAnswers,
    answer_line,
    answer_text,
    clamp_title,
    clip,
    durable_answers,
    json_field,
    parse_answers,
    remember_answer_lines,
    remember_answers,
    run_cc_notes,
    short_id,
    unseen_answers,
    utf8_len,
)
from .compact import CompactResume
from .deferred import defer
from .surface import SurfacePick

MAX_ANSWER_PATHS = 10
RESTORE_EXCERPT_CHARS = 160

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
    "\n"
    "Return an object with one field, ids: the candidate ids to surface, as a subset of those given. "
    "When none bear on the prompt, ids is an empty array."
)


class AnsweredQuestion(NamedTuple):

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


class AnswerCaptureLock(BaseModel):
    """Empty state whose file lock serializes one session's background answer captures.

    A capture lists the durable candidates, triages against them, records, and retires the
    candidate its verdict supersedes. Two captures overlapping would both list candidates before
    either recorded, so the second would supersede an answer the first had already replaced and
    both would stay live. Running on the reply's thread made that window small; running in the
    background makes it the whole capture.
    """


class PromptAnswerPicks(BaseModel):
    """The answers a prompt's background pick staged for the next prompt to float, id to rendered line."""

    lines: dict[str, str] = {}


def answered_questions(evt: PostToolUseEvent) -> list[AnsweredQuestion]:
    response = evt.tool_response
    if isinstance(response, str):
        response = json.loads(response)
    if not isinstance(response, dict) or not isinstance(answers := response.get("answers"), dict):
        return []
    annotations = response.get("annotations") or {}
    pairs = []
    for q in evt.input.raw.get("questions", []):
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
    prompt = (
        Prompt()
        .system(ANSWER_TRIAGE_SYSTEM)
        .context("answers", "\n".join(f"{i}\t{p.question} → {p.answer}" for i, p in enumerate(pairs))[:LLM_INPUT_CAP])
        .context("candidates", "\n".join(f"{a['id']}\t{answer_line(a)}" for a in candidates)[:LLM_INPUT_CAP])
        .ask("For each answer index: is it durable or ephemeral, and which candidate id, if any, does it supersede?")
    )
    triage = evt.ctx.call_llm(prompt, response_model=AnswerTriage, model="small", agent=False, transcript=False)
    return {v.index: v for v in triage.verdicts}


def superseded_id(verdict: AnswerVerdict | None, candidates: list[dict[str, Any]]) -> str:
    if verdict is None or not verdict.supersedes:
        return ""
    matches = [a["id"] for a in candidates if a["id"].startswith(verdict.supersedes)]
    if verdict.supersedes in matches:
        return verdict.supersedes
    return matches[0] if len(matches) == 1 else ""


def session_paths(evt: PostToolUseEvent) -> list[str]:
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
    labels = ["--label", f"scope:{scope}"]
    if pair.header:
        labels += ["--label", f"header:{pair.header}"]
    out = run_cc_notes(evt, "answer", "add", "--json", *labels, *anchors, f"--body={answer_body(pair)}", "--", clamp_title(pair.question))
    return json_field(out, "id")


def capture_user_answers(evt: PostToolUseEvent) -> HookResult | None:
    pairs = answered_questions(evt)
    if not pairs:
        return None
    candidates = durable_answers(evt)
    verdicts = triage_answers(evt, pairs, candidates)
    anchors = anchor_args(evt)
    recorded: list[dict[str, Any]] = []
    for i, pair in enumerate(pairs):
        verdict = verdicts.get(i)
        scope = verdict.scope if verdict else "durable"
        if not (answer_id := add_answer(evt, pair, scope, anchors)):
            continue
        old = superseded_id(verdict, candidates)
        if old and old != answer_id and run_cc_notes(evt, "answer", "supersede", old, "--by", answer_id, "--json") is not None:
            with evt.ctx.s[SessionAnswers].mutate() as state:
                state.lines.pop(old, None)
        recorded.append({"id": answer_id, "title": clamp_title(pair.question), "body": answer_body(pair)})
    if not recorded:
        return None
    remember_answers(evt, recorded)
    return evt.warn("Recorded answers in cc-notes. Review them with `cc-notes answer list`.")


@on(
    Event.PostToolUse,
    only_if=[Tool("AskUserQuestion"), CcNotesAvailable()],
    max_fires=None,
    async_=True,
    tests={
        Input(tool="AskUserQuestion", tool_input={"questions": [{"question": "Q?", "header": "h", "multiSelect": False, "options": [{"label": "A"}]}]}): Allow(),
        Input(tool="Edit", file="m.py"): Allow(),
    },
)
def record_user_answers(evt: PostToolUseEvent) -> None:
    with evt.ctx.s[AnswerCaptureLock].mutate():
        ack = capture_user_answers(evt)
    defer(evt, ack)


def pick_prompt_answers(evt: UserPromptSubmitEvent, fresh: list[dict[str, Any]]) -> dict[str, str]:
    lines = {a["id"]: answer_line(a) for a in fresh}
    prompt = (
        Prompt()
        .system(PROMPT_ANSWERS_SYSTEM)
        .context("prompt", (evt.user_prompt or "")[:LLM_INPUT_CAP])
        .context("candidates", "\n".join(f"{aid}\t{line}" for aid, line in lines.items()))
        .ask("Which candidate ids should the agent honor while acting on this prompt?")
    )
    pick = evt.ctx.call_llm(prompt, response_model=SurfacePick, model="small", agent=False, transcript=False)
    chosen = set(pick.ids)
    return {aid: line for aid, line in lines.items() if aid in chosen}


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
    max_fires=None,
    async_=True,
)
def stage_prompt_answers(evt: UserPromptSubmitEvent) -> None:
    if not (fresh := unseen_answers(evt, durable_answers(evt))):
        return
    if not (picked := pick_prompt_answers(evt, fresh)):
        return
    with evt.ctx.s[PromptAnswerPicks].mutate() as state:
        state.lines.update(picked)


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
)
def float_prompt_answers(evt: UserPromptSubmitEvent) -> HookResult | None:
    with evt.ctx.s[PromptAnswerPicks].mutate() as state:
        staged, state.lines = state.lines, {}
    if not (lines := remember_answer_lines(evt, staged)):
        return None
    return evt.warn(
        "Durable answers already given bear on this prompt; honor them. `cc-notes answer show <id>` has the full record:",
        *lines,
    )


def current_answers(evt: SessionStartEvent, ids: list[str]) -> list[dict[str, Any]]:
    rows = {a["id"]: a for a in parse_answers(run_cc_notes(evt, "answer", "list", "--json", "--include-superseded"))}

    def heads(answer_id: str, visited: set[str]) -> list[dict[str, Any]]:
        if answer_id in visited or (row := rows.get(answer_id)) is None:
            return []
        visited.add(answer_id)
        if replacements := row.get("superseded_by"):
            return [head for by in replacements for head in heads(by, visited)]
        return [] if row.get("stale_at") else [row]

    current: dict[str, dict[str, Any]] = {}
    for answer_id in ids:
        for head in heads(answer_id, set()):
            current.pop(head["id"], None)
            current[head["id"]] = head
    return list(current.values())


def is_durable(answer: dict[str, Any]) -> bool:
    return "scope:durable" in (answer.get("tags") or [])


def restore_rank(answer: dict[str, Any]) -> tuple[bool, str]:
    return is_durable(answer), answer.get("updated_at", "")


def title_line(answer: dict[str, Any]) -> str:
    return f"{short_id(answer['id'])} {answer.get('title', '')}"


def excerpt(answer: dict[str, Any]) -> str:
    return f" → {clip(answer_text(answer), RESTORE_EXCERPT_CHARS)}"


def restore_digest(answers: list[dict[str, Any]], show: str, recall: str) -> list[str]:
    ranked = sorted(answers, key=restore_rank, reverse=True)
    durable = [a for a in ranked if is_durable(a)]
    header = f"Honor these answers captured this session, durable first. {show} reads one in full:"
    reserved = [header, f"+{len(ranked)} more durable answers: {recall}", f"+{len(ranked)} more answers"]
    room = COMPACT_ANSWER_BUDGET - sum(utf8_len(line) + 1 for line in reserved)
    kept: list[dict[str, Any]] = []
    for answer in durable:
        if utf8_len(title_line(answer)) + 1 > room:
            break
        kept.append(answer)
        room -= utf8_len(title_line(answer)) + 1
    lines = [header]
    excerpts = True
    for answer in kept:
        line = title_line(answer)
        if excerpts and utf8_len(tail := excerpt(answer)) <= room:
            line += tail
            room -= utf8_len(tail)
        else:
            excerpts = False
        lines.append(line)
    if len(kept) < len(durable):
        lines.append(f"+{len(durable) - len(kept)} more durable answers: {recall}")
    counted = 0
    for answer in ranked[len(durable) :]:
        if len(kept) < len(durable) or utf8_len(line := title_line(answer)) + 1 > room:
            counted += 1
            continue
        lines.append(line)
        room -= utf8_len(line) + 1
    if counted:
        lines.append(f"+{counted} more answers")
    return lines


@on(
    Event.SessionStart,
    only_if=[CompactResume(), CcNotesAvailable()],
    tests={
        Input(source="startup"): Allow(),
        Input(source="compact"): Allow(),
    },
)
def restore_answers_after_compact(evt: SessionStartEvent) -> HookResult | None:
    ids = list(evt.ctx.s.load(SessionAnswers).lines)
    if not ids:
        return None
    answers = current_answers(evt, ids)
    if not answers:
        return None
    return evt.warn(*restore_digest(answers, "`cc-notes answer show <id>`", "`cc-notes answer list --label scope:durable`"))
