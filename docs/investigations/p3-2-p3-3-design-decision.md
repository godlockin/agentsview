# P3-2 / P3-3: cost_usd push-down and rate map move to SQL — design decision

**Status**: Investigated, not implemented. Skipped in favour
of P3-4 (A/B benchmark of `modernc.org/sqlite`).
**Date**: 2026-07-04

## Why these were skipped

After the P2-1 commit, the 30-second CPU profile taken
under a 50-burst on the production database showed
`runtime.cgocall` at 99 % of the captured 5.93 s of CPU.
That is the CGO transition into `mattn/go-sqlite3` for
every `sqlite3_step` call. The remaining 1 % is the Go
aggregation and JSON reparse loop, which P2-1 already
closed on the production data path.

P3-2 ("push `cost_usd` into SQL aggregate") and P3-3
("move the rate map to SQLite") target the Go aggregation
and the rate-map lookup, both of which are sub-1 % of the
post-P2-1 burst CPU on a 117 % burst. A focused look at
the two ideas confirmed the same conclusion: both are
correct moves, but neither will move the needle on the
burst that is actually being observed.

### P3-2 specifics

- `usage_events.cost_usd` exists in the schema but is 0 %
  populated on the production database (the legacy
  catalog-pricing path writes the rate into the in-memory
  pricing map and falls back to it, instead of materialising
  the cost into the table at write time). The PR that
  makes the rate loop write `cost_usd` per row is a
  data-path change that belongs with the existing
  fallback-pricing fix, not the daily-usage perf work.

- For `messages` the column does not exist; the legacy
  path stores cost inside the `token_usage` JSON and the
  parser does not break it out. Adding a `cost_usd REAL`
  column to `messages` and writing through the parser is a
  non-trivial data-migration that buys the same per-row
  fallback the daily-usage path already has via
  `dailyUsageAmounts`.

- After P2-1, the cost is already short-circuited via
  `if r.costUSD.Valid { cost = r.costUSD.Float64 }` in
  `dailyUsageAmounts`. Once cost_usd is populated the
  Go rate lookup is skipped. The `lookup` is an
  in-memory `map[string]modelRates` access and is not on
  any pprof top-10 — pushing it to SQL is a no-op for the
  measured profile.

### P3-3 specifics

- The rate map is read from `model_pricing` into a
  process-local map at every `GetDailyUsage` call. The
  load is one `SELECT model_pattern, ...` from a 200-ish
  row table, so the cost is in the microsecond range.
  Pushing the join into the daily-usage SQL would replace
  that read with a per-row lookup join, which is slower
  per row than the in-memory map and would also turn the
  single pre-loop read into a per-row join. Net: slower,
  not faster, for the current 800K-row scan.

- The "right" version of P3-3 is to push the rate map
  into the SQL aggregate at the GROUP BY level (one join
  per bucket instead of per row), but that needs the
  GROUP BY work to land first. The P3-1 design doc
  recorded why that is also deferred.

## What changed

- P3-2: skipped. `cost_usd` materialisation is a follow-up
  to the data write path, not the daily-usage read path.
- P3-3: skipped. The rate map is already in-memory and the
  per-row join is strictly worse than the current shape
  until the GROUP BY lands.

## Why we are doing P3-4 instead

The 99 %-cgocall share in the post-P2-1 profile is
"the CGO transition into mattn/go-sqlite3, paid per
`sqlite3_step`". Pushing work into SQL only changes
how much the C side has to do per call, not whether the
call is made. The two paths to actually reduce
`runtime.cgocall` share are:

1. **Cache the per-call work harder.** `mattn/go-sqlite3`
   already returns rows in batches; we are still
   streaming per row. A `Scan + cb`-style result
   handler that aggregates per batch could cut the CGO
   bounce count in half. The win is bounded by the
   number of distinct buckets the burst produces, which
   is on the order of 10-100, not 1.

2. **Drop the CGO driver entirely.** `modernc.org/sqlite`
   is a pure-Go port of SQLite. It keeps the same query
   plan, the same row layout, the same FTS5. The
   `runtime.cgocall` line item becomes zero. The new
   cost is a `mallocgc` hot path replacing the C heap
   and a small constant slowdown for ARM64 NEON
   paths that the C driver has but the Go one does not
   (yet).

Option (2) is the only way to actually get rid of the
99 % line item, and the right way to know whether it
pays off is to run the existing pprof burst loop with
both drivers and compare. P3-4 is that A/B.

P3-2 and P3-3 are documented here so the next person
who looks at this does not rediscover the same
"shouldn't this be a SQL aggregate?" question.
