#!/usr/bin/env bash
# End-to-end verification: a persisted session whose transcript ends in an
# orphaned toolCall (process SIGKILLed mid tool call), loaded by the real fir
# binary over ACP session/load, must replay a synthesized error tool result.
set -euo pipefail

BIN="$(cd "$(dirname "$0")/.." && pwd)/bin/fir"
ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT

AGENT_DIR="$ROOT/agent"
CWD="$ROOT/project"
mkdir -p "$CWD"

# Session dir keyed by cwd, exactly as store.SessionDirForCwd computes it.
SLUG="--$(printf '%s' "${CWD#/}" | sed 's#[/\\:]#-#g')--"
SESS_DIR="$AGENT_DIR/sessions/$SLUG"
mkdir -p "$SESS_DIR"
SESS="$SESS_DIR/orphan.jsonl"

# --- Craft the artifact -----------------------------------------------------
# Header, a user message, then an assistant message carrying TWO parallel
# toolCall blocks and no toolResult — then a partial line, as a SIGKILL
# mid-write leaves behind. Note: no trailing newline on the partial line.
{
  printf '%s\n' '{"type":"session","version":3,"id":"orphan-demo","timestamp":"2026-08-02T07:39:00.000Z","cwd":"'"$CWD"'"}'
  printf '%s\n' '{"type":"message","id":"e1","parentId":"","timestamp":"2026-08-02T07:39:10.000Z","message":{"role":"user","content":"deploy the thing","timestamp":1754120350000}}'
  printf '%s\n' '{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-08-02T07:39:18.100Z","message":{"role":"assistant","model":"claude-test","provider":"anthropic","stopReason":"toolUse","timestamp":1754120358100,"content":[{"type":"text","text":"Deploying now."},{"type":"toolCall","id":"call-push","name":"bash","arguments":{"command":"git push"}},{"type":"toolCall","id":"call-post","name":"bash","arguments":{"command":"curl -XPOST https://deploy"}}]}}'
  printf '%s' '{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-08-02T07:39:34.0","message":{"role":"toolRes'
} > "$SESS"

echo "=== crafted transcript ($(wc -c < "$SESS") bytes, last line truncated) ==="
cat "$SESS"; echo; echo

# --- Drive the real binary over ACP ----------------------------------------
{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false}}}}'
  sleep 1
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"'"$SESS"'","cwd":"'"$CWD"'"}}'
  sleep 4
} | FIR_AGENT_DIR="$AGENT_DIR" "$BIN" --mode acp --no-extensions > "$ROOT/out.jsonl" 2> "$ROOT/err.log" || true

echo "=== session/update notifications replayed by session/load ==="
python3 - "$ROOT/out.jsonl" <<'PY'
import json, sys
found = {}
for line in open(sys.argv[1]):
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except json.JSONDecodeError:
        continue
    if msg.get("method") != "session/update":
        continue
    u = msg["params"]["update"]
    kind = u.get("sessionUpdate")
    if kind == "user_message_chunk":
        print("  user      :", u["content"]["text"])
    elif kind == "agent_message_chunk":
        print("  assistant :", u["content"]["text"])
    elif kind == "tool_call":
        print("  tool_call :", u.get("toolCallId"), u.get("title"))
    elif kind == "tool_call_update":
        texts = []
        for c in u.get("content") or []:
            t = c.get("content", {})
            if t.get("type") == "text":
                texts.append(t["text"])
        body = " ".join(texts)
        print("  result    :", u.get("toolCallId"), "status=%s" % u.get("status"))
        print("              ", body)
        found[u.get("toolCallId")] = (u.get("status"), body)

print()
ok = True
for cid in ("call-push", "call-post"):
    if cid not in found:
        print("FAIL: no result replayed for", cid); ok = False; continue
    status, body = found[cid]
    if status != "failed":
        print("FAIL:", cid, "status is", status, "want failed"); ok = False
    for needle in ("MAY OR MAY NOT", "UNKNOWN", "verify the current state"):
        if needle not in body:
            print("FAIL:", cid, "missing", needle); ok = False
print("VERDICT:", "PASS — synthesized interrupted-tool-call results replayed for both orphans" if ok else "FAIL")
sys.exit(0 if ok else 1)
PY

echo
echo "=== transcript after load (truncated line repaired, nothing else rewritten) ==="
cat -A "$SESS" | tail -2 | sed 's/\$$/<LF>/'
echo
echo "no synthesized toolResult was persisted:"
if grep -q "MAY OR MAY NOT" "$SESS"; then
  echo "  FAIL: synthesized text found on disk"; exit 1
else
  echo "  confirmed — the transcript still contains only the original entries"
fi
