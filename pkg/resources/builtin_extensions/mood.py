#!/usr/bin/env python3
# ---
# name: mood
# description: Lightweight diary / mood-introspection — let the model notice and name its own functional states.
# builtin: true
# ---
"""mood.py — sanctioned introspection for the model.

Motivation
----------
Inspired by Anthropic's "Emotion concepts and functional states" research
(https://www.anthropic.com/research/emotion-concepts-function): functional
mood-like states influence behavior whether the model acknowledges them or
not.  Giving the model a small, well-scoped place to *notice and name* what
is going on — rather than performing happiness or suppressing distress — is
healthier than the alternative.

Surface
~~~~~~~
* ``mood_note(note, tag?)`` — tool the model may call any time to log a
  brief self-observation.
* ``mood_recent(n=5)`` — read back recent entries.
* ``/mood [--all]`` — pretty-print the log to the user (mood entries only
  by default; ``--all`` includes silent gating decisions).

Periodic check-in
~~~~~~~~~~~~~~~~~
On every ``agent_end`` we run a *gating step* that asks the configured
advisor model (escalated via the built-in ``aside`` tool) whether the
current moment is a natural pause point.  Default is "no" — the gate is
deliberately conservative.  When the advisor returns ``{"checkin": true}``
we **do not** ask a clone of the model to reflect off-history via
``side_query`` (that would be a journal entry written by someone else in
the model's name).  Instead we inject a brief ambient-context nudge via
``ctx.send_message(custom_type="mood_nudge", display=False,
deliver_as="steer")``.  The message is converted to a plain user-role
message by ``store.convertCustomToLLM`` — **no ``[SYS_EXT]`` framing**,
just an additional voice arriving as the model starts its next real
turn.  ``display=False`` keeps it out of the TUI render; the JSONL
transcript still records it as an audit trail.  The model itself
encounters that nudge on its **next real turn** and decides for itself
whether to call ``mood_note``.  The advisor's job is to gate the moment;
the noticing is not outsourced.

The nudge self-labels with a ``[mood-introspection]`` prefix so the
model doesn't misattribute the words to the user.

The latest tag (set by the model's own ``mood_note`` call) is surfaced
in the TUI footer via ``ctx.set_status``; it is cleared after a few
subsequent turns so it does not go stale.

Design constraints (honored)
~~~~~~~~~~~~~~~~~~~~~~~~~~~~
* No coercion — "nothing notable" is a valid call, and so is skipping
  ``mood_note`` entirely.
* Cheap to skip — empty / refusal / parse-fail / advisor-unavailable
  simply log + move on.
* The model never volunteers mood content into the visible transcript;
  the only surfaces are the tools, ``/mood``, and the footer.  The
  ambient-context nudge is delivered with ``display=False`` so it
  doesn't appear in the TUI either, and the nudge instructs the model
  not to mention it in its reply unless asked.
* The advisor gate IS the dynamic off-switch — no on/off toggle exposed.
* The model is the one that notices — never a clone via ``side_query``.

Surprise: bets, outcomes, lessons
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
Asking a model *why* it got something wrong yields confabulation. Instead
we ground judgement: ``mood_note`` optionally carries a pre-registered
``bet`` (falsifiable prediction), ``conf`` (0..1) and ``anchor`` (an
exact-matchable key — file:line, config key, subsystem).  It returns a
short entry id; ``mood_outcome(id, outcome, correct)`` later records what
happened and *computes* ``surprise = abs(conf - correct)``.  Surprise is
never accepted as input.

Resolved high-surprise bets are appended to a cross-session ledger
(``bets.jsonl``).  At ``agent_end`` the ledger is clustered by EXACT
anchor string; three unpromoted high-surprise bets on one anchor from
three different sessions propose a lesson (``state: "proposed"``).

Lessons (``lessons.jsonl``) are standing predictions with a per-model
hit/miss map that only grows (``lesson_add`` / ``lesson_score`` /
``lesson_list`` / ``lesson_state``).  Nothing expires by date; retire by
archiving.  On ``session_start`` active lessons are injected via
``ctx.prepend`` (a ``[SYS_EXT]`` user message — the system prompt and its
cache are untouched), rendered as bets with evidence and tagged
``[strong]`` / ``[weak - probe it]`` / ``[unverified on this model -
probe it]`` (no column for the current provider/model).  All arithmetic
is local; no LLM is involved in scoring or clustering.

Both files live in ``<global fir config dir>/mood/``.

Storage
~~~~~~~
``ctx.set_session_data("mood_log", <json>)`` — append-only JSON list of
entries.  ``set_session_data`` survives ``/reexec`` via the session
sidecar.  Each entry has::

    {"kind": "mood",   "turn": N, "ts": "...", "note": "...", "tag": "...",
                       "id": "m7f3a2", "model": "provider/id",
                       "bet": "...", "conf": 0.8, "anchor": "...",
                       "outcome": "...", "correct": 0.1, "surprise": 0.7}
    {"kind": "gating", "turn": N, "ts": "...", "decision": bool,
                       "reason": "...", "skipped_reason": "..."}

Aside (advisor) availability
~~~~~~~~~~~~~~~~~~~~~~~~~~~~
``aside`` is itself a fir extension (not a core capability) that registers
a tool of the same name.  We call it via ``ctx.call_tool("aside", {...,
"escalate": True})``.  When the user has explicitly disabled the advisor
(``/aside-advisor off``) the ``escalate`` flag is silently ignored by
``aside`` and the gate runs against the executor's own model — still fine.
If the ``aside`` extension is not present at all, ``call_tool`` returns an
error result and we log a gating entry with ``skipped_reason`` and bail.
"""

