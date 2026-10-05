
from __future__ import annotations

import json
import re
from datetime import UTC, datetime, timedelta
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
    PROMPT_ANSWER_BUDGET,
    CcNotesAvailable,
    SessionAnswers,
    agent_key,
    answer_line,
    clamp_title,
    durable_answers,
    fit_lines,
    json_field,
    parse_answers,
    remember_answer_lines,
    remember_answers,
    run_cc_notes,
    seen_answer_ids,
    short_id,
    unseen_answers,
    utf8_len,
)
from .compact import CompactResume
from .surface import SurfacePick

MAX_ANSWER_PATHS = 10
RECENT_ANSWER_LIMIT = 100
DEDUP_WINDOW = timedelta(hours=24)
PROMPT_ANSWERS_HEADER = "Durable answers bear on this prompt; honor them. `cc-notes answer show <id>` reads one:"
RESTORE_ANSWERS_HEADER = "Durable answers from this session; honor them:"
CAPTURE_LABELS = ("from:owner", "source:askuserquestion")
STANDING_ASK_RE = re.compile(r"\b(?:rules?|standing|always|never)\b", re.IGNORECASE)
STANDING_REPLY_RE = re.compile(r"\b(?:always|never|going forward)\b", re.IGNORECASE)

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
    asked: str
    preview: str


class AnswerVerdict(BaseModel):
    """The triage verdict for the answer at ``index``: its scope, and the candidate id it replaces."""

    index: int
    scope: Literal["durable", "ephemeral"] = "durable"
    supersedes: str = ""


class AnswerTriage(BaseModel):
    """The triage's verdicts; an answer with no verdict records as durable with no supersede."""

    verdicts: list[AnswerVerdict] = []


class AnswerCaptureLock(BaseModel):
    """Empty state whose file lock serializes one session's background answer refinements.

    A refinement lists the durable candidates, triages against them, and retires the candidate
    its verdict supersedes. Two refinements overlapping would both list candidates before either
    superseded, so both answers would stay live.
    """


class CapturedAnswers(BaseModel):
    """Answer ids the foreground capture recorded, keyed by question, awaiting background refinement."""

    ids: dict[str, str] = {}


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
        annotation = annotations.get(q["question"]) or {}
        notes = annotation.get("notes") or ""
        preview = annotation.get("preview") or ""
        options = q.get("options", [])
        pairs.append(
            AnsweredQuestion(
                question=q["question"],
                header=q.get("header", ""),
                answer=answer,
                options=[o["label"] for o in options],
                notes="" if SECRET_RE.search(notes) else notes.strip(),
                asked="\n".join([q.get("header", ""), *(f"{o['label']} {o.get('description', '')}" for o in options)]),
                preview="" if SECRET_RE.search(preview) else preview.strip(),
            )
        )
    return pairs


def typed_reply(pair: AnsweredQuestion) -> str:
    picked = [part.strip() for part in pair.answer.split(",")]
    return "" if all(label in pair.options for label in picked) else pair.answer


def heuristic_scope(pair: AnsweredQuestion) -> str:
    standing = STANDING_ASK_RE.search(pair.asked) or STANDING_REPLY_RE.search(f"{pair.notes}\n{typed_reply(pair)}")
    return "durable" if standing else "ephemeral"


def answer_body(pair: AnsweredQuestion) -> str:
    lines = [pair.answer]
    if clamp_title(pair.question) != pair.question:
        lines.append(f"Question: {pair.question}")
    if pair.options:
        lines.append("Options: " + " | ".join(pair.options))
    if pair.notes:
        lines.append(f"Notes: {pair.notes}")
    if pair.preview:
        lines.append(f"Preview: {pair.preview}")
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


def branch_args(evt: PostToolUseEvent) -> list[str]:
    branch = (evt.ctx.git("rev-parse", "--abbrev-ref", "HEAD") or "").strip()
    return ["--branch", branch] if branch and branch != "HEAD" else []


def recent_titles(evt: PostToolUseEvent) -> set[str]:
    since = datetime.now(UTC) - DEDUP_WINDOW
    rows = parse_answers(run_cc_notes(evt, "answer", "list", "--json", "--limit", str(RECENT_ANSWER_LIMIT)))
    return {a["title"] for a in rows if datetime.fromisoformat(a["updated_at"]) >= since}


