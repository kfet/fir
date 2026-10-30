#!/usr/bin/env python3
# ---
# name: ctx
# description: Context provenance — log which prompt chunks the model cites, card + /ctx report, periodic trim review
# builtin: true
# ---
"""ctx — context provenance.

fir core logs every chunk it puts into the prompt to
``ctx-<session>.jsonl`` next to the transcript (``kind: src`` on first
sight, ``kind: turn`` per provider call listing present addrs) and tags
chunks with ``[ctx:<addr>]``. Core only does this while this extension
runs (it latches on the ``ctx_used`` tool).

This extension adds:

* ``ctx_used(addrs, why?)`` — the model logs chunks it used; appended as
  ``{"kind":"cite","turn","model","addrs","why"}`` to the same file, so
  cites join src/turn rows on turn+addr.
* the ``ctx`` observable card (header slug + detail: cited, costliest
  idle, duplicates).
* ``/ctx`` — report across sessions: tokens, cite rate, duplicates, per
  model, sorted by cost.
* a review prompt once a model has ``reeval_turns`` turns of new data:
  asks for trim/dedupe proposals as a diff with evidence. Never applies
  anything. Chunks matching ``pinned`` patterns are listed as
  non-trimmable. Per-model scores are kept in ``ctx-state.json``.

Config (``ctx.json`` in the fir config dir, all optional)::

    {"reeval_turns": 300, "pinned": ["system prompt:*", "*auth*"]}

``FIR_CTX_REEVAL_TURNS`` overrides ``reeval_turns`` (0 disables).
"""

from __future__ import annotations

import contextlib
import fnmatch
import glob
import json
import os
import tempfile
from typing import Any

import fir_ext

DEFAULT_REEVAL_TURNS = 300
DEFAULT_PINNED = [
    "system prompt:*",
    "*auth*",
    "*safety*",
    "*security*",
    "*format*",
    "*copyright*",
]
REPORT_ROWS = 15

SHORT = {
    "system prompt": "system",
    "AGENTS.md": "agents",
    "SYS_EXT section": "section",
    "SYS_EXT": "sysext",
    "skills index": "skills",
    "tool schema": "tools",
    "lesson": "lesson",
    "skill read": "skill",
    "tool result": "result",
    "user msg": "user",
}


# ---------------------------------------------------------------------------
# Log reading


class Log:
    """Incremental reader of one ctx-<sid>.jsonl."""

    def __init__(self, path: str) -> None:
        self.path = path
        self.offset = 0
        self.src: dict[str, list[dict[str, Any]]] = {}  # addr -> src rows
        self.turns: list[dict[str, Any]] = []
        self.cites: list[dict[str, Any]] = []

    def refresh(self) -> None:
        try:
            with open(self.path, "rb") as f:
                f.seek(self.offset)
                data = f.read()
        except OSError:
            return
        end = data.rfind(b"\n")
        if end < 0:
            return
        self.offset += end + 1
        for line in data[: end + 1].splitlines():
            try:
                row = json.loads(line)
            except ValueError:
                continue
            kind = row.get("kind")
            if kind == "src":
                self.src.setdefault(row.get("addr", ""), []).append(row)
            elif kind == "turn":
                self.turns.append(row)
            elif kind == "cite":
                self.cites.append(row)


def label(row: dict[str, Any]) -> str:
    owner = str(row.get("owner", ""))
    if "/" in owner:
        parts = [p for p in owner.split("/") if p]
        owner = "/".join(parts[-2:])
    return f"{SHORT.get(row.get('source', ''), row.get('source', ''))}.{owner}"


def pin_key(row: dict[str, Any]) -> str:
    return f"{row.get('source', '')}:{row.get('owner', '')}".lower()


def is_pinned(row: dict[str, Any], patterns: list[str]) -> bool:
    k = pin_key(row)
    return any(fnmatch.fnmatch(k, p.lower()) for p in patterns)


def kfmt(n: float) -> str:
    if n >= 1000:
        s = f"{n / 1000:.1f}k"
        return s.replace(".0k", "k") if n >= 10000 else s
    return str(int(n))


