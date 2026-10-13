---
builtin: true
name: recall
description: Recall what earlier fir sessions learned — search bookmarks (then transcripts) of prior sessions on this repo, worktrees included. Use before exploring a repo you were spawned into, when the user says "we did this before", "what did we decide about X", or when you need context a past session likely had. Always reports exactly what was searched.
---

# recall — cross-session memory via ls + rg + read

No index, no service. Sessions leave sidecars, transcripts and bookmarks on
disk; you grep them. Work cheapest tier first and stop as soon as you have
enough.

## 1. Enumerate and rank candidates

```bash
R=$(mktemp -d)   # private scratch; concurrent agents must not share files
STATE="${XDG_STATE_HOME:-$HOME/.local/state}/fir/agents"
HERE=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)
for f in "$STATE"/*.json; do
  jq -r '[.started_at, .session_id, .cwd, .store_path, (.session_name // "")] | @tsv' "$f"
done 2>/dev/null | sort -r > $R/cands.tsv
```

Rank into tiers (record which tier each hit came from):

- **T1 repo** — sidecar `cwd` resolves to the same git *common dir* as
  `$HERE` (so sibling worktrees `<repo>-wt-*` of one repo match). Check with
  `git -C "$cwd" rev-parse --path-format=absolute --git-common-dir`; if the
  cwd no longer exists (removed worktree), match on path prefix
  `<repo>` / `<repo>-wt-`.
- **T2 recent** — any other session, newest first (`started_at`).
- **T3 all** — full catalog. Widen only when T1/T2 come up empty.

Within a tier, newest first. Skip your own session id.

```bash
# Not in a git repo? Skip T1 entirely (an empty $REPO would match every path).
[ -n "$HERE" ] || : > "$R/t1.tsv"
REPO=${HERE%/.git}
[ -n "$HERE" ] && \
while IFS=$'\t' read -r ts sid cwd store name; do
  case "$cwd" in
    "$REPO"|"$REPO"/*|"$REPO"-wt-*) ;;   # cheap path match first
    *) [ "$(git -C "$cwd" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" = "$HERE" ] || continue ;;
  esac
  printf '%s\t%s\t%s\t%s\t%s\n' "$ts" "$sid" "$cwd" "$store" "$name"
done < $R/cands.tsv > $R/t1.tsv
wc -l < $R/t1.tsv
```

## 2. Grep bookmarks first — `orient:` notes before anything else

Bookmarks live beside each transcript: `$(dirname store_path)/bookmarks-<sid>.jsonl`.
Each line is a verbatim turn with `_bookmark_note`.

```bash
# T1 dirs only, deduped
cut -f4 $R/t1.tsv | xargs -d '\n' -n1 dirname | sort -u > $R/dirs
# a) the repo map, always first
xargs -d '\n' -I{} find {} -maxdepth 1 -name 'bookmarks-*.jsonl' 2>/dev/null < $R/dirs \
  | xargs -d '\n' -r rg -l '"_bookmark_note"\s*:\s*"orient:'
# b) then your terms (case-insensitive, a few synonyms, alternation)
xargs -d '\n' -I{} find {} -maxdepth 1 -name 'bookmarks-*.jsonl' 2>/dev/null < $R/dirs \
  | xargs -d '\n' -r rg -i -c 'term1|term2|term3'
```

Read hits with `jq -r '._bookmark_note, (.message.content // . | tostring)'`
or `rg -i -C0` — pull the verbatim slice, not a paraphrase.

## 3. Fall through to the transcript when the slice is thin

A bookmark is an entry point; `store_path` is the whole record. If a hit
lacks the why/how, `rg -n -i 'term' <store_path> | head` and read around the
match. Refine terms, widen the tier, iterate — a few rounds, not dozens.

## 4. ALWAYS report the searched set

End every recall with a block like:

```
recall: searched T1 repo=7 sessions (3 with bookmarks), T2 recent=20;
terms: "cache|ttl"; hits: s1a2b (T1, orient: map), s9f0e (T1, 2 bookmarks, transcript read)
```

If nothing matched, say so plainly — "no prior session mentions X in the
N sessions searched" — and do not fill the gap with a plausible guess.
Absence reported honestly is the most important output of this skill.

When you state something learned, cite the session inline ("X — per s9f0e
bookmark"), so a later bookmark of your conclusion carries its warrant.

## 5. No repo map? Run `orient`

If step 2a found no `orient:` bookmark for this repo, run the `orient` skill
now (no need to ask) so the next session starts warm.
