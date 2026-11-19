#!/usr/bin/env python3
# ---
# name: lessons
# description: Standing, falsifiable lessons shared across sessions — lesson_* tools plus a persistent fir section.
# builtin: true
# ---
"""lessons.py — cross-session lessons (split out of mood.py).

Lessons (``lessons.jsonl``) are standing predictions with a per-model
hit/miss map that only grows (``lesson_add`` / ``lesson_score`` /
``lesson_list`` / ``lesson_state``).  Nothing expires by date; retire by
archiving.  Active lessons are published as this extension's persistent
fir section (``ctx.set_section``).

At ``agent_end`` the bets ledger (``bets.jsonl``, written by the mood
extension's ``mood_outcome``) is clustered by EXACT anchor; three
unpromoted high-surprise bets on one anchor from three sessions propose
a lesson.  All arithmetic is local; no LLM is involved.

Files live in ``<global fir config dir>/mood/`` (path kept from before
the split).  Disable with ``-d lessons`` or ``"disabledExtensions":
["lessons"]`` in settings.json; a disabled extension's section is not
emitted.
"""

from __future__ import annotations

import contextlib
import fcntl
import json
import os
import re
import sys
import threading
import time

import fir_ext

# Strip any literal [SYS_EXT] / [/SYS_EXT] marker (case-insensitive, with
# optional whitespace inside the brackets) from advisor-supplied text
# before we splice it into the nudge. Originally needed because the nudge
# was [SYS_EXT]-wrapped; still needed now because fir's base system
# prompt instructs the model to treat [SYS_EXT] content as authoritative
# wherever it appears in history — a stray marker in a plain user-role
# message could still be exploited. An unaligned / jailbroken /
# hallucinating advisor model returning crafted markers is a real
# prompt-injection vector even though the advisor is user-chosen, since
# the advisor is just an LLM and not a trusted oracle. Cheap defence in
# depth.
_SYS_EXT_MARKER_RE = re.compile(r"\[\s*/?\s*SYS_EXT\s*\]", re.IGNORECASE)


def _sanitise_reason(s: str) -> str:
    """Defang advisor-supplied free text before splicing it into a nudge.

    * Strip ``[SYS_EXT]`` / ``[/SYS_EXT]`` markers (any case / whitespace).
    * Collapse newlines + tabs to single spaces — ``reason`` is a one-sentence
      field, multiline content suggests either bad advisor output or
      injection-shaping.
    * Re-trim and cap length (extension caller already caps at 240).
    """
    if not s:
        return ""
    cleaned = _SYS_EXT_MARKER_RE.sub("", s)
    cleaned = re.sub(r"[\n\r\t]+", " ", cleaned)
    return re.sub(r"\s{2,}", " ", cleaned).strip()


def _agent_ids(ctx: fir_ext.Context) -> tuple[str, str]:
    """Return (model_key, session_id); empty strings when unavailable."""
    try:
        info = ctx.agent_info() or {}
    except Exception:
        return "", ""
    model = info.get("model") or {}
    sess = info.get("session") or {}
    key = ""
    if isinstance(model, dict) and model.get("id"):
        prov = model.get("provider") or ""
        key = f"{prov}/{model['id']}" if prov else str(model["id"])
    sid = str(sess.get("id") or "") if isinstance(sess, dict) else ""
    return key, sid


# Cross-session stores — bets ledger + lessons
# ---------------------------------------------------------------------------

# Surprise at or above this counts as "high" for promotion clustering.
_HIGH_SURPRISE = 0.5
# Distinct sessions needed on one anchor before a lesson is proposed.
_PROMOTE_SESSIONS = 3
# Max lessons injected at session_start.
_MAX_INJECT = 12
_LESSON_STATES = ("active", "proposed", "archived")

_thread_lock = threading.Lock()


@contextlib.contextmanager
def _store_lock_cm():
    """Serialise store read-modify-write across threads AND fir processes —
    every session runs its own mood process against the same files."""
    with _thread_lock:
        d = _store_dir()
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, ".lock"), "a") as fh:
            fcntl.flock(fh, fcntl.LOCK_EX)
            try:
                yield
            finally:
                fcntl.flock(fh, fcntl.LOCK_UN)


def _store_dir() -> str:
    """Global (lowest-priority) fir config dir + /mood."""
    dirs = fir_ext.config_dirs
    base = os.path.expanduser(dirs[-1]) if dirs else os.path.expanduser("~/.config/fir")
    return os.path.join(base, "mood")