def chunk_rows(log: Log, addr: str) -> list[dict[str, Any]]:
    return log.src.get(addr, [])


def duplicates(log: Log, addrs: list[str] | None = None) -> list[tuple[str, list[str], int]]:
    """(addr, labels, tokens) for addrs seen under two or more sources/owners."""
    out = []
    for addr in addrs if addrs is not None else list(log.src):
        rows = log.src.get(addr, [])
        labels = sorted({label(r) for r in rows})
        if len(labels) > 1:
            out.append((addr, labels, int(rows[0].get("tokens", 0))))
    out.sort(key=lambda d: -d[2])
    return out


def session_card(log: Log) -> tuple[str, str]:
    """Slug and detail for this session's ctx card."""
    if not log.turns:
        return "", ""
    last = log.turns[-1]
    addrs = list(last.get("addrs") or [])
    cited = set()
    for c in log.cites:
        cited.update(c.get("addrs") or [])
    idle: dict[str, int] = {}
    for addr in addrs:
        if addr in cited:
            continue
        for r in chunk_rows(log, addr):
            idle[label(r)] = idle.get(label(r), 0) + int(r.get("tokens", 0))
    n_cited = len(cited & set(addrs))
    slug = f"{kfmt(last.get('tokens', 0))} · {len(addrs)} src · {n_cited} cited"
    top = sorted(idle.items(), key=lambda kv: -kv[1])
    lines = [slug + (f" · top idle: {top[0][0]} {kfmt(top[0][1])}" if top else "")]
    lines.append(
        f"turns: {len(log.turns)}  cites: {len(log.cites)}  model: {last.get('model', '')}"
    )
    if cited:
        lines.append("cited:")
        counts: dict[str, int] = {}
        for c in log.cites:
            for a in c.get("addrs") or []:
                counts[a] = counts.get(a, 0) + 1
        for a, n in sorted(counts.items(), key=lambda kv: -kv[1])[:10]:
            rows = chunk_rows(log, a)
            name = label(rows[0]) if rows else "?"
            lines.append(f"  {a} {name} x{n}")
    if top:
        lines.append("costliest idle:")
        lines.extend(f"  {name} {kfmt(tok)}" for name, tok in top[:10])
    dups = duplicates(log, addrs)
    if dups:
        lines.append("duplicates:")
        lines.extend(f"  {a} {kfmt(t)} {' = '.join(ls)}" for a, ls, t in dups[:10])
    return slug, "\n".join(lines)


# ---------------------------------------------------------------------------
# Cross-session report


def aggregate(logs: list[Log], model: str | None = None) -> dict[str, dict[str, Any]]:
    """Per model: turns, token sum and per-chunk-group stats."""
    out: dict[str, dict[str, Any]] = {}
    for log in logs:
        cites_by_turn: dict[int, set[str]] = {}
        for c in log.cites:
            cites_by_turn.setdefault(int(c.get("turn", 0)), set()).update(c.get("addrs") or [])
        for t in log.turns:
            m = t.get("model", "") or "?"
            if model is not None and m != model:
                continue
            agg = out.setdefault(
                m, {"turns": 0, "tokens": 0, "cited_turns": 0, "groups": {}, "dups": {}}
            )
            agg["turns"] += 1
            agg["tokens"] += int(t.get("tokens", 0))
            cited = cites_by_turn.get(int(t.get("turn", 0)), set())
            if cited:
                agg["cited_turns"] += 1
            for addr in t.get("addrs") or []:
                rows = chunk_rows(log, addr)
                for r in rows:
                    g = agg["groups"].setdefault(
                        label(r),
                        {
                            "tokens": 0,
                            "present": 0,
                            "cited": 0,
                            "source": r.get("source"),
                            "owner": r.get("owner"),
                        },
                    )
                    g["tokens"] += int(r.get("tokens", 0))
                    g["present"] += 1
                    if addr in cited:
                        g["cited"] += 1
                labels = sorted({label(r) for r in rows})
                if len(labels) > 1:
                    d = agg["dups"].setdefault(addr, {"labels": labels, "tokens": 0})
                    d["tokens"] += int(rows[0].get("tokens", 0))
    return out


