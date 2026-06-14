#!/usr/bin/env python3
# ---
# name: forge
# description: Author a new fir extension and load it in-session via reload_extension.
# builtin: true
# ---
"""forge — write a new extension to the global config dir and load it live.

Exposes a single tool, ``forge_tool``, that lets the agent author a brand-new
fir extension and activate it mid-session. The extension source is written to
the GLOBAL (user) config dir's ``extensions/`` directory — never the
project-local ``.fir`` — so it is not subject to the project-local trust prompt
(which would silently skip it in headless/ACP mode). After writing, forge calls
``ctx.reload_extension(name)`` and reports which new tools/commands the
extension now exposes (or the init/handshake error, so the agent can fix it).
"""

from __future__ import annotations

import os
import re

import fir_ext

_SLUG_RE = re.compile(r"^[a-zA-Z0-9_-]+$")


def _global_config_dir() -> str | None:
    """Pick the global/user config dir, never the project-local ``.fir``.

    ``config_dirs`` is priority-ordered highest-first, with the project
    ``.fir`` highest. We want the global one, so we return the LAST entry
    that is not the project ``.fir``. Falls back to the only entry available.
    """
    dirs = [d for d in (fir_ext.config_dirs or []) if d]
    if not dirs:
        return None
    project_fir = os.path.abspath(os.path.join(fir_ext.cwd, ".fir")) if fir_ext.cwd else None
    for d in reversed(dirs):
        if project_fir is None or os.path.abspath(d) != project_fir:
            return d
    return dirs[-1]


def _err(text: str) -> dict:
    return {"content": [{"type": "text", "text": text}], "is_error": True}


@fir_ext.tool(
    "forge_tool",
    "Author a new fir extension and load it live this session. Writes <name>.py "
    "to the global config dir's extensions/ and reloads it, reporting the new "
    "tools it exposes (or the init error).",
    parameters={
        "type": "object",
        "properties": {
            "name": {
                "type": "string",
                "description": "Extension name = file basename. Safe slug: [a-zA-Z0-9_-]+.",
            },
            "code": {
                "type": "string",
                "description": "Full Python source of the extension to write.",
            },
        },
        "required": ["name", "code"],
    },
)
def forge_tool(params: dict, ctx: fir_ext.Context) -> dict:
    name = (params.get("name") or "").strip()
    code = params.get("code") or ""

    if not _SLUG_RE.match(name):
        return _err(f"invalid extension name {name!r}: must match [a-zA-Z0-9_-]+")
    if name == "forge":
        return _err("cannot forge an extension named 'forge' (would self-reload)")

    gdir = _global_config_dir()
    if not gdir:
        return _err("could not resolve a global config dir to write the extension into")

    ext_dir = os.path.join(gdir, "extensions")
    try:
        os.makedirs(ext_dir, exist_ok=True)
    except OSError as e:
        return _err(f"could not create extensions dir {ext_dir}: {e}")

    path = os.path.join(ext_dir, f"{name}.py")

    # Snapshot tool names before, so we can report what the new extension added.
    before = {t.get("name") for t in ctx.list_tools()}

    try:
        with open(path, "w") as f:
            f.write(code)
        os.chmod(path, 0o755)  # noqa: S103 — extensions must be executable
    except OSError as e:
        return _err(f"could not write extension to {path}: {e}")

    try:
        ctx.reload_extension(name)
    except Exception as e:  # RuntimeError from a JSON-RPC error (init/handshake)
        return _err(f"reload_extension({name}) failed: {e}")

    after = {t.get("name") for t in ctx.list_tools()}
    new_tools = sorted(n for n in (after - before) if n)

    if new_tools:
        summary = f"loaded extension '{name}' from {path}. New tools: {', '.join(new_tools)}"
    else:
        summary = (
            f"loaded extension '{name}' from {path}. No new tools detected "
            "(it may register only commands, events, or providers)."
        )
    return {"content": [{"type": "text", "text": summary}], "is_error": False}


fir_ext.run(name="forge")