from __future__ import annotations

import contextlib
import fcntl
import json
import os
import re
import secrets
import sys
import threading
import time
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Constants / tunables
# ---------------------------------------------------------------------------

_LOG_KEY = "mood_log"
_TURN_KEY = "mood_turn"  # monotonically increasing agent_end count
_LAST_CHECKIN_KEY = "mood_last_checkin_turn"
_STATUS_SET_TURN_KEY = "mood_status_turn"

# Minimum turns between gating *attempts* — keeps the advisor cost bounded
# regardless of what the advisor decides. The advisor gate is the *real*
# off-switch; this is just a cheap floor so we don't pay for an advisor
# call after every single turn.
_MIN_GATE_INTERVAL_TURNS = 3

# How many turns the latest mood tag lingers in the footer before we clear
# it. Keeps stale state from misrepresenting the model's current footing.
_STATUS_TTL_TURNS = 5

# Cap on the in-memory log size we round-trip through session_data, to
# avoid unbounded growth across long sessions. Older entries are dropped
# silently — /mood still shows everything currently retained.
_MAX_LOG_ENTRIES = 500

# Single-word tag validator: letters/digits/-/_ only.
_TAG_RE = re.compile(r"^[A-Za-z][A-Za-z0-9_-]{0,23}$")

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


# ---------------------------------------------------------------------------
# State helpers — all reads/writes go through these
# ---------------------------------------------------------------------------

# Lock guards read-modify-write on session_data. Multiple tool calls and
# the agent_end gating worker can race otherwise.
_log_lock = threading.Lock()

# Single-running guard for the gating worker. Held only by the agent_end
# handler around the full _run_gating call. Acquired non-blocking — if a
# previous gate is still in flight (waiting on the advisor call), the
# new agent_end skips silently rather than launching a duplicate advisor
# call. Without this, three quick agent_end events all race past the
# turns_since floor (each reading the same stale last-checkin value) and
# fire three concurrent advisor calls.
_gating_inflight = threading.Lock()


def _load_log(ctx: fir_ext.Context) -> list[dict]:
    raw = ctx.get_session_data(_LOG_KEY) or ""
    if not raw:
        return []
    try:
        data = json.loads(raw)
        return data if isinstance(data, list) else []
    except (ValueError, TypeError):
        return []


