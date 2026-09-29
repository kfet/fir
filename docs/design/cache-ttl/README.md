# Prompt cache TTL: 5m vs 1h

Status: decided, September 2026. Keep the 5-minute TTL (`CacheShort`) as the default.

## Conclusion

- Keep `CacheShort` for executor requests.
- Do not set `FIR_CACHE_RETENTION=long` on any host.
- Keep 1h retention for side queries only. This study does not cover them.

Every 1h policy we tested costs more than 5m on real fleet traffic:

| policy | Zulip relay | other workloads |
|---|---|---|
| global 1h | -9.5% | -7% to -8% |
| 1h refresh request at turn end | -97% | not run |
| 1h breakpoint at turn start, 5m in the tool loop | -7.0% | not run |
| 5m (today) | 0% | 0% |

Negative means higher cost. Percentages are of the total input-side cost.

## Cost model

Units of the base input price: 5m write 1.25, 1h write 2.00, read 0.10.
A 1h write costs 0.75 more than a 5m write. It pays back only when a read
comes 5m–1h after the write. Before 5m the 5m entry hits. After 1h both miss.

## Data

Session traces in `~/.config/fir/sessions` on zbox, since 2026-09-07:
about 380 sessions and 16,000 requests with usage numbers.
The Zulip relay is about 80% of requests and cost.

Caching at 5m already works: about 16 reads per written token.

Gap from one request to the next, all workloads:

| gap | share of requests |
|---|---|
| ≤ 5m | 94.4% |
| 5m–1h | 3.7% |
| > 1h | 1.9% |

Only the 5m–1h band can gain from 1h. Agent tool loops have gaps of seconds.
Cron work has gaps of hours. Human chat through the relay is the only
workload with real traffic in the band.

## Experiments

### 1. Global 1h, per workload (`ttl2.py`)

Pay the 0.75 premium on every write. Convert writes after a 5m–1h gap into reads.
Result: a loss on every workload (relay -9.5%). The premium on the many
tool-loop writes costs more than the few conversions save.

### 2. Refresh at turn end (`ttl3.py`, `probe.py`)

Idea: after a turn ends, send one small request that holds the final prefix at 1h,
so the next human message hits.

Of 1,330 relay turn ends, only 366 (28%) had a next message 5m–1h later.
The result depends on whether a 1h request can extend an existing 5m entry:

- If yes: +9.9%.
- If no: the refresh writes the full prefix at 1h each time, which is -97%.

`probe.py` tested this against the API with a new 23k-token prefix:

| step | request | usage |
|---|---|---|
| 1 | 5m breakpoint | 23,035 written at 5m |
| 2 | same prefix, `ttl: 1h`, 2s later | 23,035 read, no 1h write |
| 3 | same prefix, `ttl: 1h`, 400s later | miss, 23,035 written at 1h |

A 1h request reads a live 5m entry but does not extend it. So the refresh
is a full 1h write. Option rejected.

### 3. Mixed breakpoints (`ttl4.py`)

Put a 1h breakpoint on the prefix at the start of each turn (through the
human message). Keep 5m breakpoints in the tool loop. A human reply 5m–1h later
still hits the 1h part.

Of 1,398 turns, 361 (26%) hit the 1h part: +35.0M units.
The premium on all turn-start writes: -51.9M units. Net -7.0%.
Fir cannot know the next gap when it writes, so it pays on every turn to
win on one in four.

## Limits

- About 22% of trace lines with usage did not parse as one JSON object. The sample is large but not complete.
- The simulations use token counts from the traces, not a replay.
- Prices are ratios. A change in the 1h/5m price ratio changes the result.

## Repeat the study

```bash
python3 docs/design/cache-ttl/ttl2.py   # global 1h, per workload
python3 docs/design/cache-ttl/ttl3.py   # turn-end refresh
python3 docs/design/cache-ttl/ttl4.py   # mixed breakpoints
python3 docs/design/cache-ttl/probe.py  # API behaviour, about 7 minutes, under one cent
```

The scripts read `~/.config/fir/sessions`. `probe.py` uses the Anthropic OAuth
token from `~/.config/fir/auth.json`. Change the date cut-off in each script to
use a new window. Run the study again if relay usage or the price ratio changes.

Code: `resolveCacheRetention` and `cacheControlBlock` in
`pkg/ai/providers/anthropic.go`.
