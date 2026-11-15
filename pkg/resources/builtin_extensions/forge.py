#!/usr/bin/env python3
# ---
# name: forge
# description: Author a new fir extension, prove it in a sandbox sub-agent, then load it.
# builtin: true
# ---
"""forge — author an extension, prove it safe in a sandbox, then load it.

Exposes a single tool, ``forge_tool``, that lets the agent author a brand-new
fir extension and activate it mid-session. A bad extension in the global dir
breaks EVERY live session on the host (e.g. a tool named ``mcp_*`` makes the
Anthropic subscription API reject every request), so forge never installs
unproven code:

1. **Static checks** — the source must compile, carry discoverable comment
   frontmatter (forge injects a shebang and frontmatter when absent and
   hard-errors on contradicting frontmatter), and every literal tool name
   must pass ``TOOL_NAME_RULES`` (a data table of names providers reject).
2. **Sandbox** — the candidate is written to a temp project's
   ``.fir/extensions/`` next to a tiny probe extension, and ``fir -p`` runs
   there with the caller's provider and model. The sub-agent must make a
   real model call with the new tools registered, and the probe must see
   every tool the candidate declares. The actual tool names are checked
   against ``TOOL_NAME_RULES`` again (catches dynamically built names).
   Any failure is returned with the sub-agent's error so the caller can fix
   the code and retry; nothing is installed.
3. **Install** — ``scope: "global"`` (default) writes the file to the global
   (user) config dir's ``extensions/`` — never the project-local ``.fir``, so
   the trust prompt cannot silently skip it in headless/ACP mode.
   ``scope: "session"`` loads it into the calling session only
   (``ctx.reload_extension(name, path)``); no other session ever sees it.

After loading, forge VERIFIES via ``ctx.list_extensions()`` that the
extension is actually running and reports its new tools, or an error.
"""

from __future__ import annotations

import ast
import atexit
import contextlib
import json
import os
import re
import shutil
import subprocess
import tempfile

import fir_ext

_SLUG_RE = re.compile(r"^[a-zA-Z0-9_-]+$")

# Tool names that a provider rejects or mishandles. Data, not code: add a row
# when a new provider quirk is found. Each rule is (regex, reason). A forged
# extension is global by default, so it reaches every provider: the rules
# apply whatever provider the caller uses.
TOOL_NAME_RULES: list[tuple[str, str]] = [
    (
        r"^mcp_(?!_)",
        "the Anthropic Claude subscription (OAuth) API fails EVERY request that "
        "carries a tool whose name starts with 'mcp_' (HTTP 400 \"You're out of "
        'extra usage"), which breaks every session that loads the extension',
    ),
    (
        r"^mcp__",
        "the 'mcp__<server>__<tool>' namespace belongs to MCP server tools; an "
        "extension tool there would collide with or impersonate them",
    ),
    (
        r"^(?![a-zA-Z0-9_-]{1,64}$)",
        "providers (Anthropic, OpenAI, Google) only accept tool names of 1-64 "
        "characters from [a-zA-Z0-9_-]",
    ),
]

# Sandbox sub-agent budget. Generous: it is one short model call, but a cold
# start loads every global extension first.
SANDBOX_TIMEOUT_S = 180

_PROBE_NAME = "forgeprobe"
_PROBE_SOURCE = """#!/usr/bin/env python3
# ---
# name: forgeprobe
# description: forge sandbox probe (records tools seen during a real model call)
# ---
import json
import os

import fir_ext

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "probe.json")


_seen = {}


def _write(rec):
    with open(OUT, "w") as f:
        json.dump(rec, f)


# Snapshot the registered tools just before the model request. RPCs are not
# safe at message_end: in print mode the host is already exiting by then.
@fir_ext.on("turn_start")
def _on_turn_start(params, ctx):
    _seen["tools"] = sorted(t.get("name", "") for t in ctx.list_tools())
    _seen["extensions"] = ctx.list_extensions()


@fir_ext.on("message_end")
def _on_message_end(params, ctx):
    if not params or params.get("role") != "assistant":
        return
    reason = params.get("stop_reason", "")
    if reason in ("error", "aborted"):
        _write({"model_ok": False, "error": "assistant message ended with stop_reason=" + reason})
        return
    rec = {
        "model_ok": True,
        "provider": params.get("provider", ""),
        "model": params.get("model", ""),
    }
    rec.update(_seen)
    _write(rec)


@fir_ext.on("provider_error")
def _on_provider_error(params, ctx):
    _write({"model_ok": False, "error": (params or {}).get("error_text") or json.dumps(params)})


fir_ext.run(name="forgeprobe")
"""

