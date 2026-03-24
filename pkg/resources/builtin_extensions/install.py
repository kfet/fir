#!/usr/bin/env python3
# ---
# name: install
# description: Install, uninstall, update, and list fir packages (skills, extensions, themes) from git repos or local paths.
# builtin: true
# ---
"""
Builtin extension that exposes fir's package management as slash-commands
(for interactive use) and tools (for AI-driven use).
"""

from __future__ import annotations

import os
import subprocess
from typing import Any

import fir_ext


def _fir_bin() -> str:
    """Return path to the fir binary (override with FIR_BIN env var)."""
    return os.environ.get("FIR_BIN", "fir")


def _run(args: list[str]) -> str:
    """Run a fir subcommand and return combined output. Raises on non-zero exit."""
    result = subprocess.run(
        [_fir_bin(), *args],
        capture_output=True,
        text=True,
        timeout=60,
    )
    out = result.stdout.strip()
    err = result.stderr.strip()
    if result.returncode != 0:
        raise RuntimeError(err or out or f"fir {args[0]} failed (exit {result.returncode})")
    return out if out else err


# ── Post-install: JS/TS runtime wrapper setup ───────────────────────────


def _get_run_sh_path() -> str | None:
    """Find the extracted run.sh via NODE_PATH (set by fir SDK extraction)."""
    node_path = os.environ.get("NODE_PATH", "")
    for d in node_path.split(":"):
        candidate = os.path.join(d, "run.sh")
        if os.path.isfile(candidate):
            return candidate
    return None


def _has_shebang(path: str) -> bool:
    """Check if a file starts with #!"""
    try:
        with open(path, "rb") as f:
            return f.read(2) == b"#!"
    except OSError:
        return False


def _find_js_ts_entries(pkg_dir: str) -> list[str]:
    """Find JS/TS extension entry points (no shebang) in known locations."""
    # Only look for conventional entry point filenames
    entry_names = {"index.ts", "index.js", "main.ts", "main.js"}
    entries = []
    for root, dirs, files in os.walk(pkg_dir):
        for skip in (".git", "node_modules", "vendor", "__pycache__", "test", "tests", "dist", "build"):
            if skip in dirs:
                dirs.remove(skip)
        for f in files:
            if f in entry_names or (f.endswith((".ts", ".js")) and not f.endswith(".d.ts")):
                full = os.path.join(root, f)
                if not _has_shebang(full):
                    entries.append(full)
        # Only check one level deep per directory (don't recurse into src/ etc.)
        # unless it contains an extensions/ subdir
        dirs[:] = [d for d in dirs if d in ("extensions", "ext")]
    return entries


def _setup_wrapper(ext_dir: str, run_sh: str) -> bool:
    """Create an extensionless 'main' symlink → run.sh in ext_dir."""
    link_path = os.path.join(ext_dir, "main")
    if os.path.exists(link_path):
        return False
    try:
        os.symlink(run_sh, link_path)
        return True
    except OSError:
        return False


def _post_install_hook(pkg_dir: str) -> str | None:
    """Scan for JS/TS extensions and symlink run.sh wrappers."""
    run_sh = _get_run_sh_path()
    if not run_sh:
        return None

    js_ts_files = _find_js_ts_entries(pkg_dir)
    if not js_ts_files:
        return None

    dirs_with_entries: set[str] = set()
    for f in js_ts_files:
        dirs_with_entries.add(os.path.dirname(f))

    created = 0
    for d in sorted(dirs_with_entries):
        if _setup_wrapper(d, run_sh):
            created += 1

    if created > 0:
        return f"Created {created} runtime wrapper(s) for JS/TS extensions."
    return None


def _get_installed_path(source: str) -> str | None:
    """Resolve the install path for a package source from 'fir packages' output."""
    try:
        out = _run(["packages"])
    except RuntimeError:
        return None
    for line in out.splitlines():
        parts = line.split()
        # First column is SOURCE, last column is PATH
        if len(parts) >= 2 and parts[0] == source:
            path = parts[-1]
            if path.startswith("~/"):
                path = os.path.expanduser(path)
            if os.path.isdir(path):
                return path
    return None


def _run_install_and_hook(args: list[str]) -> str:
    """Run fir install, then post-install hook. Returns combined message."""
    out = _run(["install", *args])
    msg = out or "Package installed."

    source = next((a for a in args if not a.startswith("--")), None)
    if source:
        pkg_dir = _get_installed_path(source)
        if pkg_dir:
            hook_msg = _post_install_hook(pkg_dir)
            if hook_msg:
                msg += "\n" + hook_msg
    return msg