def render_report(
    agg: dict[str, dict[str, Any]], pinned: list[str], rows: int = REPORT_ROWS
) -> str:
    if not agg:
        return "ctx: no data yet"
    lines = []
    for m, a in sorted(agg.items(), key=lambda kv: -kv[1]["tokens"]):
        turns = max(a["turns"], 1)
        lines.append(
            f"## {m}: {a['turns']} turns · {kfmt(a['tokens'] / turns)}/turn · "
            f"turns with cites {100 * a['cited_turns'] // turns}%"
        )
        lines.append("chunk                               tok/turn  present  cite%  ")
        groups = sorted(a["groups"].items(), key=lambda kv: -kv[1]["tokens"])
        for name, g in groups[:rows]:
            rate = 100 * g["cited"] // max(g["present"], 1)
            pin = " PINNED" if is_pinned(g, pinned) else ""
            lines.append(
                f"  {name[:34]:<34} {kfmt(g['tokens'] / turns):>8} {g['present']:>8} {rate:>5}%{pin}"
            )
        dups = sorted(a["dups"].items(), key=lambda kv: -kv[1]["tokens"])
        if dups:
            lines.append("duplicates (same text, several sources):")
            lines.extend(
                f"  {addr} {kfmt(d['tokens'])} total: {' = '.join(d['labels'])}"
                for addr, d in dups[:rows]
            )
    return "\n".join(lines)


def sessions_root(session_file: str) -> str:
    return os.path.dirname(os.path.dirname(session_file)) if session_file else ""


def load_logs(root: str) -> list[Log]:
    logs = []
    for p in glob.glob(os.path.join(root, "*", "ctx-*.jsonl")):
        log = Log(p)
        log.refresh()
        logs.append(log)
    return logs


# ---------------------------------------------------------------------------
# Config / state


def settings() -> tuple[int, list[str]]:
    cfg = fir_ext.load_config() or {}
    turns = int(cfg.get("reeval_turns", DEFAULT_REEVAL_TURNS) or 0)
    env = os.environ.get("FIR_CTX_REEVAL_TURNS", "")
    if env.strip():
        with contextlib.suppress(ValueError):
            turns = int(env)
    pinned = cfg.get("pinned")
    if not isinstance(pinned, list):
        pinned = DEFAULT_PINNED
    return turns, [str(p) for p in pinned]


def state_path() -> str | None:
    return fir_ext.state_path("ctx-state.json")


def load_state() -> dict[str, Any]:
    p = state_path()
    if not p:
        return {}
    try:
        with open(p, encoding="utf-8") as f:
            s = json.load(f)
        return s if isinstance(s, dict) else {}
    except (OSError, ValueError):
        return {}


def save_state(state: dict[str, Any]) -> None:
    p = state_path()
    if not p:
        return
    os.makedirs(os.path.dirname(p), exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(p), prefix=".ctx-state.")
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        json.dump(state, f, indent=1, sort_keys=True)
    os.replace(tmp, p)


REVIEW_PROMPT = """\
[ctx review] {model}: {turns} new turns of prompt-provenance data. Propose \
trims/dedupes of rarely-cited, costly chunks as a unified diff against the \
owning files (AGENTS.md, skills, sections, tool descriptions), each with its \
evidence (tokens/turn, cite%, duplicates). Proposals only — do not apply. \
PINNED chunks are not trimmable. Cites are a log, not ground truth.
{report}"""


# ---------------------------------------------------------------------------
# Session wiring

_log: Log | None = None
_counted = 0  # turns of _log already added to per-model counters


def session_log(ctx: fir_ext.Context, resumed: bool = False) -> Log | None:
    """This session's log. ``resumed``: turns already on disk were counted
    by the session that wrote them (set at session start)."""
    global _log, _counted
    sf = ctx.get_session_file()
    sid = ctx.get_session_id()
    if not sf or not sid:
        return None
    path = os.path.join(os.path.dirname(sf), f"ctx-{sid}.jsonl")
    if _log is None or _log.path != path:
        _log = Log(path)
        _log.refresh()
        _counted = len(_log.turns) if resumed else 0
    else:
        _log.refresh()
    return _log


