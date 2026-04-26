#!/usr/bin/env python3
# ---
# name: install
# description: Install, uninstall, update, and list fir packages (skills, extensions, themes) from git repos or local paths.
# builtin: true
# commands: install: Install a package, uninstall: Uninstall a package, packages: List installed packages, update-packages: Update packages
# tools: install_package, list_packages, uninstall_package, update_packages
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
        out = _run(["install", *args])
        return {"message": out or "Package installed."}
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
    args = ["install", params["source"]]
    if params.get("local"):
        args.append("--local")
    try:
        return {"content": [{"type": "text", "text": _run(args)}]}
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
