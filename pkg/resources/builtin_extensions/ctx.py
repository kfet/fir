#!/usr/bin/env python3
# ---
# name: ctx
# description: Context provenance — log which prompt chunks the model cites, card + /ctx report, findings hints
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
* findings — trim/dedupe candidates, gated by trust: duplicates (same
  text under several owners) and cost outliers (large chunk on most
  turns) always; "never cited" only once >=5% of turns carry cites.
  Tool results and user messages are stats only, never candidates.
* a one-line ``[ctx]`` hint (max one per agent turn, once per finding per
  session) when the agent reads/edits a file that owns a finding:
  AGENTS.md, a SKILL.md, or an extension source (by frontmatter name).
* ``ctx_report(owner?, action?)`` — detail, dismiss or snooze.
* a one-line ``[interrupt:ctx]`` nudge at agent_end, once per new or
  changed finding. Never prepends, never starts a turn.

Config (``ctx.json`` in the fir config dir, all optional)::

    {"nudge": true, "pinned": ["system prompt:*", "*auth*"]}

``FIR_CTX_NUDGE=0`` disables the nudge. Nudge/dismiss/snooze state lives
in the global ``ctx-state.json``.
"""

from __future__ import annotations

import contextlib
import fnmatch
import glob
import json
import os
import tempfile
import time
from typing import Any

import fir_ext

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


# ---------------------------------------------------------------------------
# Findings
#
# A finding is a trim/dedupe candidate owned by one chunk group (label).
# Trust gates which kinds exist:
#   dup  — same text under several owners; always valid.
#   cost — a large chunk present on most turns; valid.
#   rare — present often, never cited; only once cite coverage is meaningful,
#          because models rarely call ctx_used.
# Tool results and user messages stay in the stats but are never candidates.

NON_TRIM = {"tool result", "user msg"}
COST_MIN_TOK = 3000  # chunk size (tokens per appearance)
COST_MIN_PRESENT = 0.5  # fraction of turns
RARE_MIN_COVERAGE = 0.05  # fraction of turns with any cite
RARE_MIN_TOK = 1000
RARE_MIN_PRESENT = 20
FINDINGS_TTL = 600.0  # seconds between cross-session recomputes
EXT_SOURCES = {"tool schema", "SYS_EXT section", "SYS_EXT", "lesson"}
READ_TOOLS = {"read", "edit", "write", "multiedit"}


def totals(agg: dict[str, dict[str, Any]]) -> dict[str, Any]:
    """Merge per-model aggregates into one."""
    out: dict[str, Any] = {"turns": 0, "cited_turns": 0, "groups": {}, "dups": {}}
    for a in agg.values():
        out["turns"] += a["turns"]
        out["cited_turns"] += a["cited_turns"]
        for name, g in a["groups"].items():
            t = out["groups"].setdefault(name, dict(g, tokens=0, present=0, cited=0))
            for k in ("tokens", "present", "cited"):
                t[k] += g[k]
        for addr, d in a["dups"].items():
            t = out["dups"].setdefault(addr, {"labels": d["labels"], "tokens": 0})
            t["tokens"] += d["tokens"]
    return out


def findings(agg: dict[str, dict[str, Any]], pinned: list[str]) -> dict[str, dict[str, Any]]:
    """id -> finding {id, kind, label, source, owner, sig, tokens, text}."""
    tot = totals(agg)
    turns = max(tot["turns"], 1)
    groups = tot["groups"]
    trim = {n: g for n, g in groups.items() if g.get("source") not in NON_TRIM}
    out: dict[str, dict[str, Any]] = {}

    def add(kind: str, name: str, sig: str, tokens: int, text: str) -> None:
        g = trim[name]
        fid = f"{kind}:{name}"
        out[fid] = {
            "id": fid,
            "kind": kind,
            "label": name,
            "source": g.get("source", ""),
            "owner": g.get("owner", ""),
            "sig": sig,
            "tokens": int(tokens),
            "text": text,
        }

    per: dict[str, dict[str, Any]] = {}
    for d in tot["dups"].values():
        labels = [n for n in d["labels"] if n in trim]
        if len(labels) < 2:
            continue
        for n in labels:
            e = per.setdefault(n, {"chunks": 0, "tokens": 0, "with": set()})
            e["chunks"] += 1
            e["tokens"] += d["tokens"]
            e["with"].update(x for x in labels if x != n)
    for n, e in per.items():
        partners = sorted(e["with"])
        add(
            "dup",
            n,
            f"{e['chunks']}|{','.join(partners)}",
            e["tokens"],
            f"{e['chunks']} chunk{'s' if e['chunks'] > 1 else ''} duplicated with "
            f"{', '.join(partners[:3])}, ~{kfmt(e['tokens'])} tok",
        )

    coverage = tot["cited_turns"] / turns
    for n, g in trim.items():
        if is_pinned(g, pinned) or not g["present"]:
            continue
        size = g["tokens"] / g["present"]
        frac = g["present"] / turns
        if size >= COST_MIN_TOK and frac >= COST_MIN_PRESENT:
            add(
                "cost",
                n,
                str(int(size).bit_length()),
                g["tokens"],
                f"{kfmt(size)} tok on {int(100 * frac)}% of turns, ~{kfmt(g['tokens'])} tok",
            )
        if (
            coverage >= RARE_MIN_COVERAGE
            and size >= RARE_MIN_TOK
            and g["present"] >= RARE_MIN_PRESENT
            and g["cited"] == 0
        ):
            add(
                "rare",
                n,
                "0",
                g["tokens"],
                f"never cited in {g['present']} turns, ~{kfmt(g['tokens'])} tok",
            )
    return out


def by_owner(fs: dict[str, dict[str, Any]], owner: str) -> list[dict[str, Any]]:
    """Findings whose label, owner or owner basename matches ``owner``."""
    o = owner.strip().lower()
    out = []
    for f in fs.values():
        own = str(f["owner"]).lower()
        if o in (f["label"].lower(), own, os.path.basename(own.rstrip("/"))):
            out.append(f)
    return sorted(out, key=lambda f: -f["tokens"])


# ---------------------------------------------------------------------------
# Owner -> file mapping


def ext_name_of(path: str) -> str:
    """Frontmatter ``name`` of an extension script, or "" when not one."""
    if os.path.splitext(path)[1] not in (".py", ".sh", ".js", ".ts", ".mjs"):
        return ""
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            head = f.read(3000).splitlines()
    except OSError:
        return ""
    inside = False
    for ln in head[:40]:
        t = ln.strip().lstrip("#/").strip()
        if t == "---":
            if inside:
                return ""
            inside = True
        elif inside and t.startswith("name:"):
            return t[5:].strip().strip("'\"")
    return ""


def _same_file(a: str, b: str) -> bool:
    try:
        return os.path.realpath(a) == os.path.realpath(b)
    except (OSError, ValueError):
        return False


def owns(path: str, f: dict[str, Any], ext: str | None = None) -> bool:
    """Does the file at ``path`` own finding ``f``'s chunk?"""
    src, owner = f.get("source", ""), str(f.get("owner", ""))
    if src in ("AGENTS.md", "skill read"):
        return bool(owner) and os.path.isabs(owner) and _same_file(path, owner)
    if src in EXT_SOURCES:
        name = ext_name_of(path) if ext is None else ext
        return bool(name) and name == owner
    return False


