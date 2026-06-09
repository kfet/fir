# Transcript Footprint — where session bytes go, and what's actually fixable

Status: analysis + proposals. No code yet.

## Method

Fleet scan of `~/.config/fir/sessions/**/*.jsonl` — **3404 sessions, 404M**
(one 18.3M PTY-loop pathology excluded; tracked separately, it's a bug not a
footprint pattern). Bytes attributed by record type, message role, content
block, and per-tool. Re-read / re-run / re-write redundancy measured by
correlating `toolCall` → `toolResult` via id, tracking per-path/per-command
state within each session.

All figures below are on-disk transcript bytes. The transcript is the event
log the context window is reconstructed from, so redundancy here is also
redundancy in-context (the higher-value axis).

## Where the bytes are

| category | bytes | % corpus |
|---|---|---|
| toolResult (output) | 125.9M | 31.1% |
| toolCall args (input) | 49.2M | 12.2% |
| assistant thinking | 32.1M | 7.9% |
| assistant text | 14.1M | 3.5% |
| user text | 6.5M | 1.6% |
| user image | 4.5M | 1.1% |
| everything else | <10M | <2.5% |

### toolResult bytes by tool

| tool | bytes | n | avg |
|---|---|---|---|
| bash | 77.1M | 53970 | 1497B |
| read | 38.8M | 8277 | 4919B |
| observe_session | 5.7M | 249 | 23966B |
| edit | 1.1M | 10589 | 105B |
| pipe | 0.8M | 95 | 9043B |

### toolCall (args) bytes by tool

| tool | bytes | n | avg |
|---|---|---|---|
| edit | 12.3M | 10600 | 1216B |
| bash | 11.9M | 54082 | 230B |
| write | 11.7M | 2960 | 4158B |
| plan | 3.0M | 4251 | 749B |

## Key finding: "bash" is not monolithic — a third of it is file-reading

Stripping `cd … &&` prefixes to recover the real command verb:

| verb | bytes | calls | nature |
|---|---|---|---|
| **sed** | 17.2M | 5665 | almost all `sed -n 'X,Yp' file` range views |
| git | 10.6M | 8615 | genuine |
| grep + rg | 13.3M | ~11k | genuine search |
| **cat** | 7.0M | 2381 | whole-file dumps |
| go | 4.2M | 5879 | genuine (build/test) |
| nl/head/tail/awk | ~3M | — | more file views |

**File-viewing via bash (cat/sed/head/tail/nl/awk) = 26.9M (35% of all bash
output), over 9362 calls.** This content bypasses the `read` tool entirely, so
it is invisible to any read-side tracking or caching and is pulled fresh every
time.

### Distribution — why blanket truncation is the wrong tool

Top 1% of bash results = 20% of bash bytes; top 10% = 61%. The fat tail is
*genuine* — test runs, `git diff main..HEAD`, doc fetches. Truncating by size
would degrade exactly the useful work. Every proposal below targets
**redundancy, not volume**, so it costs zero quality.

## The headline lever: file-content redundancy, split across two tools

Pure re-fetch = same path fetched again with **no intervening edit/write**.

| source | pure re-fetch bytes | sessions |
|---|---|---|
| `read` tool re-reads | 7.94M | 333 |
| bash sed/cat re-views | 8.89M | 351 |
| **combined** | **~16.8M** | **~700 session-instances** |

~4% of the entire corpus, the single biggest fixable pattern, and the most
systemic (~20% of all sessions when both paths are counted). The content is
already upthread verbatim, so removing the repeat is lossless.

(A further 3.19M of read re-reads happen *after* an edit/write to that path —
arguably justified, excluded from the "pure" figure above.)

## Proposals

Ordered by impact × safety × breadth. Each notes the design decision reached
with the maintainer.

### P1 — Steer file I/O toward the dedicated tools (tool-description change)

**Cheapest, highest leverage. Pure prose, no engine change.** The 26.9M of
file-viewing-via-bash exists because `bash` is the path of least resistance.
Update tool descriptions:

- **bash**: "For viewing file contents, prefer the `read` tool over
  `cat`/`sed -n`/`head`/`tail` — `read` output is tracked and de-duplicated.
  For modifying files, prefer `edit`/`write` over `sed -i` / heredoc rewrites."