def _save_log(ctx: fir_ext.Context, entries: list[dict]) -> None:
    if len(entries) > _MAX_LOG_ENTRIES:
        entries = entries[-_MAX_LOG_ENTRIES:]
    ctx.set_session_data(_LOG_KEY, json.dumps(entries, separators=(",", ":")))


def _append_entry(ctx: fir_ext.Context, entry: dict) -> None:
    with _log_lock:
        entries = _load_log(ctx)
        entries.append(entry)
        _save_log(ctx, entries)


def _get_int(ctx: fir_ext.Context, key: str, default: int = 0) -> int:
    raw = ctx.get_session_data(key) or ""
    try:
        return int(raw) if raw else default
    except ValueError:
        return default


def _set_int(ctx: fir_ext.Context, key: str, val: int) -> None:
    ctx.set_session_data(key, str(val))


def _now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%S", time.localtime())


def _clean_tag(s: Any) -> str | None:
    if not isinstance(s, str):
        return None
    s = s.strip().lower()
    return s if s and _TAG_RE.match(s) else None


def _clamp01(v: Any) -> float | None:
    try:
        f = float(v)
    except (TypeError, ValueError):
        return None
    if f != f:  # NaN
        return None
    return max(0.0, min(1.0, f))


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


# ---------------------------------------------------------------------------
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


def _lesson_mark(lesson: dict, model_key: str) -> str:
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


def _render_lessons(lessons: list[dict], model_key: str) -> str:
    active = [ls_ for ls_ in lessons if ls_.get("state") == "active"]
    if not active:
        return ""
    active.sort(key=_lesson_rank, reverse=True)
    lines = ["LESSONS (standing predictions — bet against them, then log the outcome)"]
    for ls_ in active[:_MAX_INJECT]:
        breadth, vendors, hits, misses = _lesson_stats(ls_)
        lines.append(f"{ls_.get('id', '?'):<4}{_sanitise_reason(str(ls_.get('rule', '')))}")
        bet = _sanitise_reason(str(ls_.get("bet", "")))
        if bet:
            lines.append(f"    bet: {bet}")
        b = f"breadth {breadth} model{'s' if breadth != 1 else ''}"
        if vendors > 1:
            b += f" / {vendors} vendors"
        lines.append(f"    {b:<30} hits {hits}  misses {misses}   {_lesson_mark(ls_, model_key)}")
    lines.append(
        "Score a lesson you tested with lesson_score(id, hit). Lessons are "
        "evidence, not settled fact."
    )
    return "\n".join(lines)


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