def findings_for_path(path: str, fs: dict[str, dict[str, Any]]) -> list[dict[str, Any]]:
    ext = ext_name_of(path)
    return sorted((f for f in fs.values() if owns(path, f, ext)), key=lambda f: -f["tokens"])


def hint_line(path: str, owned: list[dict[str, Any]]) -> str:
    f = owned[0]
    owner = os.path.basename(str(f["owner"])) or f["label"]
    what = f["text"]
    if len(owned) > 1:
        tok = sum(x["tokens"] for x in owned)
        what = f"{len(owned)} findings ~{kfmt(tok)} tok, top: {what}"
    return f'[ctx] {os.path.basename(path)}: {what} — ctx_report(owner="{owner}") for detail'


# ---------------------------------------------------------------------------
# Config / state


def settings() -> tuple[bool, list[str]]:
    cfg = fir_ext.load_config() or {}
    nudge = bool(cfg.get("nudge", True))
    if os.environ.get("FIR_CTX_NUDGE", "").strip() == "0":
        nudge = False
    pinned = cfg.get("pinned")
    if not isinstance(pinned, list):
        pinned = DEFAULT_PINNED
    return nudge, [str(p) for p in pinned]


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


# ---------------------------------------------------------------------------
# Session wiring

_log: Log | None = None
_logs: dict[str, Log] = {}  # cross-session cache, refreshed incrementally
_findings: dict[str, dict[str, Any]] = {}
_findings_at = 0.0
_hinted_turn = False  # one ctx line per agent turn
_hinted: set[str] = set()  # finding ids already hinted this session