# ── Slash commands ──────────────────────────────────────────────────────────


@fir_ext.command(
    name="install",
    description="Install a package from a git repo or local path",
)
def cmd_install(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    """Usage: /install <source> [--local]"""
    if not args:
        return {"message": "Usage: /install <source> [--local]\n\nExamples:\n  /install github.com/user/repo\n  /install /path/to/local/pkg\n  /install github.com/user/repo@v1.2 --local"}
    try:
        return {"message": _run_install_and_hook(args)}
    except RuntimeError as e:
        return {"message": f"Error: {e}"}


@fir_ext.command(
    name="uninstall",
    description="Uninstall a package",
)
def cmd_uninstall(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    """Usage: /uninstall <source> [--local]"""
    if not args:
        return {"message": "Usage: /uninstall <source> [--local]"}
    try:
        out = _run(["uninstall", *args])
        return {"message": out or "Package uninstalled."}
    except RuntimeError as e:
        return {"message": f"Error: {e}"}


@fir_ext.command(
    name="packages",
    description="List installed packages",
)
def cmd_packages(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    """Usage: /packages"""
    try:
        out = _run(["packages", *args])
        return {"message": out or "No packages installed."}
    except RuntimeError as e:
        return {"message": f"Error: {e}"}


@fir_ext.command(
    name="update-packages",
    description="Update installed packages",
)
def cmd_update(args: list[str], ctx: fir_ext.Context) -> dict[str, Any]:
    """Usage: /update-packages [source]"""
    try:
        out = _run(["update", *args])
        return {"message": out or "All packages up to date."}
    except RuntimeError as e:
        return {"message": f"Error: {e}"}


# ── Tools (AI-facing) ───────────────────────────────────────────────────────


@fir_ext.tool(
    name="install_package",
    description="Install a fir package (skill, extension, or theme) from a git repo or local path.",
    parameters={
        "type": "object",
        "properties": {
            "source": {
                "type": "string",
                "description": "Git repo URL (e.g. github.com/user/repo or https://github.com/user/repo) or absolute local path. Append @ref to pin a branch/tag/commit.",
            },
            "local": {
                "type": "boolean",
                "description": "Install to project-local .fir/ instead of global ~/.config/fir/.",
            },
        },
        "required": ["source"],
    },
)
def tool_install(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    args = [params["source"]]
    if params.get("local"):
        args.append("--local")
    try:
        return {"content": [{"type": "text", "text": _run_install_and_hook(args)}]}
    except RuntimeError as e:
        return {"content": [{"type": "text", "text": str(e)}], "is_error": True}


@fir_ext.tool(
    name="uninstall_package",
    description="Uninstall a fir package.",
    parameters={
        "type": "object",
        "properties": {
            "source": {
                "type": "string",
                "description": "Package source to uninstall (same format used when installing).",
            },
            "local": {
                "type": "boolean",
                "description": "Uninstall from project-local .fir/ instead of global config.",
            },
        },
        "required": ["source"],
    },
)
def tool_uninstall(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    args = ["uninstall", params["source"]]
    if params.get("local"):
        args.append("--local")
    try:
        return {"content": [{"type": "text", "text": _run(args)}]}
    except RuntimeError as e:
        return {"content": [{"type": "text", "text": str(e)}], "is_error": True}


@fir_ext.tool(
    name="list_packages",
    description="List all installed fir packages (global and project-local).",
    parameters={
        "type": "object",
        "properties": {},
    },
)
def tool_list(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    try:
        return {"content": [{"type": "text", "text": _run(["packages"])}]}
    except RuntimeError as e:
        return {"content": [{"type": "text", "text": str(e)}], "is_error": True}


@fir_ext.tool(
    name="update_packages",
    description="Update one or all installed fir packages.",
    parameters={
        "type": "object",
        "properties": {
            "source": {
                "type": "string",
                "description": "Specific package source to update. Omit to update all packages.",
            },
        },
    },
)
def tool_update(params: dict[str, Any], ctx: fir_ext.Context) -> dict[str, Any]:
    args = ["update"]
    if params.get("source"):
        args.append(params["source"])
    try:
        return {"content": [{"type": "text", "text": _run(args)}]}
    except RuntimeError as e:
        return {"content": [{"type": "text", "text": str(e)}], "is_error": True}


fir_ext.run(name="install")
