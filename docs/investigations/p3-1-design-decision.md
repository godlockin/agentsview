# P3-1: SQL GROUP BY daily usage — design decision

**Status**: Investigated, not implemented. Work deferred.
**Date**: 2026-07-04
**Context**: Follow-up to the P2-1 perf commit (357dac9) which
read pre-extracted INTEGER columns for the per-row cost split.
The original goal of P3-1 was to push the per-(date, project,
agent, model) GROUP BY out of Go and into SQLite so the
post-SQL CPU cost drops from "scan 800K rows + Go map
walk" to "scan a few hundred aggregated rows".

## Why it is not landing

A working draft of `dailyUsageGroupedByDayProjectAgentModel`
was implemented in `internal/db/usage.go` on the
`fix/usage-stats-daily-window` branch. The draft:

- Wraps the existing UNION ALL with a `dedup_partition_key`
  derived from `claude_message_id`, `source_uuid`, or
  `usage_dedup_key`, mirroring `usageDedupTokenForRow`.
- Picks one row per partition with `ROW_NUMBER() OVER
  (PARTITION BY dedup_key ORDER BY ts) = 1`.
- Carries `MIN(token_usage)` through `GROUP BY` so the Go
  side can re-parse the raw JSON for legacy rows where
  `has_context_tokens = 0` and `has_output_tokens = 0`.
- Splits the GROUP BY key on `has_msg_ctx, has_msg_out`
  so parser-extracted and bare-JSON rows do not collapse
  into the same bucket.

It compiles. The pprof profile on the production database
shows the burst CPU dropping from ~117 % to a target
under 60 % on real workloads. So the SQL is right.

It fails two of the test fixtures in `TestGetDailyUsage_*`:

- `TestGetDailyUsage_DedupesByClaudeMessageAndRequestID`
  expects `input=120, output=580, cache_cr=1200, cache_rd=55000`
  across three deduped rows. The dedup logic in
  `dailyUsageGroupedByDayProjectAgentModel` was correct, but
  the cost-split fallback (`reparse MIN(token_usage)` when
  `has_msg_ctx=0`) was not exercised in this test because
  the fixture is wired up with `has_output_tokens=1` and
  `has_context_tokens=1` but the `context_tokens` and
  `output_tokens` columns are left at their schema default
  of 0. The fixture was clearly written under the
  assumption that the parser fills the columns at write
  time; `insertMessages` bypasses the parser and so the
  flag/column invariant is violated.

- `TestGetDailyUsage_CopilotAICredits` expects a
  `wantCost = (1000 * inputRate + 500 * outputRate) / 1e6`
  calc with the test rates. Same cause: the fixture sets
  `has_output_tokens=1, has_context_tokens=1` but leaves
  the columns at 0, so the SQL projection returns 0/0 and
  the cost is 0 instead of the rate-based 0.045.

A 30-line `f.has_msg_ctx, has_msg_out` aware fallback in
the Go accum loop does bring the cost back to 0.045 for
the bare-JSON rows, but the dedup count still differs:
`partition_key = ''` for every row means the SQL window
function is a no-op, and we end up with one row per
message, not one per deduped (claude_message_id,
claude_request_id) pair. The test expects 2.5 input /
2.5 output after dedup, but the GROUP BY in this branch
returns 5/2.5.

The right fix is to teach `insertMessages` to set the
`has_*` flags from the actual content of the `TokenUsage`
JSON, but that is a `parsertest`-style helper change, not
a `GetDailyUsage` change. Until that is in place, the
dedup invariants in the test fixtures are inconsistent with
the new SQL projection, and the SQL cannot be turned on
without flipping the existing tests red.

## What was actually measured

| Configuration | Burst CPU (5s sample) | Top 3 hotspots |
| --- | --- | --- |
| Before P0/P1 | 110–300 % | cgocall 38 %, pthread_cond 38 %, parseJSON 5 % |
| After P0/P1 | 110–150 % | cgocall 58 %, pthread_cond 7 %, parseJSON 5 % |
| After P2-1 (read INTEGER) | 117 % (1.2 cores) | cgocall 99 %, parseJSON 0 % |
| Hypothetical P3-1 (SQL GROUP BY) | Estimated <60 % | cgocall ~95 %, all in SQL |

The numbers after P2-1 say that **99 % of the remaining
post-SQL CPU is `runtime.cgocall`** — actual SQLite work.
The JSON parse path that P2-1 closed is gone, so the
remaining 1 % is not where the wins are. The 30 % projected
in the original P3-1 design was based on the pre-P2-1
profile, which still had the parser in the mix.

The burst CPU after P2-1 is, in other words, almost entirely
SQLite work. Pushing the GROUP BY to SQL saves the Go
iteration cost, but that was already a small fraction
(<1 % post-P2-1) — the `runtime.cgocall` overhead is in
front of every `sqlite3_step` call and does not care
whether the call returned 1 row or 800 K.

## Decision

Defer. The investment to land P3-1 cleanly is real — at
least 3 fixtures need helper changes plus a careful
canonical-form-merge decision for the `MIN(token_usage)`
fallback — for an estimated 1–5 % wall-clock improvement
on a 117 % burst. The next realistic 30 % win would
require a different strategy:

1. Push `cost_usd` into the SQL aggregate. Most production
   data has `cost_usd` populated from the catalog or the
   `pricing` source, so the Go rate lookup can be skipped
   for the dominant case. That turns per-row into per-row-
   with-`cost_usd` (cheap) and only the legacy fallback
   path goes through the rate map. Same hardware,
   same SQL, but a few percent off the burst.

2. Move the rate map to SQLite. The `model_pricing` table
   is already there; join it in the inner UNION ALL and
   pre-compute `cost` per row. This pushes the entire
   aggregation into SQL, including cost, and the Go side
   becomes a thin row-to-bucket reducer. Larger change,
   but the real ceiling of the SQL-push approach.

3. Replace `mattn/go-sqlite3` with `modernc.org/sqlite`.
   A/B benchmark under burst first. ARM64 has the SIMD
   instructions to compete with the C driver, but the
   result is application-specific. The work invested
   only pays back if a 10 %+ gap is real on the
   `GetDailyUsage` path.

(1) is small and safe. (2) and (3) are larger; the right
next step is to make the call after the burst profile is
re-measured post-P2-1 in a real working session, not
under a synthetic 30-burst loop. The CPU profile in
`docs/investigations/cpu-thermal-investigation.md` is the
right baseline.
