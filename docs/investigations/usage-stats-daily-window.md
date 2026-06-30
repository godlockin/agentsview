# Investigation: usage daily window returns empty for recent data

**Status**: Open investigation
**Branch**: `fix/usage-stats-daily-window` (off `fork/main`)
**Related upstream issue**: [kenn-io/agentsview#904](https://github.com/kenn-io/agentsview/issues/904)

## Symptom (user report, 2026-06-28)

UI usage stats show `totals` but `days[]` and per-session
breakdowns are empty for the last ~30 days, even though recent
sessions clearly produced tokens.

## Reproduction (HTTP API)

```bash
# All-time totals — works
curl http://127.0.0.1:9765/api/v1/usage/summary
# → totals.inputTokens=264413885, totalCost=112.84

# Explicit range — broken
curl 'http://127.0.0.1:9765/api/v1/usage/summary?from=2026-06-20&to=2026-06-28'
# → days: [], totalCost: 0

# Same for May
curl 'http://127.0.0.1:9765/api/v1/usage/summary?from=2026-05-01&to=2026-05-31'
# → days: [], totalCost: 182.37 (the totals path uses a different SQL)
```

CLI path (issue #904) shows the same root cause: `agentsview
usage daily --since 7d` returns `$0.00` with exit 0 — silently
wrong rather than failing loud.

## Confirmed facts

| Layer | State |
| --- | --- |
| `messages.token_usage` (SQLite) | ✅ Recent rows exist: 6/28 has 171 messages with token_usage, 6/25 has 4 879 |
| `usage_events` table | ⚠️ Last insert 2026-05-30 — stale 29 days; different ingestion path |
| API `totals` field | ✅ Always populated (uses a different SQL path with no time-window predicate) |
| API `days[]` field | ❌ Empty for May/June ranges |
| API `session_counts` field | ❌ Missing in responses |

## Hypothesis

`internal/db/usage.go` `GetDailyUsage` builds a time-windowed
SELECT against `usageRowsSQLTemplate` and aggregates in Go. One
of the following is wrong:

1. The `from`/`to` SQL fragment comes back malformed for any
   range that starts after 2026-04-30 — possibly a hardcoded
   fallback or off-by-one in the date-padding code at lines
   ~1488–1750.
2. The post-query date filter at line ~1993 truncates all rows
   because of a timezone mismatch between SQLite TEXT timestamps
   and the parsed filter.
3. `messages.timestamp` is empty/null for newly-synced sessions
   after the 2026-06 schema change (dataVersion bump); the
   eligibility clause `m.timestamp ... OR s.started_at` should
   cover it but the fallback may not be wired into the daily
   aggregate.

Issue #904's root-cause paragraph points at `runUsageDaily`
passing raw `--since` strings straight into `db.UsageFilter`,
which appends `T00:00:00Z`. That explains the CLI path but not
why HTTP `from=2026-05-01` (a valid YYYY-MM-DD) also returns
empty. Two distinct bugs or one shared upstream of both.

## Investigation plan

1. Reproduce both the CLI and HTTP failures against the local
   DB at `~/.agentsview/sessions.db`.
2. Capture the SQL emitted by `GetDailyUsage` for a known-good
   range (May 2026) and a known-bad range (June 2026) via
   `db.db.QueryLog` or temporary `log.Printf`.
3. Diff the two queries to find the predicate that diverges.
4. Cross-reference `db.UsageFilter` and `service.UsageRequest`
   to confirm the HTTP path also passes the unparsed string.
5. Fix at the root: parse + validate `from`/`to` at the
   boundary, reject durations with a 400 on HTTP and a non-zero
   exit on CLI.
6. Add regression tests covering the May/June boundary and the
   CLI duration case from issue #904.

## Scope estimate

Half a day to a full day: SQL diagnosis + targeted fix + tests
+ rebake binary. The fix is localised to `internal/db/usage.go`
and possibly `internal/service/usage.go`; the HTTP handler
already returns 400 for malformed `from`/`to` per issue #904,
so the CLI path is the primary surface to repair.

## Not blocking

The `feat/apple-silicon-local` branch (apple-silicon builds,
self-hosted fonts, port default, run-offline targets) is
independent of this fix and can ship without it.