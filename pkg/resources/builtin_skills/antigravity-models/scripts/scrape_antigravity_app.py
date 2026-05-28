#!/usr/bin/env python3
"""Scrape what we can from the local Antigravity desktop app bundle.

The Antigravity client doesn't ship a static model menu — the menu is fetched
at runtime from the cascade server via the gRPC `GetCascadeModelConfigs`
method on a protobuf-encoded stream we can't easily replay. What the bundle
*does* give us, and what this script extracts, is the small but useful
"client fingerprint": app version, User-Agent we must impersonate, the
Cloud Code Assist API client tag, and any literal model IDs that *do*
appear in the JS (currently only the image-gen ones leak through).

Output is a single JSON object on stdout. Step-2 of the discovery flow.

Usage:
    scrape_antigravity_app.py [--app /Applications/Antigravity.app]
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


DEFAULT_APP_DARWIN = "/Applications/Antigravity.app"
DEFAULT_APP_LINUX = "/usr/share/antigravity"


def find_app(explicit: str | None) -> Path:
    if explicit:
        p = Path(explicit)
        if not p.exists():
            sys.exit(f"app not found: {p}")
        return p
    for c in (DEFAULT_APP_DARWIN, DEFAULT_APP_LINUX):
        if Path(c).exists():
            return Path(c)
    sys.exit("Antigravity app not found; pass --app PATH")


def find_resources(app: Path) -> Path:
    """Return the directory holding package.json and the JS bundles."""
    for cand in (
        app / "Contents/Resources/app",   # macOS
        app / "resources/app",            # Linux
        app,                              # unpacked
    ):
        if (cand / "package.json").exists():
            return cand
    sys.exit(f"could not find resources/app under {app}")


def read_package_json(res: Path) -> dict:
    try:
        return json.loads((res / "package.json").read_text())
    except Exception as e:
        return {"_error": str(e)}


def grep_bytes(path: Path, pattern: bytes) -> list[bytes]:
    try:
        data = path.read_bytes()
    except OSError:
        return []
    return re.findall(pattern, data)


# Conservative model-id pattern. Antigravity-style suffixed forms only.
# Requires a digit somewhere in the suffix to exclude things like
# "claude.md", "claude-desktop", "llamaindex" that are not model IDs.
_MODEL_RE = re.compile(
    rb'"((?:gemini|claude|gpt-oss|grok|deepseek|qwen|kimi|llama)-?\d[a-z0-9.\-]{1,60})"'
)
# What User-Agent and X-Goog-Api-Client tags appear?
_UA_RE = re.compile(rb'"(antigravity/[0-9][0-9a-z.\-]*[^"]*)"')
_GOOG_CLIENT_RE = re.compile(rb'"(google-cloud-sdk[^"]+)"')


def scan_js(res: Path) -> dict:
    candidates = [
        res / "out/jetskiAgent/main.js",
        res / "out/vs/workbench/workbench.desktop.main.js",
        res / "out/main.js",
    ]
    bundles = [p for p in candidates if p.exists()]

    model_hits: set[str] = set()
    ua_hits: set[str] = set()
    goog_hits: set[str] = set()
    for b in bundles:
        for m in grep_bytes(b, _MODEL_RE):
            try:
                model_hits.add(m.decode())
            except UnicodeDecodeError:
                pass
        for m in grep_bytes(b, _UA_RE):
            try:
                ua_hits.add(m.decode())
            except UnicodeDecodeError:
                pass
        for m in grep_bytes(b, _GOOG_CLIENT_RE):
            try:
                goog_hits.add(m.decode())
            except UnicodeDecodeError:
                pass

    return {
        "bundles_scanned": [str(p.relative_to(res)) for p in bundles],
        "literal_model_ids": sorted(model_hits),
        "user_agents": sorted(ua_hits),
        "x_goog_api_client_hits": sorted(goog_hits),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--app", help="Path to Antigravity.app (or unpacked resources dir)")
    args = ap.parse_args()

    app = find_app(args.app)
    res = find_resources(app)
    pkg = read_package_json(res)
    scan = scan_js(res)

    out = {
        "app_path": str(app),
        "resources_path": str(res),
        "version": pkg.get("version"),
        "distro": pkg.get("distro"),
        "main": pkg.get("main"),
        "scan": scan,
        "notes": [
            "Antigravity's model menu is server-driven (GetCascadeModelConfigs gRPC) "
            "and is NOT a static list in the JS bundle.",
            "Literal model IDs found in the JS are typically image-gen-only.",
            "The User-Agent + X-Goog-Api-Client values are the useful bits — "
            "they tell us what to impersonate when probing.",
            "Feed this output into probe_antigravity_models.py to test live model IDs.",
        ],
    }
    json.dump(out, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
