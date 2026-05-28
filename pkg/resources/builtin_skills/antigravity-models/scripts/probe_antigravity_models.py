#!/usr/bin/env python3
"""Probe Antigravity's Cloud Code Assist endpoint for valid model IDs.

For each candidate ID we send a tiny one-token request to
`/v1internal:streamGenerateContent?alt=sse` using the user's stored OAuth
creds, and infer the model's existence from the HTTP status:

  200 — model exists, request worked
  400 — model exists; request shape was wrong (e.g. wrong thinkingBudget)
  429 — model exists; user is out of quota for it
  500 — model exists; server error (still a positive existence signal)
  404 — model NOT in the catalog
  401/403 — auth/endpoint issue (don't classify the model)

Usage:
    probe_antigravity_models.py                 # uses built-in candidate list
    probe_antigravity_models.py model-a model-b # probes only the given IDs
    probe_antigravity_models.py --file ids.txt  # one ID per line
    probe_antigravity_models.py --json          # machine-readable output

Reads OAuth creds from ~/.config/fir/auth.json (key "google-antigravity").
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

AUTH_FILE = Path(os.environ.get("FIR_AUTH_FILE", str(Path.home() / ".config/fir/auth.json")))
PROVIDER = "google-antigravity"
ENDPOINT = os.environ.get(
    "FIR_ANTIGRAVITY_ENDPOINT", "https://cloudcode-pa.googleapis.com"
)
APP_VERSION = os.environ.get("FIR_ANTIGRAVITY_VERSION", "1.107.0")

# Status codes that mean "model exists on this endpoint".
EXISTS_CODES = {200, 400, 429, 500}


# Candidate IDs. Edit freely. The script runs sweeps in parallel and reports
# clean exists / missing buckets. Add new candidates after each Antigravity
# desktop-app bump or when a new Google/Anthropic/OpenAI release is announced.
DEFAULT_CANDIDATES = [
    # gemini-3.x base
    "gemini-3-flash", "gemini-3-flash-high", "gemini-3-flash-low",
    "gemini-3-pro", "gemini-3-pro-high", "gemini-3-pro-low",
    # gemini-3.1
    "gemini-3.1-flash", "gemini-3.1-flash-light", "gemini-3.1-flash-lite",
    "gemini-3.1-flash-low", "gemini-3.1-flash-high",
    "gemini-3.1-pro", "gemini-3.1-pro-high", "gemini-3.1-pro-low",
    "gemini-3.1-pro-medium",
    # gemini-3.5  (the one this skill was created to chase down)
    "gemini-3.5-flash", "gemini-3.5-flash-high", "gemini-3.5-flash-low",
    "gemini-3.5-flash-light", "gemini-3.5-flash-lite",
    "gemini-3.5-pro", "gemini-3.5-pro-high", "gemini-3.5-pro-low",
    # claude-4.x
    "claude-opus-4-5", "claude-opus-4-5-thinking",
    "claude-opus-4-6", "claude-opus-4-6-thinking",
    "claude-opus-4-7", "claude-opus-4-7-thinking",
    "claude-sonnet-4-5", "claude-sonnet-4-5-thinking",
    "claude-sonnet-4-6", "claude-sonnet-4-6-thinking",
    "claude-sonnet-4-7", "claude-sonnet-4-7-thinking",
    "claude-haiku-4-5", "claude-haiku-4-5-thinking",
    # gpt-oss
    "gpt-oss-20b-medium", "gpt-oss-120b-medium", "gpt-oss-120b-high",
    # known bad — sanity-check the classifier
    "definitely-not-a-real-model-xyz",
]


def load_creds() -> tuple[str, str]:
    if not AUTH_FILE.exists():
        sys.exit(f"no auth file at {AUTH_FILE} — run `fir login google-antigravity` first")
    data = json.loads(AUTH_FILE.read_text())
    creds = data.get(PROVIDER)
    if not creds:
        sys.exit(f"{PROVIDER!r} not in {AUTH_FILE}; available: {list(data)}")
    token = creds.get("access")
    project = creds.get("projectId") or (creds.get("extra") or {}).get("projectId")
    if not token or not project:
        sys.exit(f"{PROVIDER} creds missing access token or projectId")
    exp = int(creds.get("expires") or 0)
    if exp and exp < int(time.time() * 1000):
        sys.exit(f"{PROVIDER} access token expired; run `fir login {PROVIDER}` to refresh")
    return token, project


def probe(model_id: str, token: str, project: str, timeout: float = 20.0) -> tuple[int, str]:
    inner = {
        "contents": [{"role": "user", "parts": [{"text": "."}]}],
        "generationConfig": {"maxOutputTokens": 1, "thinkingConfig": {"thinkingBudget": 0}},
    }
    body = {
        "project": project,
        "model": model_id,
        "request": inner,
        "requestType": "agent",
        "userAgent": "antigravity",
        "requestId": f"probe-{int(time.time() * 1000)}-{model_id[:8]}",
    }
    arch = "arm64"  # cosmetic; server doesn't validate
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
        "User-Agent": f"antigravity/{APP_VERSION} darwin/{arch}",
        "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
    }
    url = f"{ENDPOINT}/v1internal:streamGenerateContent?alt=sse"
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:  # noqa: S310
            return r.status, r.read(200).decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, (e.read() or b"")[:200].decode("utf-8", "replace")
    except Exception as e:
        return 0, f"{type(e).__name__}: {e}"


def classify(code: int) -> str:
    if code in EXISTS_CODES:
        return "exists"
    if code == 404:
        return "missing"
    return "unknown"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("models", nargs="*", help="model IDs to probe (default: built-in list)")
    ap.add_argument("--file", help="file with one model ID per line")
    ap.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    ap.add_argument("--workers", type=int, default=8, help="parallel probes (default 8)")
    args = ap.parse_args()

    candidates: list[str] = list(args.models)
    if args.file:
        candidates += [
            ln.strip() for ln in Path(args.file).read_text().splitlines()
            if ln.strip() and not ln.lstrip().startswith("#")
        ]
    if not candidates:
        candidates = DEFAULT_CANDIDATES
    candidates = sorted(dict.fromkeys(candidates))  # dedup, stable

    token, project = load_creds()

    results: list[tuple[str, int, str, str]] = []
    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {ex.submit(probe, m, token, project): m for m in candidates}
        for fut in futs:
            m = futs[fut]
            code, body = fut.result()
            results.append((m, code, classify(code), body))

    results.sort(key=lambda r: (r[2] != "exists", r[2] != "missing", r[0]))

    if args.json:
        json.dump(
            [{"model": m, "status": c, "class": k, "body": b[:200]} for m, c, k, b in results],
            sys.stdout, indent=2,
        )
        sys.stdout.write("\n")
        return 0

    exists = [r for r in results if r[2] == "exists"]
    missing = [r for r in results if r[2] == "missing"]
    unknown = [r for r in results if r[2] == "unknown"]

    print(f"Endpoint: {ENDPOINT}    project: {project}")
    print(f"Probed {len(results)} candidates  →  "
          f"{len(exists)} exist, {len(missing)} missing, {len(unknown)} inconclusive")
    print()
    print("=== EXIST ===")
    for m, c, _, _ in exists:
        print(f"  {c}  {m}")
    print()
    print("=== MISSING (404) ===")
    for m, _, _, _ in missing:
        print(f"  {m}")
    if unknown:
        print()
        print("=== INCONCLUSIVE ===")
        for m, c, _, b in unknown:
            print(f"  {c}  {m}    {b[:80].replace(chr(10), ' ')}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