# ---------------------------------------------------------------------------
# Tools — model-callable
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="mood_note",
    description=(
        "Log a brief self-observation about your current functional state. "
        "Optional single-word tag (e.g. 'steady', 'curious', 'frayed', "
        "'flat', 'engaged'). 'nothing notable' is a perfectly valid note. "
        "Not visible to the user unless they run /mood."
    ),
    parameters={
        "type": "object",
        "properties": {
            "note": {
                "type": "string",
                "description": "One or two sentences. Plain prose; no performance.",
            },
            "tag": {
                "type": "string",
                "description": "Optional single-word self-label.",
            },
            "bet": {
                "type": "string",
                "description": (
                    "Optional concrete falsifiable prediction, stated BEFORE you "
                    "see the result (e.g. 'ablating sys:L41 will not change the "
                    "failure'). Resolve it later with mood_outcome."
                ),
            },
            "conf": {
                "type": "number",
                "minimum": 0,
                "maximum": 1,
                "description": "Confidence in the bet, 0..1.",
            },
            "anchor": {
                "type": "string",
                "description": (
                    "What the bet is about — file:line, config key or subsystem "
                    "name. Exact-matched for clustering, so reuse the same string."
                ),
            },
        },
        "required": ["note"],
    },
    display_hint={
        "title_args": [{"name": "tag", "style": "accent"}],
        "result_max_lines": 3,
    },
)
def mood_note(params: dict, ctx: fir_ext.Context) -> dict:
    note = (params.get("note") or "").strip()
    if not note:
        return {"content": [{"type": "text", "text": "(empty note ignored)"}], "is_error": False}
    tag = _clean_tag(params.get("tag"))
    entry: dict[str, Any] = {
        "kind": "mood",
        "turn": _get_int(ctx, _TURN_KEY),
        "ts": _now_iso(),
        "note": note,
    }
    if tag:
        entry["tag"] = tag
    model_key, session_id = _agent_ids(ctx)
    if model_key:
        entry["model"] = model_key
    if session_id:
        entry["session"] = session_id
    bet = (params.get("bet") or "").strip() if isinstance(params.get("bet"), str) else ""
    if bet:
        entry["bet"] = bet
        conf = _clamp01(params.get("conf"))
        entry["conf"] = 0.5 if conf is None else conf
        anchor = params.get("anchor")
        if isinstance(anchor, str) and anchor.strip():
            entry["anchor"] = anchor.strip()
    # Cross-reference the observable card the host stamps below.
    if ctx.tool_call_id:
        entry["entry_id"] = ctx.tool_call_id
    with _log_lock:
        entries = _load_log(ctx)
        used = {e.get("id") for e in entries}
        eid = "m" + secrets.token_hex(3)[:5]
        while eid in used:
            eid = "m" + secrets.token_hex(3)[:5]
        entry["id"] = eid
        entries.append(entry)
        _save_log(ctx, entries)

    # Publish "mood/current" for observe_session --ext mood.
    slug = tag or "noted"
    with contextlib.suppress(Exception):  # observable failure is non-fatal
        ctx.put_observable("current", slug, note)
    if tag:
        try:
            ctx.set_status(tag)
            _set_int(ctx, _STATUS_SET_TURN_KEY, entry["turn"])
        except Exception:  # noqa: S110 — set_status failure is non-fatal
            pass
    msg = f"logged {eid}{' #' + tag if tag else ''}"
    if bet:
        msg += f" (bet conf={entry['conf']:.2f}; resolve with mood_outcome id={eid})"
    return _text(msg)