def add_answer(evt: PostToolUseEvent, pair: AnsweredQuestion, anchors: list[str]) -> str:
    labels = [*CAPTURE_LABELS, f"scope:{heuristic_scope(pair)}"]
    if pair.header:
        labels.append(f"header:{pair.header}")
    flags = [arg for label in labels for arg in ("--label", label)]
    out = run_cc_notes(evt, "answer", "add", "--json", *flags, *anchors, f"--body={answer_body(pair)}", "--", clamp_title(pair.question))
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
    if not (pairs := answered_questions(evt)):
        return None
    seen = recent_titles(evt)
    anchors = branch_args(evt)
    recorded: list[dict[str, Any]] = []
    captured: dict[str, str] = {}
    for pair in pairs:
        title = clamp_title(pair.question)
        if title in seen:
            continue
        seen.add(title)
        if answer_id := add_answer(evt, pair, anchors):
            captured[pair.question] = answer_id
            recorded.append({"id": answer_id, "title": title, "body": answer_body(pair)})
    if not recorded:
        return None
    with evt.ctx.s[CapturedAnswers].mutate() as state:
        state.ids.update(captured)
    remember_answers(evt, recorded)
    noun = "answer" if len(recorded) == 1 else "answers"
    ids = ", ".join(short_id(a["id"]) for a in recorded)
    return evt.warn(f"Recorded {len(recorded)} {noun} in cc-notes ({ids}); never record them again by hand.")


def scope_edit(scope: str, verdict: AnswerVerdict | None) -> list[str]:
    if verdict is None or verdict.scope == scope:
        return []
    return ["--rm-label", f"scope:{scope}", "--add-label", f"scope:{verdict.scope}"]


def refine_captured_answers(evt: PostToolUseEvent) -> None:
    pairs = answered_questions(evt)
    with evt.ctx.s[CapturedAnswers].mutate() as state:
        captured = {p.question: state.ids.pop(p.question) for p in pairs if p.question in state.ids}
    if not (pairs := [p for p in pairs if p.question in captured]):
        return
    fresh = set(captured.values())
    candidates = [a for a in durable_answers(evt) if a["id"] not in fresh]
    verdicts = triage_answers(evt, pairs, candidates)
    paths = [arg for path in session_paths(evt) for arg in ("--add-path", path)]
    for i, pair in enumerate(pairs):
        answer_id = captured[pair.question]
        verdict = verdicts.get(i)
        if edits := [*scope_edit(heuristic_scope(pair), verdict), *paths]:
            run_cc_notes(evt, "answer", "edit", answer_id, "--json", *edits)
        old = superseded_id(verdict, candidates)
        if old and old != answer_id and run_cc_notes(evt, "answer", "supersede", old, "--by", answer_id, "--json") is not None:
            with evt.ctx.s[SessionAnswers].mutate() as state:
                state.lines.pop(old, None)


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
def refine_user_answers(evt: PostToolUseEvent) -> None:
    with evt.ctx.s[AnswerCaptureLock].mutate():
        refine_captured_answers(evt)


def title_line(answer: dict[str, Any]) -> str:
    return f"{short_id(answer['id'])} {answer.get('title', '')}"


def pick_prompt_answers(evt: UserPromptSubmitEvent, fresh: list[dict[str, Any]]) -> dict[str, str]:
    lines = {a["id"]: title_line(a) for a in fresh}
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
    seen = seen_answer_ids(evt)
    fresh = [aid for aid in staged if aid not in seen]
    shown = fit_lines([staged[aid] for aid in fresh], PROMPT_ANSWER_BUDGET - utf8_len(PROMPT_ANSWERS_HEADER) - 1)
    if not (lines := remember_answer_lines(evt, dict(zip(fresh, shown)))):
        return None
    return evt.warn(PROMPT_ANSWERS_HEADER, *lines)


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


def restore_pointer(hidden: int) -> str:
    more = f"+{hidden} more. " if hidden else ""
    return f"{more}`cc-notes answer list --label scope:durable` lists them with their answers."


def restore_digest(answers: list[dict[str, Any]]) -> list[str]:
    durable = sorted(filter(is_durable, answers), key=lambda a: a.get("updated_at", ""), reverse=True)
    if not durable:
        return []
    reserved = utf8_len(RESTORE_ANSWERS_HEADER) + utf8_len(restore_pointer(len(durable))) + 2
    kept = fit_lines([title_line(a) for a in durable], COMPACT_ANSWER_BUDGET - reserved)
    return [RESTORE_ANSWERS_HEADER, *kept, restore_pointer(len(durable) - len(kept))]


@on(
    Event.SessionStart,
    only_if=[CompactResume(), CcNotesAvailable()],
    tests={
        Input(source="startup"): Allow(),
        Input(source="compact"): Allow(),
    },
)
def restore_answers_after_compact(evt: SessionStartEvent) -> HookResult | None:
    state = evt.ctx.s.load(SessionAnswers)
    ids = [aid for aid in state.lines if state.owners.get(aid) == agent_key(evt)]
    if not ids or not (lines := restore_digest(current_answers(evt, ids))):
        return None
    return evt.warn(*lines)