- **read** / **write** / **edit**: reciprocal nudge — "this is the preferred
  way to view/modify files; do not shell out to cat/sed for this."

This is the only available handle on the bash file-view bytes: bash output is
otherwise un-classifiable at the engine layer. Routing it through `read` makes
P2 possible.

### P2 — Optional model-supplied hash on `read` (opt-in, never automatic)

Decision: **the tool never decides to withhold content, and the prompt must
not reason about "did *I* change it"** — outside processes change files too, so
"unless you changed it" is wrong and is removed entirely. Re-reads are
intentional and legitimate; we do not assume the model can't or shouldn't
re-read. We only give the model a cheap way to *confirm* a file is unchanged
when that's all it needs.

Mechanism — model-driven opt-in:

- Every `read` result carries a `hash` (content hash) in its header.
- `read` gains an optional `if_hash` input param. When the model already holds
  a file's content and only needs to confirm it hasn't moved, it calls
  `read(path, if_hash=<that hash>)`.
- The tool computes the current hash fresh on every call and compares:
  - **match** → returns a tiny stub `{ unchanged: true, hash }` instead of the
    full body. The model recalls the content it already has.
  - **mismatch** (model edit *or* any outside change) → returns the full
    current content + new hash, exactly as a normal read.
- No `if_hash` supplied → always a full read. Default behaviour is unchanged.

Because the hash is recomputed every call, outside changes invalidate it
automatically — there is no staleness window and no "who changed it" question.
The optimisation is entirely the model's to invoke, the same way the plan tool
trusts the model to drive updates.

Tool-description guidance (no "unless you changed it"): "Each read returns a
`hash`. If you already hold a file's contents and only need to confirm it is
still current, pass `if_hash` to get back `unchanged` cheaply instead of the
full body. Re-read freely whenever you actually need the content."

Caught: up to ~16.8M of pure re-fetch (once file-views are routed through
`read` via P1), at the model's discretion.

### P3 — Compact the plan args via short keys (same JSON, compressed)

Decision: **plan is intentional and stays** — re-emitting full state every
update is what keeps agents on track; that is not waste. But the *serialised
representation* is token-heavy JSON, repeated across 4251 updates = 3.0M. The
bloat is **repeated keys + verbose enum values** — same structure on every
entry, every update.

Decision on form: **keep it JSON, just compress the names.** Short keys, short
enum codes, documented by the schema. It stays valid JSON the model emits
natively (no bespoke parser, no escaping story, robust stdlib decode), and the
schema descriptions carry the meaning.

**Worked example.** Current (full keys + full enums) — what lands today:

```json
[{"content":"Initialize and run review pass 1","priority":"high","status":"in_progress"},
 {"content":"Execute quality checks (go vet + targeted -race tests)","priority":"high","status":"pending"},
 {"content":"Record findings","priority":"low","status":"completed"}]
```

Proposed (short keys `c`/`p`/`s`, enum codes `h|m|l`, `p|i|x`):

```json
[{"c":"Initialize and run review pass 1","p":"h","s":"i"},
 {"c":"Execute quality checks (go vet + targeted -race tests)","p":"h","s":"p"},
 {"c":"Record findings","p":"l","s":"x"}]
```

~25–30% fewer arg tokens, no behavioural change, and it is still ordinary JSON.

**Mechanism — compressed wire form, canonical handler form.** The compact JSON
*is* the model-facing schema (the tool advertises `c`/`p`/`s` with descriptions
mapping them to content/priority/status). On every call the handler:
1. decodes the compact args,
2. **expands** them to the canonical full-name form via a schema-derived map,
3. validates against the canonical schema — **fail the call if invalid**,
4. runs the existing handler logic on the canonical form.

So internal code and stored plan state stay full-named and readable; only the
wire/transcript representation is compressed. Expansion + validation is a thin,
mechanical layer (see "Generalisation" below).

**Versioning is trivial here** — it's still a JSON array of objects, so no
format-version header is needed. Make the reader **tolerant**: accept both
`content` and `c`, and both full (`high`) and code (`h`) enum values. Old
transcripts with full keys still decode and render unchanged; new sessions emit
short. A field-alias table (full ↔ short) is the single source of truth.