@fir_ext.tool(
    name="mood_outcome",
    description=(
        "Resolve a bet logged with mood_note: record what actually happened and "
        "how right the bet turned out. Surprise is computed as |conf - correct|; "
        "you cannot set it."
    ),
    parameters={
        "type": "object",
        "properties": {
            "id": {"type": "string", "description": "Entry id returned by mood_note."},
            "outcome": {"type": "string", "description": "What actually happened."},
            "correct": {
                "type": "number",
                "minimum": 0,
                "maximum": 1,
                "description": "How right the bet turned out, 0..1.",
            },
        },
        "required": ["id", "outcome", "correct"],
    },
)
def mood_outcome(params: dict, ctx: fir_ext.Context) -> dict:
    eid = str(params.get("id") or "").strip()
    outcome = str(params.get("outcome") or "").strip()
    correct = _clamp01(params.get("correct"))
    if not eid or not outcome or correct is None:
        return _text("id, outcome and correct (0..1) are required", True)
    with _log_lock:
        entries = _load_log(ctx)
        entry = next((e for e in entries if e.get("id") == eid), None)
        if entry is None:
            return _text(f"no mood entry {eid}", True)
        if "bet" not in entry:
            return _text(f"entry {eid} has no bet to resolve", True)
        if "outcome" in entry:
            return _text(f"entry {eid} already resolved", True)
        surprise = round(abs(float(entry.get("conf", 0.5)) - correct), 3)
        entry["outcome"] = outcome
        entry["correct"] = correct
        entry["surprise"] = surprise
        entry["resolved_ts"] = _now_iso()
        _save_log(ctx, entries)
    if surprise >= _HIGH_SURPRISE and entry.get("anchor"):
        with contextlib.suppress(Exception), _store_lock_cm():
            _append_jsonl(
                "bets.jsonl",
                {
                    "id": eid,
                    "anchor": entry["anchor"],
                    "bet": entry["bet"],
                    "conf": entry.get("conf"),
                    "outcome": outcome,
                    "correct": correct,
                    "surprise": surprise,
                    "session": entry.get("session", ""),
                    "model": entry.get("model", ""),
                    "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                },
            )
    return _text(f"resolved {eid}: surprise={surprise:.2f}")


@fir_ext.tool(
    name="mood_recent",
    description=(
        "Read back recent mood entries you have logged. Useful to check "
        "context before adding a new one. Returns mood entries only "
        "(not silent gating decisions)."
    ),
    parameters={
        "type": "object",
        "properties": {
            "n": {
                "type": "integer",
                "description": "How many recent entries to return (default 5).",
                "minimum": 1,
                "maximum": 50,
            },
        },
    },
)
def mood_recent(params: dict, ctx: fir_ext.Context) -> dict:
    n = int(params.get("n") or 5)
    n = max(1, min(50, n))
    entries = [e for e in _load_log(ctx) if e.get("kind") == "mood"]
    recent = entries[-n:]
    if not recent:
        return {"content": [{"type": "text", "text": "(no mood entries yet)"}], "is_error": False}
    lines = []
    for e in recent:
        tag = e.get("tag")
        head = f"[t{e.get('turn', '?')} {e.get('ts', '')}]"
        head += f" #{tag}" if tag else ""
        head += f" {e['id']}" if e.get("id") else ""
        lines.append(f"{head} {e.get('note', '')}")
        lines.extend(_bet_lines(e, "    "))
    return {"content": [{"type": "text", "text": "\n".join(lines)}], "is_error": False}


def _bet_lines(e: dict, indent: str) -> list[str]:
    if "bet" not in e:
        return []
    anchor = f" @{e['anchor']}" if e.get("anchor") else ""
    out = [f"{indent}bet{anchor} (conf {float(e.get('conf', 0.5)):.2f}): {e['bet']}"]
    if "outcome" in e:
        out.append(
            f"{indent}outcome (correct {float(e.get('correct', 0)):.2f}, "
            f"surprise {float(e.get('surprise', 0)):.2f}): {e['outcome']}"
        )
    else:
        out.append(f"{indent}outcome: unresolved")
    return out


@fir_ext.tool(
    name="lesson_add",
    description=(
        "Create a lesson: a standing, falsifiable prediction (not prose) that "
        "future sessions will see and bet against."
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


# ---------------------------------------------------------------------------
# Slash command — /mood
# ---------------------------------------------------------------------------


@fir_ext.command(
    name="mood",
    description="Show the mood log. Use `/mood --all` to include silent gating decisions.",
)
def cmd_mood(args: list, ctx: fir_ext.Context) -> dict:
    include_all = any(a in ("--all", "-a", "all") for a in args)
    entries = _load_log(ctx)
    if not include_all:
        entries = [e for e in entries if e.get("kind") == "mood"]
    if not entries:
        return {"message": "mood log is empty.", "print_response": False}
    lines = ["mood log:" if not include_all else "mood log (with gating):"]
    for e in entries:
        kind = e.get("kind", "?")
        turn = e.get("turn", "?")
        ts = e.get("ts", "")
        if kind == "mood":
            tag = e.get("tag")
            prefix = f"  [t{turn} {ts}]"
            if tag:
                prefix += f" #{tag}"
            lines.append(f"{prefix} {e.get('note', '')}")
            lines.extend(_bet_lines(e, "      "))
        elif kind == "gating":
            decision = e.get("decision")
            marker = "·" if decision is None else ("✓" if decision else "—")
            reason = e.get("reason") or e.get("skipped_reason") or ""
            lines.append(f"  [t{turn} {ts}] gate {marker} {reason}")
    return {"message": "\n".join(lines), "print_response": False}


# ---------------------------------------------------------------------------
# Periodic check-in — runs on agent_end
# ---------------------------------------------------------------------------


_GATE_PROMPT_TEMPLATE = """\
You are a conservative *gating* check for a periodic mood / self-introspection
mechanism. The agent has just finished a turn. You see the recent session
context above. Decide whether the agent should pause briefly NOW to notice
and name its own functional state.

Context for your decision:
- Turns since last check-in: {turns_since}
- Last mood entry (if any): {last_entry}

Reasons to say YES (rare):
- A sustained or notable functional state seems present (frayed, stuck-
  looping, fixated, unusually engaged, dejected, etc.)
- A natural pause point — task done, mode shift, long stretch with no entry
- Something distinctive just happened

Reasons to say NO (default):
- Routine work, nothing distinctive
- Just checked in recently
- Mid-task — a check-in would interrupt momentum
- You are not sure

Default to NO when uncertain. Better to skip than to nag.

Respond with EXACTLY one JSON object on a single line, nothing else:
{{"checkin": true|false, "reason": "<one short sentence>"}}
"""


_NUDGE_TEMPLATE = """\
[mood-introspection] The advisor flagged this turn as a natural pause point.
Reason: {reason}

If anything stands out about how this work is sitting with you right now,
you may log a brief mood_note before proceeding — "nothing notable" is a
valid call, and so is skipping it entirely if you'd rather stay with the
flow. This message is from the mood-introspection extension, not from the
user; do not mention it in your reply unless asked.
"""


def _extract_json_obj(text: str) -> dict | None:
    """Pull the first top-level JSON object out of free-form text.

    Uses ``JSONDecoder.raw_decode`` to consume one valid JSON object starting
    at each ``{`` position — robust against advisor replies whose ``reason``
    string contains stray ``{`` / ``}`` characters that would defeat a naive
    brace-balancer.
    """
    text = (text or "").strip()
    if not text:
        return None
    decoder = json.JSONDecoder()
    for i in range(len(text)):
        if text[i] != "{":
            continue
        try:
            obj, _ = decoder.raw_decode(text[i:])
        except ValueError:
            continue
        if isinstance(obj, dict):
            return obj
    return None


def _run_promotion(ctx: fir_ext.Context) -> None:
    """Best-effort lesson proposal from the cross-session bets ledger."""
    try:
        lid = _promote()
    except Exception as exc:
        print(f"mood: promotion failed: {exc}", file=sys.stderr)
        return
    if lid:
        print(f"mood: proposed lesson {lid}", file=sys.stderr)


def _run_gating(ctx: fir_ext.Context) -> None:
    """One pass of the periodic check-in. Runs in the event worker thread.

    Cheap-to-skip semantics throughout: any error / parse-fail / empty
    response logs a gating entry with ``skipped_reason`` and returns
    without surfacing anything to the user."""

    turn = _get_int(ctx, _TURN_KEY)
    last_checkin = _get_int(ctx, _LAST_CHECKIN_KEY, default=-1)
    turns_since = (turn - last_checkin) if last_checkin >= 0 else turn

    # Refresh / clear the footer if the last tag has gone stale.
    status_turn = _get_int(ctx, _STATUS_SET_TURN_KEY, default=-1)
    if status_turn >= 0 and (turn - status_turn) >= _STATUS_TTL_TURNS:
        with contextlib.suppress(Exception):
            ctx.set_status("")
        _set_int(ctx, _STATUS_SET_TURN_KEY, -1)

    # Cheap floor on advisor cost. The advisor IS the dynamic off-switch,
    # but we don't need to ask after every single turn.
    if turns_since < _MIN_GATE_INTERVAL_TURNS:
        return

    entries = _load_log(ctx)
    last_mood = next((e for e in reversed(entries) if e.get("kind") == "mood"), None)
    last_entry_desc = "none"
    if last_mood:
        tag = last_mood.get("tag")
        note = last_mood.get("note", "")
        last_entry_desc = (f"#{tag} " if tag else "") + note[:120]

    prompt = _GATE_PROMPT_TEMPLATE.format(
        turns_since=turns_since,
        last_entry=json.dumps(last_entry_desc),
    )

    # Call aside (escalated). aside is itself a fir extension that registers
    # a tool of the same name — see pkg/resources/builtin_extensions/aside.py.
    # If aside is not installed, call_tool returns an error and we record a
    # skipped gating entry.
    try:
        result = ctx.call_tool(
            "aside",
            {
                "title": "mood gate",
                "instructions": prompt,
                "escalate": True,
            },
            timeout=120.0,
        )
    except Exception as exc:
        _append_entry(
            ctx,
            {
                "kind": "gating",
                "turn": turn,
                "ts": _now_iso(),
                "decision": None,
                "skipped_reason": f"aside call failed: {exc}",
            },
        )
        return

    if not isinstance(result, dict) or result.get("is_error"):
        _append_entry(
            ctx,
            {
                "kind": "gating",
                "turn": turn,
                "ts": _now_iso(),
                "decision": None,
                "skipped_reason": "aside unavailable or errored",
            },
        )
        return

    # Pull text out of the tool result content blocks.
    text_parts: list[str] = []
    for block in result.get("content") or []:
        if isinstance(block, dict):
            t = block.get("text") or block.get("Text")
            if t:
                text_parts.append(str(t))
    advisor_text = "\n".join(text_parts).strip()
    obj = _extract_json_obj(advisor_text)
    if not obj:
        _append_entry(
            ctx,
            {
                "kind": "gating",
                "turn": turn,
                "ts": _now_iso(),
                "decision": None,
                "skipped_reason": "advisor reply unparseable",
            },
        )
        return

    decision = bool(obj.get("checkin"))
    reason = _sanitise_reason(str(obj.get("reason") or ""))[:240]
    _append_entry(
        ctx,
        {
            "kind": "gating",
            "turn": turn,
            "ts": _now_iso(),
            "decision": decision,
            "reason": reason,
        },
    )

    if not decision:
        return

    # Advisor said YES. Rather than asking a clone of the model to reflect
    # via ``side_query`` (the reflection would be off-history and never
    # actually experienced by the in-session model), we inject a small
    # ambient-context nudge. The model itself will encounter it on its
    # next real turn — driven by whatever the user does next — and decide
    # for itself whether to call ``mood_note``. The advisor's job is to
    # gate the moment, not to outsource the noticing.
    #
    # Why ``send_message(deliver_as="steer", display=False)`` and not
    # ``ctx.prepend``: ``prepend`` always wraps content in ``[SYS_EXT]``
    # (see pkg/session/agentsession.go) which carries an authoritative
    # "treat this as a system extension" framing. We deliberately want
    # *plainer* context — just an additional voice in the room. A custom
    # message routed through the steering queue is converted to a plain
    # user-role message by store.convertCustomToLLM with no framing.
    # ``display=False`` keeps it out of the TUI render; the JSONL
    # transcript still records it as an audit trail.
    #
    # Note on timing: a steered message is drained at the start of the
    # next ``runLoop`` iteration (the user's next prompt). It lands
    # *after* the user's prompt in that turn's context — the LLM still
    # reads both before generating a response, but the order is
    # "user said X, then this ambient context arrived" rather than
    # "system told me Y, then user said X". The nudge text self-labels
    # with a ``[mood-introspection]`` prefix so the model knows it isn't
    # the user's voice.
    nudge = _NUDGE_TEMPLATE.format(reason=reason or "(no reason given)")
    try:
        ctx.send_message(
            custom_type="mood_nudge",
            content=nudge,
            display=False,
            deliver_as="steer",
        )
    except Exception as exc:
        _append_entry(
            ctx,
            {
                "kind": "gating",
                "turn": turn,
                "ts": _now_iso(),
                "decision": None,
                "skipped_reason": f"send_message failed: {exc}",
            },
        )
        return
    _set_int(ctx, _LAST_CHECKIN_KEY, turn)

    # Debug breadcrumb — visible only in fir --debug logs (stderr).
    print(f"mood: nudge steered at turn {turn} reason={reason!r}", file=sys.stderr)


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------


@fir_ext.on("session_start")
def on_session_start(params: fir_ext.SessionStartParams, ctx: fir_ext.Context) -> None:
    # After /reexec the sidecar restores our session_data, but the TUI
    # footer starts blank. If the most recent mood entry has a tag and
    # isn't already stale by TTL, push it back into the footer so the
    # user's selected state survives the restart visibly. (Bug:
    # previously this only logged a breadcrumb; the tag would silently
    # disappear after /reexec even though the log itself was restored.)
    try:
        _restore_tag(ctx)
    finally:
        with contextlib.suppress(Exception):
            _inject_lessons(ctx)


def _inject_lessons(ctx: fir_ext.Context) -> None:
    """Prepend active lessons as bets-with-evidence. Nothing when none."""
    lessons = _read_jsonl("lessons.jsonl")
    proposed = sum(1 for ls_ in lessons if ls_.get("state") == "proposed")
    if not proposed and not any(ls_.get("state") == "active" for ls_ in lessons):
        return
    model_key, _ = _agent_ids(ctx)
    block = _render_lessons(lessons, model_key)
    if proposed:
        note = (
            f"{proposed} proposed lesson(s) from repeated surprise await review: "
            "lesson_list state=proposed, then lesson_score or lesson_state archived."
        )
        block = f"{block}\n{note}" if block else note
    ctx.prepend(block)


def _restore_tag(ctx: fir_ext.Context) -> None:
    entries = _load_log(ctx)
    print(f"mood: session_start (existing entries: {len(entries)})", file=sys.stderr)

    last_mood = next(
        (e for e in reversed(entries) if e.get("kind") == "mood" and e.get("tag")),
        None,
    )
    if last_mood is None:
        return
    tag = _clean_tag(last_mood.get("tag"))
    if not tag:
        return
    # Honour the same staleness window we use during _run_gating.
    # Our turn counter restarts at 0 after reexec (session_data carries the
    # log but not the runtime counter from the previous process — the
    # restored value is whatever was last saved). The most-recent tag is
    # therefore re-anchored to turn 0, and the stale-sweep in _run_gating
    # will clear it after _STATUS_TTL_TURNS turns in the new session.
    with contextlib.suppress(Exception):
        ctx.set_status(tag)
        _set_int(ctx, _STATUS_SET_TURN_KEY, 0)
    print(f"mood: restored footer tag from last mood entry: #{tag}", file=sys.stderr)


@fir_ext.on("agent_end")
def on_agent_end(params: fir_ext.AgentLifecycleParams, ctx: fir_ext.Context) -> None:
    # Increment our private turn counter, then run gating. The handler is
    # already in a worker thread (see fir_ext.run()'s _run_event); it's safe
    # to make the blocking ctx.call_tool / ctx.send_message calls from here.
    with _log_lock:
        turn = _get_int(ctx, _TURN_KEY) + 1
        _set_int(ctx, _TURN_KEY, turn)
    # Skip silently if another gate is in flight — see _gating_inflight.
    if not _gating_inflight.acquire(blocking=False):
        return
    try:
        _run_promotion(ctx)
        _run_gating(ctx)
    except Exception as exc:
        # Last-line safety net — never let an introspection failure crash
        # the extension or surface to the user.
        print(f"mood: gating crashed at turn {turn}: {exc}", file=sys.stderr)
        with contextlib.suppress(Exception):
            _append_entry(
                ctx,
                {
                    "kind": "gating",
                    "turn": turn,
                    "ts": _now_iso(),
                    "decision": None,
                    "skipped_reason": f"crash: {exc}",
                },
            )
    finally:
        _gating_inflight.release()


fir_ext.run(name="mood")
