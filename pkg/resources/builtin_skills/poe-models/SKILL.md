---
name: poe-models
description: Look up Poe model details — pricing, context window, modalities, tool/vision/reasoning support — by querying Poe's /v1/models catalog API.
builtin: true
override: true
---

# Poe Models Skill

Query Poe's OpenAI-compatible model catalog at `https://api.poe.com/v1/models` to inspect pricing, context windows, supported features (tools, vision, reasoning effort), and endpoints for any bot.

The endpoint is public — no auth required — but the script will send a Bearer token if one is available (resolution order matches `poe-usage`: `POE_API_KEY` env → `~/.config/fir/models.json` `.providers.Poe.apiKey` → `~/.config/fir/auth.json` `.poe.access`/`.poe.key`).

## Usage

```bash
# Summary table of all bots
bash "$SKILL_DIR/scripts/models.sh"

# Filter by substring (case-insensitive) on id or display name
bash "$SKILL_DIR/scripts/models.sh" --filter claude
bash "$SKILL_DIR/scripts/models.sh" --filter gpt-5

# Full normalized JSON details for one model
bash "$SKILL_DIR/scripts/models.sh" --id claude-haiku-4.5

# Machine-readable JSON for all (filtered) models
bash "$SKILL_DIR/scripts/models.sh" --filter qwen --json | jq '.[].id'

# Raw upstream response (everything Poe returns)
bash "$SKILL_DIR/scripts/models.sh" --raw

# Cap rows in table mode
bash "$SKILL_DIR/scripts/models.sh" --limit 20
```

| Flag | Meaning |
|---|---|
| `--filter STR` | Substring match on `id` + `display_name` (case-insensitive) |
| `--id ID` | Show full JSON for a single bot by exact id |
| `--json` | Emit normalized JSON array for the filtered set |
| `--raw` | Dump the upstream response as-is |
| `--limit N` | Cap table rows |

## Output fields

Table columns: `ID`, `CTX` (context window, k-rounded), `PROMPT` and `COMPLETION` (USD per token — multiply by 1e6 for per-1M cost), `TOOLS`, `VISION`, `EFFORT` (advertises `reasoning_effort`).

Normalized JSON (`--json`) per entry:

- `id`, `display_name`, `owned_by`, `description`
- `input_modalities` — e.g. `["text", "image"]`
- `supported_features` — e.g. `["tools", "streaming"]`
- `supported_endpoints` — e.g. `["/v1/chat/completions", "/v1/messages"]`
- `context_length`, `max_output_tokens`
- `pricing` — Poe's raw object (`prompt`, `completion`, `input_cache_read`, `input_cache_write` — USD per token, as strings)
- `reasoning` — `{ required, supports_reasoning_effort }`
- `tools`, `vision` — convenience booleans

## Notes

- Poe's metadata is incomplete for many third-party bots: `context_length` of `0` means Poe didn't fill it in. Use `--raw` and grep the description as a last resort.
- For richer parsing (description-mined context windows, hardcoded overrides, endpoint routing rules), see `cmd/generate-models/main.go` (`fetchPoeModels`).
- For point balance and usage history rather than the model catalog, use the `poe-usage` skill.
