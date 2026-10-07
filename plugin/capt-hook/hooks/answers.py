
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
from spawnllm import Binary, BinaryAnswer, Label, LabelAnswer

from .common import (
    COMPACT_ANSWER_BUDGET,
    LLM_INPUT_CAP,
    PROMPT_ANSWER_BUDGET,
    CcNotesAvailable,
    SessionAnswers,
    agent_key,
    answer_line,
    answer_question,
    answer_text,
    branch_answers,
    clamp_title,
    current_branch,
    durable_answers,
    fit_lines,
    flat_clip,
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

NO_ANSWER = "none"
OPTION_DESCRIPTION_CAP = 400
DURABLE_FLOOR = 0.4
ANSWER_SCOPE_QUESTION = Binary(
    "Does the user's answer set a preference, convention, or decision that should hold beyond the task at hand?",
    yes="A standing preference, convention, rule, or design decision later work must respect.",
    no="A one-off pick that only steers the current task, such as which item to do first or whether to proceed now.",
)
ANSWER_SUPERSEDE_SYSTEM = (
    "A user just answered questions a coding agent asked. Earlier durable answers are listed as candidates. "
    "When an answer replaces a candidate that asks the same question, name that candidate's id in supersedes; "
    "otherwise leave supersedes empty. Return one verdict per answer, keyed by its index."
)
PROMPT_ANSWER_INSTRUCTIONS = (
    "The state is a prompt a user just sent a coding agent. Each option other than none is a durable answer the "
    "user gave to an earlier question on this branch. Pick the answer the agent, acting on this prompt, would have "
    "to honor because it would make the very decision that answer settled, or would contradict it. Sharing a word, "
    "a system, or a topic is not enough; status reports, notifications, and progress updates almost never bear on one."
)


class AnsweredQuestion(NamedTuple):

    question: str
    header: str
    answer: str
    options: list[str]
    notes: str
    asked: str
    preview: str


class AnswerVerdict(NamedTuple):
    """The triage verdict for one answer: its scope, None when unjudged, and the candidate id it replaces."""

    scope: Literal["durable", "ephemeral"] | None
    supersedes: str


class SupersedeVerdict(BaseModel):
    """The supersede verdict for the answer at ``index``: the candidate id it replaces, or empty."""

    index: int
    supersedes: str = ""


class SupersedeTriage(BaseModel):
    """The supersede pass's verdicts; an answer with no verdict supersedes nothing."""

    verdicts: list[SupersedeVerdict] = []


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


def answer_options(answers: list[dict[str, Any]], none: str) -> dict[str, str | None]:
    return {NO_ANSWER: none, **{a["id"]: flat_clip(f"{answer_question(a)} → {answer_text(a)}", OPTION_DESCRIPTION_CAP) for a in answers}}


def answered_state(pair: AnsweredQuestion) -> str:
    lines = [f"Question: {pair.question}"]
    if pair.header:
        lines.append(f"Header: {pair.header}")
    if pair.options:
        lines.append("Options offered: " + " | ".join(pair.options))
    lines.append(f"User's answer: {pair.answer}")
    if pair.notes:
        lines.append(f"User's notes: {pair.notes}")
    return "\n".join(lines)[:LLM_INPUT_CAP]


def answer_scope(evt: PostToolUseEvent, pair: AnsweredQuestion) -> Literal["durable", "ephemeral"] | None:
    match (decision := evt.decide(answered_state(pair), {"durable": ANSWER_SCOPE_QUESTION})) and decision.answers["durable"]:
        case BinaryAnswer(p_yes=p_yes):
            return "durable" if p_yes >= DURABLE_FLOOR else "ephemeral"
    return None


def supersede_triage(evt: PostToolUseEvent, pairs: list[AnsweredQuestion], candidates: list[dict[str, Any]]) -> dict[int, str]:
    if not candidates:
        return {}
    prompt = (
        Prompt()
        .system(ANSWER_SUPERSEDE_SYSTEM)
        .context("answers", "\n".join(f"{i}\t{p.question} → {p.answer}" for i, p in enumerate(pairs))[:LLM_INPUT_CAP])
        .context("candidates", "\n".join(f"{a['id']}\t{answer_line(a)}" for a in candidates)[:LLM_INPUT_CAP])
        .ask("For each answer index: which candidate id, if any, does it supersede?")
    )
    triage = evt.ctx.call_llm(prompt, response_model=SupersedeTriage, model="small", agent=False, transcript=False)
    return {v.index: v.supersedes for v in triage.verdicts if v.supersedes}


def triage_answers(evt: PostToolUseEvent, pairs: list[AnsweredQuestion], candidates: list[dict[str, Any]]) -> dict[int, AnswerVerdict]:
    supersedes = supersede_triage(evt, pairs, candidates)
    scopes = {i: answer_scope(evt, pair) for i, pair in enumerate(pairs)}
    return {i: AnswerVerdict(scope, supersedes.get(i, "")) for i, scope in scopes.items() if scope or i in supersedes}


def candidate_id(named: str, candidates: list[dict[str, Any]]) -> str:
    if not named:
        return ""
    matches = [a["id"] for a in candidates if a["id"].startswith(named)]
    if named in matches:
        return named
    return matches[0] if len(matches) == 1 else ""


def superseded_id(verdict: AnswerVerdict | None, candidates: list[dict[str, Any]]) -> str:
    return candidate_id(verdict.supersedes, candidates) if verdict else ""


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
    branch = current_branch(evt)
    return ["--branch", branch] if branch else []


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
    if verdict is None or verdict.scope in (None, scope):
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
        if old and old != answer_id:
            run_cc_notes(evt, "answer", "supersede", old, "--by", answer_id, "--json")


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


def pick_prompt_answer(evt: UserPromptSubmitEvent, fresh: list[dict[str, Any]]) -> dict[str, str]:
    asked = {"answer": Label(PROMPT_ANSWER_INSTRUCTIONS, answer_options(fresh, "The prompt bears on none of these answers."))}
    match (decision := evt.decide((evt.user_prompt or "")[:LLM_INPUT_CAP], asked)) and decision.answers["answer"]:
        case LabelAnswer(choice=choice) if choice != NO_ANSWER:
            return {a["id"]: title_line(a) for a in fresh if a["id"] == choice}
    return {}


@on(
    Event.UserPromptSubmit,
    only_if=[CcNotesAvailable()],
    max_fires=None,
    async_=True,
)
def stage_prompt_answers(evt: UserPromptSubmitEvent) -> None:
    if not (fresh := unseen_answers(evt, branch_answers(evt))):
        return
    if not (picked := pick_prompt_answer(evt, fresh)):
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
    ids = [aid for aid in state.lines if state.owners.get(aid, "main") == agent_key(evt)]
    if not ids or not (lines := restore_digest(current_answers(evt, ids))):
        return None
    return evt.warn(*lines)
