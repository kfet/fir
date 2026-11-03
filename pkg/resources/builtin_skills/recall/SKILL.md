---
builtin: true
name: recall
description: Recall what earlier fir sessions built or decided — search bookmarks, transcripts and sibling repo docs. Use BEFORE you say you lack a tool or capability or do not know how; when the user says "we did/patched/added/built X", "don't you have/know X", "can't you X", "what did we decide about X"; and before exploring a repo you were spawned into. Always reports exactly what was searched.
---

# recall — cross-session memory via `fir sessions` + rg + read

No index, no service. Sessions leave transcripts and bookmarks on disk, in
fir's own store and in any read-only archive stores (other hosts' synced
sessions, configured as `sessionStores`). `fir sessions` reads all of them.
Work cheapest tier first and stop as soon as you have enough.

## 1. Enumerate and rank candidates

```bash
R=$(mktemp -d)   # private scratch; concurrent agents must not share files
fir sessions dirs > $R/stores.tsv      # store  path  readonly  exists — name these in your report
fir sessions list --all > $R/cands.tsv # store  modified  id  msgs  path  cwd  name  first_msg — newest first
HERE=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)
```

Rank into tiers (record which tier each hit came from):

- **T1 repo** — `cwd` is this repo or a sibling worktree `<repo>-wt-*`.
  Archive sessions come from other hosts, so match on the path, not with git.
- **T2 recent** — any other session, newest first.
- **T3 all** — the full catalog. Widen only when T1/T2 come up empty.

Skip your own session id.

```bash
# Not in a git repo? Skip T1 entirely (an empty $REPO would match every path).
REPO=${HERE%/.git}
[ -n "$HERE" ] && awk -F'\t' -v r="$REPO" '$6==r || index($6, r"/")==1 || index($6, r"-wt-")==1' $R/cands.tsv > $R/t1.tsv || : > $R/t1.tsv
wc -l < $R/t1.tsv
cut -f5 $R/t1.tsv | xargs -r -d '\n' -n1 dirname | sort -u > $R/t1dirs   # T1 project dirs, all stores
```

## 2. Search bookmarks first — `orient:` notes before anything else

`fir sessions search` greps every store, newest first. Each hit is
`store  kind  id  path:line  snippet`. Bookmark search is fast; a transcript
scan of a large archive takes tens of seconds, so narrow it (step 3).

```bash
# a) the repo map, always first (keep T1 hits)
fir sessions search --kind bookmark '"_bookmark_note"\s*:\s*"orient:' --limit 200 | grep -F -f $R/t1dirs | head
# b) then your terms (-i, a few synonyms, alternation)
fir sessions search --kind bookmark -i 'term1|term2|term3' --limit 200 > $R/hits.tsv
grep -F -f $R/t1dirs $R/hits.tsv | head -30   # T1 first, then the rest of hits.tsv
```

Read a hit at its line: `sed -n '<line>p' <path> | jq -r '._bookmark_note, (.message.content // . | tostring)'`
or `rg -n -i -C0 'term' <path>` — pull the verbatim slice, not a paraphrase.

## 3. Fall through to the transcript when the slice is thin

A bookmark is an entry point; the transcript (`path` in cands.tsv) is the
whole record. If a hit lacks the why/how, `rg -n -i 'term' <path> | head` and
read around the match. No bookmark hit? Search transcripts, narrowest first:
`fir sessions search --kind transcript -i 'terms' --cwd "$REPO"` (one project
dir, every store), then add `--since 720h` without `--cwd`, then everything. Refine terms, widen the tier, iterate — a few rounds,
not dozens. Archive transcripts are read-only: read them, never write there.

## 3b. Capability question? Grep the repo that would hold it

Sessions record work; repos record what shipped. Pick the likely repo beside this one
(e.g. "patched zulip" → `$SRC/zulip*`) and search its docs and log:

```bash
SRC=$(dirname "${REPO:-$PWD}")   # the dir holding your repos
for d in "$SRC"/*term*/; do git -C "$d" grep -iEl 'term1|term2' -- '*.md'; git -C "$d" log --oneline -i -E --grep 'term1|term2' | head -5; done
```

## 4. ALWAYS report the searched set

End every recall with a block like:

```
recall: stores local + zbox (read-only); searched T1 repo=7 sessions, T2 recent=20;
terms: "cache|ttl"; hits: s1a2b (T1 local, orient: map), s9f0e (T1 zbox, 2 bookmarks, transcript read)
```

If nothing matched, say so plainly — "no prior session mentions X in the
N sessions searched" — and do not fill the gap with a plausible guess.
Absence reported honestly is the most important output of this skill.

When you state something learned, cite the session inline ("X — per s9f0e
bookmark"), so a later bookmark of your conclusion carries its warrant.

## 5. No repo map? Run `orient`

If step 2a found no `orient:` bookmark for this repo, run the `orient` skill
now (no need to ask) so the next session starts warm.