_SANDBOX_PROMPT = "This is an automated smoke test. Reply with just OK. Do not call any tools."


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


def _literal_tool_names(code: str) -> list[str]:
    """Tool names passed as string literals to ``fir_ext.tool(...)`` / ``tool(...)``.

    A static pre-check only; dynamically built names are caught after the
    sandbox run, from the tools the extension actually registered.
    """
    try:
        tree = ast.parse(code)
    except SyntaxError:
        return []
    names = []
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        fn = node.func
        if not (
            (isinstance(fn, ast.Attribute) and fn.attr == "tool")
            or (isinstance(fn, ast.Name) and fn.id == "tool")
        ):
            continue
        arg = node.args[0] if node.args else None
        for kw in node.keywords:
            if kw.arg == "name":
                arg = kw.value
        if isinstance(arg, ast.Constant) and isinstance(arg.value, str):
            names.append(arg.value)
    return names


def check_tool_names(names) -> str | None:
    """Return an error naming every tool that breaks a ``TOOL_NAME_RULES`` row."""
    problems = []
    for n in names:
        for pattern, reason in TOOL_NAME_RULES:
            if re.search(pattern, n):
                problems.append(f"tool name {n!r} is rejected: {reason}.")
                break
    if not problems:
        return None
    return " ".join(problems) + " Rename the tool(s) and retry."


def _caller_model(ctx: fir_ext.Context) -> tuple[str, str]:
    """The calling session's (provider, model id), or ("", "") if unknown."""
    try:
        info = ctx.agent_info()
    except Exception:
        return "", ""
    model = info.get("model") if isinstance(info, dict) else None
    if not isinstance(model, dict):
        return "", ""
    return str(model.get("provider") or ""), str(model.get("id") or "")


def _fir_bin() -> str | None:
    return os.environ.get("FIR_BIN") or shutil.which("fir")


def _tail(text: str, n: int = 2000) -> str:
    text = (text or "").strip()
    return text if len(text) <= n else "..." + text[-n:]


_MODES_LINE_RE = re.compile(r"^#\s*modes?\s*:.*$")


def _sandbox_copy(source: str) -> str:
    """``source`` with any frontmatter ``modes:`` line removed.

    The sandbox runs ``fir -p`` (print mode); an extension gated to other
    modes must still load there so its tools can be proven. Only the
    frontmatter block is touched.
    """
    lines = source.split("\n")
    start = 1 if lines and lines[0].startswith("#!") else 0
    if start >= len(lines) or lines[start].strip() != "# ---":
        return source
    out = lines[: start + 1]
    i = start + 1
    while i < len(lines) and lines[i].strip() != "# ---":
        if not _MODES_LINE_RE.match(lines[i].strip()):
            out.append(lines[i])
        i += 1
    return "\n".join(out + lines[i:])