def session_log(ctx: fir_ext.Context) -> Log | None:
    global _log
    sf = ctx.get_session_file()
    sid = ctx.get_session_id()
    if not sf or not sid:
        return None
    path = os.path.join(os.path.dirname(sf), f"ctx-{sid}.jsonl")
    if _log is None or _log.path != path:
        _log = Log(path)
    _log.refresh()
    return _log


def all_logs(root: str) -> list[Log]:
    for p in glob.glob(os.path.join(root, "*", "ctx-*.jsonl")):
        if p not in _logs:
            _logs[p] = Log(p)
        _logs[p].refresh()
    return list(_logs.values())


def current_findings(ctx: fir_ext.Context, force: bool = False) -> dict[str, dict[str, Any]]:
    global _findings, _findings_at
    now = time.time()
    if force or not _findings_at or now - _findings_at >= FINDINGS_TTL:
        _, pinned = settings()
        root = sessions_root(ctx.get_session_file())
        _findings = findings(aggregate(all_logs(root)), pinned) if root else {}
        _findings_at = now
    return _findings


def detail(fs: list[dict[str, Any]]) -> str:
    return "\n".join(f"- [{f['id']}] {f['source']}:{f['owner']} — {f['text']}" for f in fs)


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


@fir_ext.tool(
    name="ctx_report",
    description=(
        "Prompt-cost findings (duplicated or costly chunks) and their owning "
        'files. Call when the user says "ctx review" or a [ctx] line points '
        "here. Propose trims as a diff to the owner; never apply unasked. "
        "action=dismiss drops findings (ids, or all); action=snooze mutes the "
        "nudge for days."
    ),
    parameters={
        "type": "object",
        "properties": {
            "owner": {
                "type": "string",
                "description": "e.g. lessons, AGENTS.md, a skill path; omit for all",
            },
            "action": {"type": "string", "enum": ["show", "dismiss", "snooze"]},
            "ids": {"type": "array", "items": {"type": "string"}},
            "days": {"type": "number"},
        },
    },
)
def ctx_report(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    fs = current_findings(ctx, force=True)
    action = str(params.get("action") or "show")
    state = load_state()
    if action == "snooze":
        days = float(params.get("days") or 7)
        state["snooze_until"] = time.time() + days * 86400
        save_state(state)
        return {"ok": True, "snoozed_days": days}
    if action == "dismiss":
        ids = params.get("ids") or list(fs)
        dis = state.setdefault("dismissed", {})
        for i in ids:
            if i in fs:
                dis[i] = fs[i]["sig"]
        save_state(state)
        return {"ok": True, "dismissed": [i for i in ids if i in fs]}
    owner = str(params.get("owner") or "").strip()
    sel = by_owner(fs, owner) if owner else sorted(fs.values(), key=lambda f: -f["tokens"])
    if not sel:
        return {"message": f"ctx: no findings{' for ' + owner if owner else ''}"}
    return {"message": detail(sel)}


@fir_ext.on("hook/tool_call")
def on_tool_call(params: Any, ctx: fir_ext.Context) -> None:
    """Contextual hint: one line when the agent touches a file that owns a finding."""
    global _hinted_turn
    with contextlib.suppress(Exception):  # never block a tool
        if _hinted_turn or str(params.get("tool_name", "")).lower() not in READ_TOOLS:
            return
        path = str((params.get("params") or {}).get("path") or "")
        if not path:
            return
        if not os.path.isabs(path):
            path = os.path.join(os.getcwd(), path)
        owned = [f for f in findings_for_path(path, _findings) if f["id"] not in _hinted]
        if not owned:
            return
        _hinted_turn = True
        _hinted.update(f["id"] for f in owned)
        ctx.send_message("ctx_hint", hint_line(path, owned), display=True, deliver_as="steer")


@fir_ext.on("session_start")
def on_session_start(params: Any, ctx: fir_ext.Context) -> None:
    with contextlib.suppress(Exception):
        session_log(ctx)
        current_findings(ctx)


@fir_ext.on("agent_start")
def on_agent_start(params: Any, ctx: fir_ext.Context) -> None:
    global _hinted_turn
    _hinted_turn = False


@fir_ext.on("turn_end")
def on_turn_end(params: Any, ctx: fir_ext.Context) -> None:
    log = session_log(ctx)
    if log is None:
        return
    slug, det = session_card(log)
    if slug:
        with contextlib.suppress(Exception):
            ctx.put_observable("ctx", slug, det)


@fir_ext.on("agent_end")
def on_agent_end(params: Any, ctx: fir_ext.Context) -> None:
    global _hinted_turn
    _hinted_turn = False
    with contextlib.suppress(Exception):
        maybe_nudge(ctx)


def maybe_nudge(ctx: fir_ext.Context) -> None:
    """One interrupt line when a finding is new or changed. Never prepends."""
    nudge, _ = settings()
    if not nudge:
        return
    fs = current_findings(ctx)
    state = load_state()
    if time.time() < float(state.get("snooze_until") or 0):
        return
    seen = state.setdefault("nudged", {})
    dis = state.get("dismissed", {})
    new = [f for f in fs.values() if seen.get(f["id"]) != f["sig"] and dis.get(f["id"]) != f["sig"]]
    if not new:
        return
    for f in new:
        seen[f["id"]] = f["sig"]
    save_state(state)
    new.sort(key=lambda f: -f["tokens"])
    top = new[0]
    n = len(new)
    body = (
        f"[interrupt:ctx] {n} new\n- ctx: {n} finding{'s' if n > 1 else ''} ready "
        f"({top['kind']} {top['label']} ~{kfmt(top['tokens'])} tok) — "
        'say "ctx review" to act, or ctx_report action=dismiss/snooze.'
    )
    ctx.send_message("ctx_nudge", body, display=True)


@fir_ext.command(
    name="ctx",
    description="Prompt-provenance report (per model), or /ctx findings | <owner> | <model>",
)
def cmd_ctx(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    _, pinned = settings()
    agg = aggregate(all_logs(sessions_root(ctx.get_session_file())))
    if not args:
        return {"message": render_report(agg, pinned)}
    arg = args[0]
    if arg in agg:
        return {"message": render_report({arg: agg[arg]}, pinned)}
    fs = findings(agg, pinned)
    sel = (
        sorted(fs.values(), key=lambda f: -f["tokens"]) if arg == "findings" else by_owner(fs, arg)
    )
    return {"message": detail(sel) if sel else f"ctx: no findings for {arg}"}


fir_ext.run(name="ctx")