def _read_jsonl(name: str) -> list[dict]:
    path = os.path.join(_store_dir(), name)
    out: list[dict] = []
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                with contextlib.suppress(ValueError):
                    obj = json.loads(line)
                    if isinstance(obj, dict):
                        out.append(obj)
    except OSError:
        pass
    return out


def _write_jsonl(name: str, rows: list[dict]) -> None:
    d = _store_dir()
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, name)
    tmp = f"{path}.{os.getpid()}.tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, separators=(",", ":")) + "\n")
    os.replace(tmp, path)


def _append_jsonl(name: str, row: dict) -> None:
    d = _store_dir()
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, name), "a", encoding="utf-8") as f:
        f.write(json.dumps(row, separators=(",", ":")) + "\n")


def _next_lesson_id(lessons: list[dict]) -> str:
    n = 0
    for ls_ in lessons:
        m = re.match(r"^L(\d+)$", str(ls_.get("id", "")))
        if m:
            n = max(n, int(m.group(1)))
    return f"L{n + 1}"


def _vendor(model_key: str) -> str:
    parts = model_key.split("/")
    return parts[-2] if len(parts) >= 3 else parts[0]


def _lesson_stats(lesson: dict) -> tuple[int, int, int, int]:
    """(breadth, vendors, hits, misses)."""
    models = lesson.get("models") or {}
    hits = sum(int(v.get("hits", 0)) for v in models.values() if isinstance(v, dict))
    misses = sum(int(v.get("misses", 0)) for v in models.values() if isinstance(v, dict))
    vendors = {_vendor(k) for k in models}
    return len(models), len(vendors), hits, misses


def _lesson_mark(lesson: dict, model_key: str = "") -> str:
    """Evidence tag. With model_key (per-session views only, never the
    shared section) a lesson untested on that model is flagged unverified."""
    breadth, _, hits, misses = _lesson_stats(lesson)
    if model_key and model_key not in (lesson.get("models") or {}):
        return "[unverified on this model - probe it]"
    total = hits + misses
    if breadth >= 2 and hits >= 4 and total and hits / total >= 0.75:
        return "[strong]"
    return "[weak - probe it]"


def _lesson_rank(lesson: dict) -> tuple:
    breadth, vendors, hits, misses = _lesson_stats(lesson)
    return (vendors, breadth, hits - misses, hits)


def _render_lessons(lessons: list[dict], limit: int = _MAX_INJECT) -> str:
    """Model-independent render: the section is shared by every session and
    model, so it must not depend on who is reading it."""
    active = [ls_ for ls_ in lessons if ls_.get("state") == "active"]
    if not active:
        return ""
    active.sort(key=_lesson_rank, reverse=True)
    lines = ["LESSONS (standing predictions — bet against them, then log the outcome)"]
    for ls_ in active[:limit]:
        breadth, vendors, hits, misses = _lesson_stats(ls_)
        lines.append(f"{ls_.get('id', '?'):<4}{_sanitise_reason(str(ls_.get('rule', '')))}")
        bet = _sanitise_reason(str(ls_.get("bet", "")))
        if bet:
            lines.append(f"    bet: {bet}")
        b = f"breadth {breadth} model{'s' if breadth != 1 else ''}"
        if vendors > 1:
            b += f" / {vendors} vendors"
        lines.append(f"    {b:<30} hits {hits}  misses {misses}   {_lesson_mark(ls_)}")
        tested = sorted((ls_.get("models") or {}).keys())
        if tested:
            lines.append(f"    tested on: {', '.join(tested)}")
    lines.append(
        "A lesson not tested on your model is unverified for you - probe it. "
        "Score a lesson you tested with lesson_score(id, hit). Lessons are "
        "evidence, not settled fact."
    )
    return "\n".join(lines)


def _render_section(lessons: list[dict], limit: int = _MAX_INJECT) -> str:
    """Full mood section text: active lessons plus a proposed-review note."""
    block = _render_lessons(lessons, limit)
    proposed = sum(1 for ls_ in lessons if ls_.get("state") == "proposed")
    if proposed:
        note = (
            f"{proposed} proposed lesson(s) from repeated surprise await review: "
            "lesson_list state=proposed, then lesson_score or lesson_state archived."
        )
        block = f"{block}\n{note}" if block else note
    return block