def run_sandbox(
    name: str, source: str, provider: str, model: str
) -> tuple[list[str] | None, str | None]:
    """Prove ``source`` in a throwaway ``fir -p`` sub-agent.

    Returns ``(tools, None)`` with the tools the candidate registered, or
    ``(None, error)`` with the sub-agent's error. The sub-agent runs in a
    fresh temp project whose ``.fir/extensions/`` holds the candidate and the
    probe; it uses the caller's provider and model, so a provider that
    rejects the new tools fails here instead of in every live session.
    """
    fir = _fir_bin()
    if not fir:
        return None, "cannot find the fir binary (FIR_BIN unset and 'fir' not on PATH)"
    if not provider or not model:
        return None, "cannot determine the calling session's provider and model for the sandbox run"
    root = tempfile.mkdtemp(prefix="fir-forge-sandbox-")
    try:
        ext_dir = os.path.join(root, ".fir", "extensions")
        os.makedirs(ext_dir)
        for fname, text in (
            (f"{name}.py", _sandbox_copy(source)),
            (f"{_PROBE_NAME}.py", _PROBE_SOURCE),
        ):
            path = os.path.join(ext_dir, fname)
            with open(path, "w") as f:
                f.write(text)
            os.chmod(path, 0o755)  # noqa: S103 — extensions must be executable
        cmd = [
            fir,
            "-p",
            "--no-session",
            "--no-mcp",
            "--provider",
            provider,
            "--model",
            model,
            "--thinking",
            "off",
            _SANDBOX_PROMPT,
        ]
        try:
            proc = subprocess.run(
                cmd,
                cwd=root,
                capture_output=True,
                text=True,
                timeout=SANDBOX_TIMEOUT_S,
                stdin=subprocess.DEVNULL,
            )
        except subprocess.TimeoutExpired:
            return None, f"sandbox sub-agent timed out after {SANDBOX_TIMEOUT_S}s"
        except OSError as e:
            return None, f"could not start sandbox sub-agent {fir}: {e}"

        probe = None
        try:
            with open(os.path.join(root, ".fir", "probe.json")) as f:
                probe = json.load(f)
        except (OSError, ValueError):
            pass
        output = _tail((proc.stderr or "") + "\n" + (proc.stdout or ""))
        if proc.returncode != 0:
            detail = (probe or {}).get("error") or output
            return (
                None,
                f"sandbox sub-agent ({provider}/{model}) failed with exit code {proc.returncode}: {detail}",
            )
        if not probe:
            return (
                None,
                f"sandbox sub-agent made no successful model call (probe saw nothing). Output: {output}",
            )
        if not probe.get("model_ok"):
            return None, f"sandbox model call failed: {probe.get('error') or output}"

        ext = next(
            (
                e
                for e in probe.get("extensions") or []
                if isinstance(e, dict) and e.get("name") == name
            ),
            None,
        )
        if ext is None:
            return None, (
                f"the extension did not load in the sandbox (it must survive its init "
                f"handshake). Output: {output}"
            )
        declared = [t for t in ext.get("tools") or [] if t]
        visible = set(probe.get("tools") or [])
        missing = [t for t in declared if t not in visible]
        if missing:
            return (
                None,
                f"tools declared but not visible to the model in the sandbox: {', '.join(missing)}",
            )
        return declared, None
    finally:
        shutil.rmtree(root, ignore_errors=True)


# Names this session loaded with scope "session": a session extension shadows
# the global one, so a later global forge of the same name must re-point it.
_session_loaded: set[str] = set()
# Temp dirs holding session-only extension files; removed when forge exits
# (the session ends), since nothing else ever loads them.
_session_dirs: list[str] = []


@atexit.register
def _cleanup_session_dirs() -> None:
    for d in _session_dirs:
        shutil.rmtree(d, ignore_errors=True)


