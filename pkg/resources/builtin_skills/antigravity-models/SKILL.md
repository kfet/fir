---
name: antigravity-models
description: Discover what models are actually available on Google Antigravity. Scrapes the desktop app for client fingerprint, then probes Cloud Code Assist's streamGenerateContent endpoint to confirm valid model IDs. Use when the user asks which models Antigravity exposes, why a specific model is missing, or wants to refresh fir's antigravity_auth.py catalogue.
---

# Antigravity model discovery

Google Antigravity has no public "list models" endpoint, and its desktop app fetches its menu over a server-pushed protobuf gRPC stream — there is no static list in either the JS bundle or any HTTPS API we can read. The only reliable way to know what's available is to **probe the streaming endpoint** with candidate IDs and read the status codes.

## Division of labour with the in-extension probe

Fir's `antigravity_auth` builtin extension **already self-prunes** the model catalogue at runtime via its `auth_list_models` hook — it probes every ID in its own static catalogue once per hour (fir caches the result), and hides any ID that comes back 404. That means **stale entries you don't need to think about**: a model removed from Antigravity stops appearing in the picker the next time `fir` runs, without any code change.

This skill is for the half the in-extension probe can't cover: **discovering NEW IDs** that aren't in the catalogue yet. The extension can only filter the static list it knows about; it can't speculate about IDs Google may have added (`gemini-3.5-flash-low`, future `claude-haiku-4-7-thinking`, etc.).

Use this skill when:

- A new Gemini/Claude/GPT-OSS release is announced and you want to know if Antigravity is serving it yet.
- The Antigravity desktop app bumps versions (run the scraper to see the new `version` and bump the UA in `antigravity_auth.py` if needed).
- A user reports a model they expect on Antigravity isn't appearing.

## Scripts

1. **`scripts/scrape_antigravity_app.py`** — pulls the client fingerprint from the locally installed Antigravity desktop app (version string, User-Agent we must impersonate, X-Goog-Api-Client tag, any literal model IDs that happen to leak through). Confirms the menu is server-driven and surfaces the impersonation values.

2. **`scripts/probe_antigravity_models.py`** — for each candidate model ID, sends a 1-token request to `/v1internal:streamGenerateContent?alt=sse` using the user's stored OAuth creds. Buckets the IDs as `exists` (200/400/429/500) vs `missing` (404).

## Steps

1. Run the scraper:
   ```bash
   python3 $SKILL_DIR/scripts/scrape_antigravity_app.py
   ```
   Note the `version` field. If it differs from the UA pin in `pkg/resources/builtin_extensions/antigravity_auth.py` (`antigravity/X.Y.Z`), update both that file and the same constant in `scripts/probe_antigravity_models.py`.

2. Run the prober with **speculative additions** (anything not already in `antigravity_auth.py`'s catalogue):
   ```bash
   python3 .fir/skills/antigravity-models/scripts/probe_antigravity_models.py gemini-3.5-flash gemini-3.5-flash-low claude-haiku-4-7-thinking
   ```
   Or with the built-in candidate sweep (~40 IDs, ~30s, parallel):
   ```bash
   python3 .fir/skills/antigravity-models/scripts/probe_antigravity_models.py
   ```

3. For each new `EXIST` entry that isn't yet in `antigravity_auth.py`:
   - Add an `_antigravity_model(...)` entry with pricing copied from models.dev's `google/<id>` row.
   - The next time `fir` runs and refreshes the live cache, the in-extension probe will confirm it stays live (or hide it again if it was a flake).
   - Add a one-line `CHANGELOG.md` entry under `## [Unreleased] / ### Added`.

4. **Removals are not needed** — leave 404'd entries in the catalogue. The in-extension probe hides them automatically and they can come back if Google reinstates the model.

## Non-obvious

- The Antigravity desktop app's JS bundle does **not** list its model menu. Don't waste time trying to grep it out — the only literal model IDs in there are image-gen ones (`gemini-3-flash-image`, etc.). The cascade server pushes the live menu over a gRPC call that requires running the Antigravity language-server daemon to replay.

- Status-code classifier: `200/400/429/500` all mean **the model exists**. Only `404` means missing. Some IDs (`gemini-3.1-pro-low`) reject `thinkingBudget=0` with a 400 explaining "this model only works in thinking mode" — that's still a positive existence signal. Don't tighten the classifier to "200 only" or you'll miss thinking-only models.

- The skill expects creds at `~/.config/fir/auth.json` under the `google-antigravity` key. If the token is expired the prober will exit cleanly and ask the user to `fir login google-antigravity`.

- Endpoints: `cloudcode-pa.googleapis.com` and `daily-cloudcode-pa.sandbox.googleapis.com` work for personal Google accounts; `autopush-cloudcode-pa.sandbox.googleapis.com` returns 403 for everyone. Override with `FIR_ANTIGRAVITY_ENDPOINT=...` if you need to test against a different one.

- Antigravity's naming is its own — `gemini-3.1-flash-lite` (with `-lite`), not `gemini-3.1-flash-light` (with `-light`), even though both look plausible. Always probe a candidate before adding it to the catalogue; never trust naming conventions from sibling providers.

- Each probe burns a tiny bit of quota (1 generated token). The default list is ~40 candidates and parallelises to 8 workers; on a free-tier account this is well under the daily limit.