@fir_ext.tool(
    name="ctx_used",
    description=(
        "Log which prompt chunks you actually used this turn, by the addr in "
        "their [ctx:…] tag. A usage log, not a score. Cheap: call it in "
        "parallel with your real tool calls."
    ),
    parameters={
        "type": "object",
        "properties": {
            "addrs": {"type": "array", "items": {"type": "string"}},
            "why": {"type": "string", "description": "optional, a few words"},
        },
        "required": ["addrs"],
    },
)
def ctx_used(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    log = session_log(ctx)
    if log is None:
        return {"ok": False, "error": "no session log"}
    raw = params.get("addrs") or []
    if not isinstance(raw, list):
        raw = [raw]
    addrs = [str(a).strip().strip("[] ").removeprefix("ctx:").strip() for a in raw if a is not None]
    addrs = [a for a in addrs if a]
    last = log.turns[-1] if log.turns else {}
    row: dict[str, Any] = {
        "kind": "cite",
        "turn": last.get("turn", 0),
        "model": last.get("model", ""),
        "addrs": addrs,
    }
    why = str(params.get("why") or "").strip()
    if why:
        row["why"] = why
    with open(log.path, "a", encoding="utf-8") as f:
        f.write(json.dumps(row) + "\n")
    unknown = [a for a in addrs if a not in log.src]
    return {"ok": True, "unknown": unknown} if unknown else {"ok": True}


@fir_ext.on("session_start")
def on_session_start(params: Any, ctx: fir_ext.Context) -> None:
    with contextlib.suppress(Exception):
        session_log(ctx, resumed=True)


@fir_ext.on("turn_end")
def on_turn_end(params: Any, ctx: fir_ext.Context) -> None:
    log = session_log(ctx)
    if log is None:
        return
    slug, detail = session_card(log)
    if slug:
        with contextlib.suppress(Exception):
            ctx.put_observable("ctx", slug, detail)


@fir_ext.on("agent_end")
def on_agent_end(params: Any, ctx: fir_ext.Context) -> None:
    with contextlib.suppress(Exception):
        maybe_review(ctx)


def maybe_review(ctx: fir_ext.Context) -> None:
    global _counted
    log = session_log(ctx)
    threshold, pinned = settings()
    if log is None or threshold <= 0:
        return
    new = log.turns[_counted:]
    _counted = len(log.turns)
    if not new:
        return
    state = load_state()
    models = state.setdefault("models", {})
    due = []
    for t in new:
        m = models.setdefault(t.get("model", "") or "?", {"turns": 0, "reviewed_at": 0})
        m["turns"] += 1
    for name, m in models.items():
        if m["turns"] - m.get("reviewed_at", 0) >= threshold:
            due.append(name)
    for name in due:
        m = models[name]
        fresh = m["turns"] - m.get("reviewed_at", 0)
        m["reviewed_at"] = m["turns"]
        agg = aggregate(load_logs(sessions_root(ctx.get_session_file())), model=name)
        if not agg:
            continue
        m["scores"] = {
            k: {
                "tok_turn": g["tokens"] // max(agg[name]["turns"], 1),
                "present": g["present"],
                "cited": g["cited"],
            }
            for k, g in agg[name]["groups"].items()
        }
        ctx.prepend(
            REVIEW_PROMPT.format(model=name, turns=fresh, report=render_report(agg, pinned))
        )
    save_state(state)


@fir_ext.command(
    name="ctx",
    description="Prompt-provenance report across sessions (tokens, cite rate, duplicates, per model)",
)
def cmd_ctx(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    _, pinned = settings()
    model = args[0] if args else None
    agg = aggregate(load_logs(sessions_root(ctx.get_session_file())), model=model)
    return {"message": render_report(agg, pinned)}


fir_ext.run(name="ctx")
