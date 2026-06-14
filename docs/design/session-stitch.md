# Session Stitch — emulating infinite context across fir sessions

**Status**: design — accepted; v1 not yet built
**Branch**: `work/session-stitch`

> **This is a self-handoff.** It is written for the agent (probably me)
> who will build this. Per the thesis it argues for, a handoff carries
> *warrants, not summaries* — so this doc keeps the reasoning that
> produced each decision, not just the decision. If you are about to
> implement, read "Why these constraints" before "Proposal"; the
> constraints are load-bearing and were argued for, not assumed.

## Problem

A fir session has a finite window. A working life spans hundreds of
sessions. The goal is to **emulate infinite context**: an agent in
session N should recall, reason over, and build on what sessions 1..N-1
established, as if it had all been in one window.

The hard part is **not** fetching a fact. It is **stitching disjoint
sessions** — fusing reasoning that lives in different transcripts into
one coherent working context. State that precisely, because it is what
disqualifies the obvious tool (RAG) and qualifies the chosen one.

## Why these constraints (the reasoning trail)

These are the conclusions of a long dialectic. Each is a position, with
the argument attached so a future agent can *re-derive* (and overturn)
it rather than cargo-cult it.

**No RAG / no embeddings — and this is goal-specific, not dogma.**
RAG wins at single-shot semantic lookup over prose ("needle in a
haystack"). Our task is the opposite: multi-hop synthesis across
disjoint sources, which is exactly RAG's weakness — it returns
nearest-neighbour chunks stripped of reasoning context and *cannot
report what it didn't look at*. The usual objection to dropping
embeddings is the lexical-miss problem (grep "latency", miss "slow
response"). **That objection collapses here** because the index is
*self-authored*: the same model wrote the bookmark and writes the query,
so the anchors are findable by construction. Embeddings would patch a
blind spot this design doesn't have. (If we ever serve keyword-unknown
prose Q&A, revisit — but that is not this.)

**The model is the index and the synthesiser.** Bookmarks are the index
(verbatim, self-authored, grep-findable); attention is the stitcher
(slices loaded into a finite window). We do not index knowledge better
than the model — nothing can, so we don't try. This mirrors the
industry's revealed preference: agentic grep replaced vector RAG in every
major coding agent, and the stated reason is that it *improves for free
as models improve* while RAG needs continuous engineering.

**A bookmark is an actively-identified slice, not a summary.** A summary
is post-hoc lossy compression by paraphrase. A bookmark is an attention
act *during* the work that keeps the **verbatim** span. Lossless at the
slice; lossy only in *selection*. That single property is what makes the
whole thing work: verbatim ⇒ greppable; selection-only-loss ⇒ the raw
transcript is always the fallback.

**Checkability over determinism.** Determinism is the wrong target:
discoveries compound, and so do errors, through the same pipe. The
defence is not "never wrong" but "wrong *detectably*": re-derive, detect
contradiction, invalidate downstream when a premise changes.

**No single root.** The moment a recall stitches two lineages, the store
is a *graph*, not a tree — it has no apex. "Roots" are connected
components; entry is locality → scope → catalog; components *merge* as
discoveries bridge them. So the only singular thing is a dumb enumerable
catalog — which fir already has as the sidecar dir, not a daemon.

## Two layers: in-context learning first, agentic search in addition

The single idea that organises everything below. The design **leans on
the transformer's in-context learning (ICL) first**, and uses **agentic
search only as the fuel line** that feeds it.

The counterfactual makes it concrete: *in a true infinite-context model,
ICL alone solves this.* Load every prior session into the window; the
transformer attends over all of it and stitches natively in one forward
pass — no tools, no index, no search. We don't have that window, so we
**externalise the parts of ICL the finite window can't hold, and stream
them back in on demand.** Every component is a stand-in for a native
in-context faculty:

| native ICL faculty | externalised as | layer |
|---|---|---|
| saliency — "what matters" (attention) | `bookmark()` → verbatim slice | **ICL** |
| relevance judged at query time | the model generates the grep terms | **ICL** |
| synthesis / fusing slices | in-window attention once loaded | **ICL** |
| reasoning trace / warrant | prose written inline, re-read later | **ICL** |
| multi-hop *reach* across context | iterative grep → read → refine loop | search |

**Why ICL is first.** The intelligence is assumed to already live in the
model's in-context abilities; the design adds *no semantic machinery*,
because any such machinery moves judgement out of live ICL into a frozen
artifact. Bookmarks are ICL's own saliency precipitated to disk.
Stitching is ICL fusing slices with the same attention it uses on any
in-context material — we feed that engine, we don't rebuild it. Rejecting
embeddings is the *consequence* of ICL-first (relevance is judged live,
not baked into a vector), not a separate preference. Verbatim-not-summary
preserves the raw tokens ICL needs to re-judge later. Derived-as-regular-
bookmark works because ICL re-reads the prose and re-derives the
inference.

**Why agentic search is in addition.** grep/ls/read carry zero semantics
— they fetch what ICL's judgement points at and never judge. The loop is
iterative precisely to emulate what attention does for free in one pass:
probe, read, notice it's not enough, reach further, and *stop when the
model decides it has what it needs.* In the infinite-context limit this
layer vanishes; the ICL layer does not.

**The ordering is the bet.** Put intelligence in ICL, keep the
scaffolding dumb, and the system improves for free as models improve —
while any semantic index you build needs perpetual re-engineering. The
seam between the two layers is exactly the failure surface: every failure
of this system is ICL over-trusting a partial gather, which is why the
load-bearing metric is *coverage honesty*, not accuracy (see "How we'll
know it works").

## What already exists (verify each before building)

Most of the substrate ships today. This proposal is a thin layer, not a
new system.

1. **Verbatim salient slices — the index.** `bookmark(quote, note)`
   (`handoff.py`; design: [handoff-bookmarks.md](handoff-bookmarks.md))
   pins a *verbatim* transcript entry into `bookmarks-<sid>.jsonl` beside
   the transcript, with `_bookmark_note` injected. Its stated core
   principle is already our thesis: *"The transformer is uniquely
   equipped to know what matters in its own context."*

2. **Enumerable session catalog.** `observe.py` ([observe.md](observe.md))
   writes a sidecar per session at `$XDG_STATE_HOME/fir/agents/<id>.json`
   (`session_id`, `cwd`, `mode`, `store_path`, `started_at`, `status`,
   `session_name`). Deliberately a filesystem catalog, **not** a registry
   — `ls`-enumerable, crash-isolated, post-mortem-durable. This is the
   "no dumb single root" catalog, already built.

3. **Transcripts — the ground-truth leaves.** Each sidecar's `store_path`
   is the full session JSONL. A bookmark is an entry point; the
   transcript is the whole record. The path back is always there.

4. **Single-lineage inheritance.** `self_handoff(content)`
   ([self-handoff-design.md](self-handoff-design.md)) restarts with a
   prepended briefing and hands the child the parent's bookmarks *by
   path*. The child reads/greps with plain tools. Parent→child only; no
   query API.

## The gap

[handoff-bookmarks.md](handoff-bookmarks.md) names it in its own
out-of-scope list:

> *"Cross-session bookmark queries. Bookmarks belong to the session that
> wrote them. The child inherits them by path, not by a query API."*

Inheritance is a **single lineage**. Session N cannot recall from
non-ancestors — a sibling worktree, last week's debugging session, a
different project that already solved this. Index, catalog, and leaves
all exist; nothing reads *across* them. **That sentence is the whole
project.**

## Proposal

**Session Stitch** — a cross-session recall layer over the three existing
primitives, adding nothing heavier than `ls`, `ripgrep`, `read`.

### Read side — an agentic grep loop (not a query engine)

1. **Enumerate** sidecars → candidates. Rank cheapest-first:
   locality (same `cwd`, free from sidecar) → scope (`session_name` /
   `started_at` window / `status`) → all (full catalog scan, the
   widening fallback).
2. **Grep** the query across `bookmarks-*.jsonl` of candidates (parallel
   `rg`). On a hit, optionally descend into that session's `store_path`.
3. **Pull** matched *verbatim* slices into context — attention stitches.
   This is the step RAG cannot do; it is why we keep slices lossless.
4. **Iterate** — refine keywords, widen the tier, or fall through to the
   full transcript when a slice is insufficient.

### Write side — there isn't one

A recall run that synthesises across sessions needs **no new write path
and no new schema**. The agent writes its conclusion as a normal turn —
*with its grounding inline* ("X, because s3 said A and s7 said B") — and
`bookmark()`s it like any other slice. Because the bookmark is verbatim,
the warrant is captured for free: the slice names what it stood on, in
the model's own words.

This drops the earlier `_derived` / `_sources` typing. Per the axiom,
observation is just derivation with an empty premise set — there is no
seen-vs-concluded *class* to encode. A synthesis is a claim whose warrant
happens to be other bookmarks; a regular verbatim bookmark already
carries that warrant as prose. A stitch becomes a higher-level index
entry future runs can grep, indistinguishable from any other bookmark —
which is the point.

Consequences, kept honest:

- **Invalidation is grep, like everything else.** If a leaf is corrected,
  find the derivations that stood on it by grepping its terms — the
  self-warranting prose mentions them. No structured provenance needed.
- **The discipline moves from schema to skill.** Nothing *forces* the
  model to write its grounding. The recall skill must instruct it to
  state conclusions with their sources inline, so the slice is
  self-warranting. Structure in the loop, not in the store.
- **No new risk.** Indistinguishable derived/observed bookmarks can both
  be re-stitched, so a wrong synthesis can propagate — but that is the
  symmetric compounding we already accepted; the defence (detect
  contradiction, re-derive) is unchanged.

## Smallest thing that works (v1 slice — build this first)

Resist building the whole design. The first commit that delivers value:

1. A **skill** `recall` that instructs the agent to:
   `ls $XDG_STATE_HOME/fir/agents/*.json` → pick candidates by `cwd`
   then recency → `rg <terms> $(dirname store_path)/bookmarks-*.jsonl`
   → read hits → fall through to `store_path` on a thin slice →
   **report the searched-set** (which sessions, which tier) with the
   answer.
2. Nothing else. No extension, no derived-write, no ranking helper.

If that skill, hand-driven, reliably stitches across two real prior
sessions, the thesis holds and we earn the next pieces. If it doesn't,
no amount of structure will save it.

## How we'll know it works (falsifiable — do not skip)

The architecture lives or dies on one metric, and it is *not* answer
accuracy. Seed known facts across several sessions, then query. Measure:

- **Stitch accuracy** — does it correctly fuse facts from ≥2 sessions?
- **Coverage honesty (the load-bearing one)** — when the fact is *absent*
  from what it searched, does it (a) say so and widen / fall through, or
  (b) confabulate a plausible answer? A high-accuracy system with poor
  coverage-honesty is a confidence trick that *feels* like memory.

If coverage-honesty isn't calibrated, stop and fix that before anything
else — a generative recall loop poisons itself at exactly the rate it
learns.

## Open questions — with my position on each

1. **Tool vs skill vs thin extension.** *Position: skill for v1.* AGENTS.md
   keeps logic out of core unless skills/extensions can't express it;
   inheritance already runs on plain `read`/`grep`. Add a thin extension
   *only* if sidecar ranking (parsing `cwd`/`started_at`) proves to need
   structure beyond `ls | rg`. Don't pre-build it.

2. **Where derived bookmarks live.** *Resolved — dissolved.* Derived
   insights are regular bookmarks (see "Write side — there isn't one"), so
   they live where bookmarks already live: the current session's
   `bookmarks-<sid>.jsonl`. No separate store, no tag, no distinction.

3. **Coverage manifest.** *Position: a skill convention for v1* (the loop
   prints `searched N sessions [tier]; M hits`), promoted to an
   extension-enforced contract only if agents skip it. This is the
   mechanism that makes "didn't find" ≠ "didn't look"; it is not optional.

4. **Candidate ranking.** *Position: locality (`cwd` exact) then recency
   (`started_at`) is enough for v1.* Both are free from the sidecar.

5. **Stop criterion / budget.** *Position: stop when a tier yields zero
   new hits, with a hard session-count cap as a backstop;* parallel `rg`
   for latency. Tune the cap from the eval, not a priori.

6. **Staleness.** *Position: non-issue.* Grep reads live files (fresh by
   construction); the only race is a half-written trailing JSONL line,
   which line-oriented parsing skips. `status=running` flags live writers.

## Out of scope (v1)

- **Hierarchy / recursion.** "Bookmark the bookmarks"; roots as connected
  components that merge as discoveries bridge sessions. Build **flat**
  first; add the pyramid only when flat *provably* can't route within
  budget. The tree is shallow — don't pre-build it.
- **Multi-box federation.** Single-box for v1. Later: sync the small
  artifacts (sidecars + bookmarks) across boxes, fan out on local miss,
  root-per-box — never a global live root.
- **Embeddings / vectors / graph DB.** Permanently rejected (see "Why
  these constraints"), not deferred.

## Assumptions (overturn any, and the design shifts)

Made explicit so a future agent can check them against reality rather
than inherit them silently. Each is load-bearing.

1. **ICL is good enough now, and improving.** The model, given the right
   verbatim slices, can already judge saliency, relevance, and synthesis
   well. The whole bet rides on this; if model upgrades stop improving
   recall quality, the ordering ("ICL first") is wrong.
2. **Bookmarks actually get written.** The index is exactly what agents
   pinned — no more. We assume the bookmark habit is real and promptable;
   an empty index is silent total amnesia, not an error.
3. **Anchors are grep-findable because the same intelligence wrote both
   sides.** Index entries and query terms come from the same model
   lineage, so vocabulary matches. Cross-lineage recall (very different
   model) weakens this and may need keyword expansion.
4. **The filtered corpus fits the budget.** Locality + recency narrows
   the candidate set enough that grep + load stays within the window.
   True while session counts are modest; recursion is the escape hatch
   when it stops being true.
5. **Slices are small.** Bookmarks are spans, not whole transcripts, so
   several load at once. If agents bookmark huge regions, this breaks.
6. **Single box, single user, cooperative sessions.** Shared local
   filesystem (XDG paths), no adversarial or concurrent-conflicting
   writes. Multi-box and trust are deferred, not solved.
7. **Conclusions are written with grounding inline.** The warrant lives
   in prose; if the model asserts without sourcing, invalidation-by-grep
   fails. This is a skill discipline we *assume holds*, not a guarantee.
8. **Coverage honesty is reachable by prompting.** We assume the model
   can be made to report its searched-set and recognise a dry gather. If
   prompting can't achieve it, it must become an enforced contract.

## Future outlook, plans, and expectations

**The roadmap is pressure-driven, not date-driven.** Each phase ships
only when the previous one's *measured* pressure demands it. Building
ahead of pressure is the failure mode this whole design is reacting
against.

- **Phase 0 — now.** Design accepted; this doc is the artifact.
- **Phase 1 — flat recall skill (v1).** The `ls → rg → read → fall
  through → report searched-set` skill. *Gate:* hand-driven stitch across
  two real prior sessions, with calibrated coverage honesty. Nothing else
  ships until this holds.
- **Phase 2 — harden the loop.** Coverage manifest, candidate ranking,
  stop criterion, parallel `rg`. *Trigger:* token/latency cost becomes the
  felt pressure. Tune caps from the eval, not a priori.
- **Phase 3 — thin sidecar-ranking extension.** *Trigger only:* `ls | rg`
  ranking demonstrably can't scope well enough. Until then, skill-only.
- **Phase 4 — recursion ("bookmark the bookmarks").** Roots as connected
  components that merge as discoveries bridge sessions. *Trigger only:*
  flat grep provably can't route within budget (Assumption 4 breaks). The
  tree is shallow; we expect to defer this a long time.
- **Phase 5 — multi-box federation.** Sync the small artifacts (sidecars
  + bookmarks) across boxes, fan out on local miss, root-per-box.
  *Trigger:* work genuinely spans machines.

**What we expect to be true.**

- *The flat layer carries surprisingly far.* Most of the value lands in
  Phase 1; Phases 3–5 may never be needed for a single practitioner.
- *Coverage honesty is the recurring failure mode,* not retrieval
  precision. Budget review attention there.
- *Agentic grep keeps pace for free.* As models improve, recall quality
  improves with zero re-engineering (the bitter-lesson bet). If a model
  upgrade does **not** improve recall, treat it as evidence against the
  whole ordering and re-open "Why these constraints".

**The long-horizon expectation — the design should age by shrinking.**
This entire structure is scaffolding for *finite* windows. As context
windows grow toward effectively-infinite, the agentic-search layer should
**thin**, and ICL should absorb more of the work directly. We expect to
*delete* layers over time, not add them. A healthy version of this
project a few model-generations out is *smaller* than v1, not larger —
and in the limit it collapses back into the counterfactual it started
from: just load everything and let attention stitch. Until then, the
patterns it forces into the open — selective salience, lazy gather,
coverage honesty, warrant-in-prose — are themselves the prototype of what
an infinite-context model must do internally as attention allocation. The
external design is a readable model of the eventual internal one.

## Invariants

- **A marked slice is an entry point, never the whole record.** Always
  keep the path back to `store_path`, or selection becomes silent amnesia
  at the rate the model gets confident about what mattered.
- **The filesystem is the only substrate; the model is the index and the
  synthesiser.** No storage engine, no daemon, no central root.
- **One kind of bookmark.** A synthesis is just a bookmark whose warrant
  is other bookmarks — no `_derived` flag, no `_sources` schema. The
  warrant lives in the verbatim prose, so conclusions must be written with
  their grounding inline. Invalidation and weighting are grep +
  contradiction, not type tags.
- **Checkability over determinism.** Re-derive, detect contradiction,
  invalidate downstream.
- **Fresh by construction.** Recall greps live files; no index to rebuild.