Breadth: 450 sessions, 4251 plan calls — the most widely-triggered pattern in
the fleet.

### Generalisation — auto-schema compression as a reusable mechanism

The plan fix is a special case of a general idea: present each tool a
**compressed wire schema** (short field names + short enum codes), expand inbound
args back to the canonical schema, validate, then hand off. One codec, driven by
a per-field alias declaration, reused across tools.

**Worth doing — with eyes open about where it pays.** The saving is on *keys and
enum values*, not on *values*. So the benefit gradient is sharp:

- **High payoff:** tools whose args are **repeated structured records** — arrays
  of small objects where the same keys recur N times. `plan` is the standout;
  any future todo/batch/table-shaped tool qualifies.
- **~Zero payoff:** tools whose args are dominated by **free-text values** —
  `bash` (the command string), `write` (file body), `edit` (`oldText`/`newText`).
  Shortening `"command"`→`"c"` saves one key against a multi-KB value. Not worth
  the indirection.

**Design constraints, learned the hard way:**
- **Don't auto-derive abbreviations from field names.** Minimal-unique-prefix
  schemes collide (two fields starting the same) and are unstable under schema
  evolution (adding a field silently shifts another's abbreviation, breaking old
  transcripts). Instead **declare the short alias explicitly** in each schema
  field (e.g. an `x-short` keyword). Stable, intentional, reviewable. "Auto" =
  the codec is generic; the aliases are declared.
- **Canonical form is the source of truth.** Handlers, stored state, and
  validation all use full names; compression is purely an edge transform.
- **Fail closed.** Any arg that doesn't expand-and-validate is a hard error, same
  as a malformed call today.
- **Schema-definition cost is paid once** (in the system prompt); descriptions
  still need to convey meaning. The win is strictly on *per-call* arg tokens
  replayed every turn — which is exactly the cost that compounds.

Recommendation: build the codec generically but **enable it per-field, opt-in**,
starting with `plan`. Roll it to other tools only where args are
record-shaped. Don't blanket-apply — most tools won't benefit and would just
carry indirection.

### P4 — Optional model-supplied hash on `bash` (same opt-in as P2)

Decision: **never skip execution** — re-running exists to get fresh data and we
can't assume idempotency. And, as with reads, **do not assume the model can't
make good use of repeated runs** — that's its call. Same opt-in shape as P2:

- Every `bash` result carries a `hash` of its output.
- `bash` gains an optional `if_hash` param. The command **always executes**;
  after it runs, the tool hashes the output and compares to `if_hash`:
  - **match** → store/return a stub `{ unchanged: true, hash }` (the model
    already holds this output verbatim).
  - **mismatch** → return the fresh output + new hash.
- The model opts in when it expects output may be unchanged and only needs to
  confirm (e.g. re-checking `git status` after a no-op). Fresh data is always
  guaranteed because the command runs regardless.

This is a transcript/context optimisation only — zero change to what executes.
Realised saving tracks the byte-identical subset of the 3.49M re-run bytes.

### P5 — Smaller, clean wins

- **observe_session** — 5.7M, avg 24KB/call. Add **range/partial** retrieval
  (like `read`): let the caller request a turn span (e.g. turns N–M) instead of
  pulling the whole transcript. Keep the default behaviour as-is — this just
  gives the model a way to fetch only the slice it needs.

## What we explicitly will NOT do

- **Compress/truncate bash by size** — the large tail is genuine work
  (test output, diffs). Size is not the signal; redundancy is.
- **Touch thinking blocks** (32M) — quality-bearing, not redundant.
- **Auto-stub re-reads/re-runs without the model opting in** — outside changes
  and intentional repeats are both legitimate; the `if_hash` opt-in (P2/P4)
  keeps the decision with the model.

## Suggested sequencing

1. **P1** (tool descriptions) — ships today, no engine change, unlocks the rest.
2. **P3** (plan format) — self-contained, broad footprint, no behaviour change.
3. **P2** (read `if_hash`) — the big lever; needs P1 first to capture bash views.
4. **P4 / P5** — opportunistic.

P1 + P2 + P3 together address ~20M of provably-redundant bytes (~5% of corpus)
with no quality degradation, and the same bytes in-context turn over turn.