def _sync_section(ctx: fir_ext.Context) -> None:
    """Publish lessons as this extension's persistent fir section.

    Fir reads the section from disk before the first turn (so ``fir -p``
    sees it) and re-injects it only when its content changes. If the render
    exceeds fir's size cap, fewer lessons are published (strongest first).
    """
    lessons = _read_jsonl("lessons.jsonl")
    limit = _MAX_INJECT
    while True:
        text = _render_section(lessons, limit)
        if not text:
            ctx.clear_section()
            return
        try:
            ctx.set_section(text)
            return
        except RuntimeError:
            if limit <= 1:
                raise
            limit -= 1


def _publish(ctx: fir_ext.Context) -> None:
    """Best-effort _sync_section after lessons.jsonl changed."""
    try:
        _sync_section(ctx)
    except Exception as exc:
        print(f"lessons: set_section failed: {exc}", file=sys.stderr)


def _promote() -> str | None:
    """Cluster unpromoted high-surprise bets by exact anchor. Returns the id
    of a newly proposed lesson, or None. Pure arithmetic — no LLM."""
    with _store_lock_cm():
        bets = _read_jsonl("bets.jsonl")
        clusters: dict[str, list[dict]] = {}
        for b in bets:
            if b.get("promoted") or float(b.get("surprise", 0)) < _HIGH_SURPRISE:
                continue
            anchor = b.get("anchor")
            if anchor:
                clusters.setdefault(anchor, []).append(b)
        for anchor, rows in clusters.items():
            sessions = {r.get("session") or "" for r in rows}
            sessions.discard("")
            if len(sessions) < _PROMOTE_SESSIONS:
                continue
            lessons = _read_jsonl("lessons.jsonl")
            lid = _next_lesson_id(lessons)
            latest = rows[-1]
            lessons.append(
                {
                    "id": lid,
                    "rule": f"repeated surprise at {anchor} ({len(rows)} bets, "
                    f"{len(sessions)} sessions) — restate as a rule",
                    "bet": str(latest.get("bet", "")),
                    "anchor_pattern": anchor,
                    "learned_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                    "models": {},
                    "state": "proposed",
                    "evidence": [r.get("id") for r in rows],
                }
            )
            _write_jsonl("lessons.jsonl", lessons)
            for r in rows:
                r["promoted"] = lid
            _write_jsonl("bets.jsonl", bets)
            return lid
    return None


def _text(msg: str, is_error: bool = False) -> dict:
    return {"content": [{"type": "text", "text": msg}], "is_error": is_error}


@fir_ext.tool(
    name="lesson_add",
    description=(
        "Create a lesson: a standing, falsifiable prediction (not prose) that "
        "future sessions will see and bet against. A :graduate: reaction (Zulip's "
        "name; also :mortar_board:) from the user on a message is an agreed trigger: "
        "read that message and call lesson_add (or fix the lesson it corrects) in the "
        "same turn. A note, runbook or memory file does not count. Reply briefly with "
        "the lesson id instead of staying silent."
    ),
    parameters={
        "type": "object",
        "properties": {
            "rule": {"type": "string", "description": "Short statement of the rule."},
            "bet": {"type": "string", "description": "The falsifiable prediction it implies."},
            "anchor_pattern": {
                "type": "string",
                "description": "Exact anchor string the lesson is about.",
            },
        },
        "required": ["rule", "bet", "anchor_pattern"],
    },
)
def lesson_add(params: dict, ctx: fir_ext.Context) -> dict:
    rule = str(params.get("rule") or "").strip()
    bet = str(params.get("bet") or "").strip()
    anchor = str(params.get("anchor_pattern") or "").strip()
    if not rule or not bet or not anchor:
        return _text("rule, bet and anchor_pattern are required", True)
    with _store_lock_cm():
        lessons = _read_jsonl("lessons.jsonl")
        lid = _next_lesson_id(lessons)
        lessons.append(
            {
                "id": lid,
                "rule": rule,
                "bet": bet,
                "anchor_pattern": anchor,
                "learned_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "models": {},
                "state": "active",
            }
        )
        _write_jsonl("lessons.jsonl", lessons)
    _publish(ctx)
    return _text(f"added {lid}")