@fir_ext.tool(
    "forge_tool",
    "Author a new fir extension and load it live. First proves it in a sandbox: "
    "a short-lived `fir -p` sub-agent with your provider and model loads it, "
    "makes a real model call with its tools, and checks each tool is visible; "
    "tool names providers reject (e.g. the 'mcp_' prefix) are refused. Only then "
    "installs it: scope 'global' (default) writes <name>.py to the global "
    "extensions dir (every new session loads it); scope 'session' loads it into "
    "this session only. On failure returns the sandbox error so you can fix the "
    "code and retry.",
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
            "scope": {
                "type": "string",
                "enum": ["global", "session"],
                "description": "global (default): install for all sessions. session: load into this session only.",
            },
        },
        "required": ["name", "code"],
    },
    timeout=SANDBOX_TIMEOUT_S + 60,
)
def forge_tool(params: dict, ctx: fir_ext.Context) -> dict:
    name = (params.get("name") or "").strip()
    code = params.get("code") or ""
    scope = (params.get("scope") or "global").strip()

    if not _SLUG_RE.match(name):
        return _err(f"invalid extension name {name!r}: must match [a-zA-Z0-9_-]+")
    if name in ("forge", _PROBE_NAME):
        return _err(f"cannot forge an extension named {name!r} (reserved by forge)")
    if scope not in ("global", "session"):
        return _err(f"invalid scope {scope!r}: must be 'global' or 'session'")

    prepared, prep_err = _prepare_source(code, name)
    if prep_err:
        return _err(f"refusing to install {name}: {prep_err}")
    name_err = check_tool_names(_literal_tool_names(prepared))
    if name_err:
        return _err(f"refusing to install {name}: {name_err}")
    fields, _ = _parse_frontmatter(prepared)

    provider, model = _caller_model(ctx)
    ctx.report_progress(f"forge: proving {name} in a sandbox sub-agent ({provider}/{model})")
    sandbox_tools, sb_err = run_sandbox(name, prepared, provider, model)
    if sb_err:
        return _err(
            f"sandbox check failed, nothing installed: {sb_err} (the sandbox also loads "
            "your other global extensions; if the error names one of them, the fault is "
            "there, not in this code)"
        )
    name_err = check_tool_names(sandbox_tools or [])
    if name_err:
        return _err(f"sandbox check failed, nothing installed: {name_err}")

    if scope == "session":
        target_dir = tempfile.mkdtemp(prefix="fir-forge-session-")
    else:
        gdir = _global_config_dir()
        if not gdir:
            return _err("could not resolve a global config dir to write the extension into")
        target_dir = os.path.join(gdir, "extensions")
    try:
        os.makedirs(target_dir, exist_ok=True)
    except OSError as e:
        return _err(f"could not create extensions dir {target_dir}: {e}")
    path = os.path.join(target_dir, f"{name}.py")
    previous = None
    if scope == "global" and os.path.exists(path):
        try:
            with open(path) as f:
                previous = f.read()
        except OSError:
            previous = None

    def fail(text: str) -> dict:
        # Never leave a file that failed to load where every new session
        # would pick it up: restore the previous version or remove it.
        if scope == "session":
            shutil.rmtree(target_dir, ignore_errors=True)
            return _err(text)
        try:
            if previous is None:
                os.remove(path)
                what = "removed it"
            else:
                with open(path, "w") as f:
                    f.write(previous)
                what = "restored the previous version"
            # Best effort: the file on disk is already safe; this only
            # resyncs the live session with it.
            with contextlib.suppress(Exception):
                ctx.reload_extension(name)
        except OSError as e:
            what = f"could not roll it back ({e}); remove {path} by hand"
        return _err(f"{text} forge {what}.")

    # Snapshot tool names before, so we can report what the new extension added.
    before = {t.get("name") for t in ctx.list_tools()}

    try:
        with open(path, "w") as f:
            f.write(prepared)
        os.chmod(path, 0o755)  # noqa: S103 — extensions must be executable
    except OSError as e:
        return fail(f"could not write extension to {path}: {e}")

    try:
        if scope == "session" or name in _session_loaded:
            # A session-only load shadows discovery, so a global forge of a
            # name this session loaded earlier must re-point it too.
            ctx.reload_extension(name, path)
        else:
            ctx.reload_extension(name)
    except Exception as e:  # RuntimeError from a JSON-RPC error (init/handshake)
        return fail(f"reload_extension({name}) failed: {e}")

    after = {t.get("name") for t in ctx.list_tools()}
    new_tools = sorted(n for n in (after - before) if n)

    # Verify the file actually became a LIVE extension. A silent discovery
    # skip used to surface as "no new tools detected", which reads as success.
    loaded, known = _is_loaded(ctx, name)
    where = "this session only" if scope == "session" else "all new sessions"

    if known and not loaded:
        if _mode_gated(fields, _active_mode(ctx)):
            return _ok(
                f"sandbox passed and wrote {path}, but it is not loaded in this session: its "
                "frontmatter 'modes:' excludes the current mode. The file is valid and will "
                "load in a session running a declared mode."
            )
        return fail(
            f"sandbox passed and wrote {path}, but the extension is NOT loaded in this "
            "session. fir only runs a file that is executable AND has valid comment "
            "frontmatter AND is not mode-gated out; the extension process must also "
            "survive its init handshake. Check the fir log before retrying."
        )

    if scope == "session":
        _session_loaded.add(name)
        _session_dirs.append(target_dir)

    if loaded:
        state = f"sandbox passed; loaded extension '{name}' from {path} ({where})"
    else:
        # Host does not expose list_extensions: do not assert either way.
        state = f"sandbox passed; wrote and reloaded extension '{name}' at {path} ({where})"

    if new_tools:
        return _ok(f"{state}. New tools: {', '.join(new_tools)}")
    return _ok(
        f"{state}. It registers no tools (it may register only commands, events, or providers)."
    )


fir_ext.run(name="forge")
