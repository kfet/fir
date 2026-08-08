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
(which would silently skip it in headless/ACP mode).

Before writing, the source is validated and repaired so the file is actually
DISCOVERABLE: fir only treats a file as an extension when it is executable AND
carries comment frontmatter (a ``# ---`` block). A file missing frontmatter is
skipped forever, silently — so forge injects a shebang and a frontmatter block
when they are absent, and hard-errors on frontmatter that contradicts the
request (name mismatch, ``builtin: true``, malformed block).

After writing, forge calls ``ctx.reload_extension(name)`` and then VERIFIES via
``ctx.list_extensions()`` that the extension is actually running. If it is not,
that is reported as an error rather than as a cheerful "no new tools detected".
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


def _ok(text: str) -> dict:
    return {"content": [{"type": "text", "text": text}], "is_error": False}


_SHEBANG = "#!/usr/bin/env python3"


def _parse_frontmatter(code: str) -> tuple[dict[str, str] | None, bool]:
    """Mirror of Go's ``resources.ParseCommentFrontmatter``.

    Returns ``(fields, attempted)``:

    * ``fields`` — the parsed key/value map, or ``None`` when there is no
      VALID frontmatter block (exactly what makes fir skip the file).
    * ``attempted`` — True when the file opens a ``# ---`` block but the block
      is malformed (no closing delimiter, or a non-comment line inside).
      Distinguishing this lets forge report a broken block instead of
      silently prepending a second one.

    Kept deliberately in lockstep with pkg/resources/builtin_extensions.go:
    skip a leading shebang, require ``# ---`` as the next line, accept
    ``# key: value`` lines until the closing ``# ---``.
    """
    lines = code.split("\n")
    start = 1 if lines and lines[0].startswith("#!") else 0
    if start >= len(lines) or lines[start].strip() != "# ---":
        return None, False
    fields: dict[str, str] = {}
    for line in lines[start + 1 :]:
        stripped = line.strip()
        if stripped == "# ---":
            return fields, True
        if not stripped.startswith("# "):
            return None, True  # non-comment line inside the block: invalid
        key, sep, value = stripped[2:].partition(":")
        if not sep:
            continue
        fields[key.strip()] = value.strip()
    return None, True  # no closing delimiter: invalid


def _inject_frontmatter(code: str, name: str) -> str:
    """Prepend a shebang (if missing) and a minimal frontmatter block."""
    lines = code.split("\n")
    if lines and lines[0].startswith("#!"):
        shebang, body = lines[0], lines[1:]
    else:
        shebang, body = _SHEBANG, lines
    # No `modes:` key on purpose: an omitted modes list means "all modes",
    # so a forged extension loads in TUI, ACP and print sessions alike. That
    # keeps this tool's whole point (the thing you just wrote is live) true
    # everywhere, instead of making it silently absent under `-p`.
    header = [
        shebang,
        "# ---",
        f"# name: {name}",
        f"# description: {name} extension (forged in-session)",
        "# ---",
    ]
    return "\n".join(header + body)


def _prepare_source(code: str, name: str) -> tuple[str, str | None]:
    """Validate and repair ``code`` so the written file is discoverable.

    Returns ``(prepared_code, error)``. When ``error`` is non-None the caller
    must not write anything.
    """
    try:
        compile(code, f"{name}.py", "exec")
    except SyntaxError as e:
        return "", f"extension source has a syntax error at line {e.lineno}: {e.msg}"

    fields, attempted = _parse_frontmatter(code)

    if fields is None:
        if attempted:
            return "", (
                "extension source opens a '# ---' frontmatter block but the block is "
                "malformed (every line must start with '# ', and it must be closed by "
                "a '# ---' line). fir would skip this file silently — fix the block."
            )
        return _inject_frontmatter(code, name), None

    if fields.get("builtin") == "true":
        return "", (
            "frontmatter sets 'builtin: true', which makes discovery skip the file in "
            "user/project extension dirs. Remove it."
        )

    declared = fields.get("name")
    if declared is None:
        # Valid block, just missing the name key: repair it in place.
        lines = code.split("\n")
        idx = next(i for i, ln in enumerate(lines) if ln.strip() == "# ---")
        lines.insert(idx + 1, f"# name: {name}")
        code = "\n".join(lines)
    elif declared != name:
        return "", (
            f"frontmatter declares name: {declared!r} but the extension is being written "
            f"as {name!r}. fir keys extensions by file basename, so the mismatch would be "
            "confusing at best. Make them agree."
        )

    if not code.startswith("#!"):
        code = _SHEBANG + "\n" + code
    return code, None


def _mode_gated(fields: dict[str, str] | None, active_mode: str) -> bool:
    """True when declared frontmatter modes exclude the session's mode.

    Mirrors pkg/extension/mode_filter.go: an empty/absent list means all
    modes, "tui" normalises to "interactive", and "print" covers the
    text/json headless modes.
    """
    if not fields or not active_mode:
        return False
    raw = fields.get("modes") or fields.get("mode") or ""
    raw = raw.strip().strip("[]")
    declared = [m.strip().strip("\"'").lower() for m in raw.split(",")]
    declared = [m for m in declared if m]
    if not declared:
        return False
    active = active_mode.strip().lower()
    if active == "tui":
        active = "interactive"
    for m in declared:
        if m in ("all", "*"):
            return False
        if m == "tui":
            m = "interactive"
        if m == "print" and active in ("text", "json", "print"):
            return False
        if m == active:
            return False
    return True


def _is_loaded(ctx: fir_ext.Context, name: str) -> tuple[bool, bool]:
    """Return ``(loaded, known)`` for extension ``name``.

    ``known`` is False when the host cannot report loaded extensions (older
    fir, or a bridge without an extension manager). Callers must then avoid
    asserting either success or failure.
    """
    lister = getattr(ctx, "list_extensions", None)
    if lister is None:
        return False, False
    try:
        exts = lister()
    except Exception:
        return False, False
    if not isinstance(exts, list) or not exts:
        # An empty list means the host answered but knows of no extensions —
        # impossible while forge itself is running, so treat it as "unknown".
        return False, False
    return any(e.get("name") == name for e in exts if isinstance(e, dict)), True


def _active_mode(ctx: fir_ext.Context) -> str:
    """Best-effort current session mode ("interactive"/"acp"/"text"/...)."""
    try:
        info = ctx.agent_info()
    except Exception:
        return ""
    return str(info.get("mode") or "") if isinstance(info, dict) else ""


@fir_ext.tool(
    "forge_tool",
    "Author a new fir extension and load it live this session. Writes <name>.py "
    "to the global config dir's extensions/ (adding a shebang and comment "
    "frontmatter if missing, so the file is actually discoverable), reloads it, "
    "and verifies it is running — reporting the new tools it exposes, or an error.",
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

    prepared, prep_err = _prepare_source(code, name)
    if prep_err:
        return _err(f"refusing to write {path}: {prep_err}")
    fields, _ = _parse_frontmatter(prepared)

    # Snapshot tool names before, so we can report what the new extension added.
    before = {t.get("name") for t in ctx.list_tools()}

    try:
        with open(path, "w") as f:
            f.write(prepared)
        os.chmod(path, 0o755)  # noqa: S103 — extensions must be executable
    except OSError as e:
        return _err(f"could not write extension to {path}: {e}")

    try:
        ctx.reload_extension(name)
    except Exception as e:  # RuntimeError from a JSON-RPC error (init/handshake)
        return _err(f"reload_extension({name}) failed: {e}")

    after = {t.get("name") for t in ctx.list_tools()}
    new_tools = sorted(n for n in (after - before) if n)

    # Verify the file actually became a LIVE extension. A silent discovery
    # skip used to surface as "no new tools detected", which reads as success.
    loaded, known = _is_loaded(ctx, name)

    if known and not loaded:
        if _mode_gated(fields, _active_mode(ctx)):
            return _ok(
                f"wrote {path}, but it is not loaded in this session: its frontmatter "
                f"'modes:' excludes the current mode. The file is valid and will load "
                "in a session running a declared mode."
            )
        return _err(
            f"wrote {path}, but the extension is NOT loaded in this session. fir only "
            "runs a file that is executable AND has valid comment frontmatter AND is "
            "not mode-gated out; the extension process must also survive its init "
            "handshake. Check the file (and the fir log) before retrying."
        )

    if loaded:
        state = f"loaded extension '{name}' from {path}"
    else:
        # Host does not expose list_extensions: do not assert either way.
        state = f"wrote and reloaded extension '{name}' at {path}"

    if new_tools:
        return _ok(f"{state}. New tools: {', '.join(new_tools)}")
    return _ok(
        f"{state}. It registers no tools (it may register only commands, events, or providers)."
    )


fir_ext.run(name="forge")