@fir_ext.tool(
    name="lesson_score",
    description=(
        "Record whether a lesson's bet held (hit) or failed (miss) for the "
        "CURRENT model. Scoring a proposed lesson activates it."
    ),
    parameters={
        "type": "object",
        "properties": {
            "id": {"type": "string", "description": "Lesson id, e.g. L3."},
            "hit": {"type": "boolean", "description": "True if the bet held."},
        },
        "required": ["id", "hit"],
    },
)
def lesson_score(params: dict, ctx: fir_ext.Context) -> dict:
    lid = str(params.get("id") or "").strip()
    hit = params.get("hit")
    if not lid or not isinstance(hit, bool):
        return _text("id and boolean hit are required", True)
    model_key, _ = _agent_ids(ctx)
    if not model_key:
        return _text("current model unknown; cannot score", True)
    with _store_lock_cm():
        lessons = _read_jsonl("lessons.jsonl")
        lesson = next((ls_ for ls_ in lessons if ls_.get("id") == lid), None)
        if lesson is None:
            return _text(f"no lesson {lid}", True)
        models = lesson.setdefault("models", {})
        col = models.setdefault(model_key, {"hits": 0, "misses": 0})
        col["hits" if hit else "misses"] = int(col.get("hits" if hit else "misses", 0)) + 1
        if lesson.get("state") == "proposed":
            lesson["state"] = "active"
        _write_jsonl("lessons.jsonl", lessons)
    _publish(ctx)
    return _text(f"{lid} {model_key}: hits {col['hits']} misses {col['misses']}")


@fir_ext.tool(
    name="lesson_state",
    description=(
        "Change a lesson's state: active, proposed or archived. Archive to "
        "retire — lessons are never deleted."
    ),
    parameters={
        "type": "object",
        "properties": {
            "id": {"type": "string"},
            "state": {"type": "string", "enum": list(_LESSON_STATES)},
        },
        "required": ["id", "state"],
    },
)
def lesson_state(params: dict, ctx: fir_ext.Context) -> dict:
    lid = str(params.get("id") or "").strip()
    state = params.get("state")
    if state not in _LESSON_STATES:
        return _text(f"state must be one of {', '.join(_LESSON_STATES)}", True)
    with _store_lock_cm():
        lessons = _read_jsonl("lessons.jsonl")
        lesson = next((ls_ for ls_ in lessons if ls_.get("id") == lid), None)
        if lesson is None:
            return _text(f"no lesson {lid}", True)
        lesson["state"] = state
        _write_jsonl("lessons.jsonl", lessons)
    _publish(ctx)
    return _text(f"{lid} -> {state}")


@fir_ext.tool(
    name="lesson_list",
    description="List lessons with their per-model evidence. Optional state filter.",
    parameters={
        "type": "object",
        "properties": {"state": {"type": "string", "enum": list(_LESSON_STATES)}},
    },
)
def lesson_list(params: dict, ctx: fir_ext.Context) -> dict:
    state = params.get("state")
    lessons = _read_jsonl("lessons.jsonl")
    if state:
        lessons = [ls_ for ls_ in lessons if ls_.get("state") == state]
    if not lessons:
        return _text("(no lessons)")
    model_key, _ = _agent_ids(ctx)
    lines = []
    for ls_ in lessons:
        lines.append(
            f"{ls_.get('id')} [{ls_.get('state')}] @{ls_.get('anchor_pattern', '')} "
            f"{ls_.get('rule', '')} {_lesson_mark(ls_, model_key)}"
        )
        lines.append(f"    bet: {ls_.get('bet', '')}")
        for mk, col in (ls_.get("models") or {}).items():
            lines.append(f"    {mk}: hits {col.get('hits', 0)} misses {col.get('misses', 0)}")
    return _text("\n".join(lines))


def _run_promotion(ctx: fir_ext.Context) -> None:
    """Best-effort lesson proposal from the cross-session bets ledger."""
    try:
        lid = _promote()
    except Exception as exc:
        print(f"lessons: promotion failed: {exc}", file=sys.stderr)
        return
    if lid:
        print(f"lessons: proposed lesson {lid}", file=sys.stderr)
        _publish(ctx)


def _sync_quiet(ctx: fir_ext.Context) -> None:
    with contextlib.suppress(Exception):
        _sync_section(ctx)


@fir_ext.on("session_start")
def on_session_start(params: fir_ext.SessionStartParams, ctx: fir_ext.Context) -> None:
    # Self-heal: republish when lessons.jsonl changed outside a tool call.
    _sync_quiet(ctx)


@fir_ext.on("agent_end")
def on_agent_end(params: fir_ext.AgentLifecycleParams, ctx: fir_ext.Context) -> None:
    _run_promotion(ctx)


fir_ext.run(name="lessons")
