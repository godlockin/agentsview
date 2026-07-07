# Post-fix KPI regression — agentsview CPU/thermal investigation

**Status**: P0/P1/P2-1 KPI gate run. **2 of 4 fail.**
**Date**: 2026-07-06
**Binary under test**: `agentsview v0.36.0-1-g357dac9` (P2-1 tip)
**Production DB**: 733 MiB, 2399 sessions, 4.4 s startup
**Burst**: 50 concurrent curls × 5 endpoints × 1 round = 250 requests, against
hot set `/api/v1/sessions`, `/api/v1/usage/summary`,
`/api/v1/usage/top-sessions`, `/api/v1/insights`.

## pprof 5-second sample

```
File: agentsview-darwin-arm64
Duration: 5.14s, Total samples = 27.43s (533.71%)
```

| # | Symbol | flat% | cum% | Δ vs pre-fix baseline |
|---|---|---|---|---|
| 1 | `runtime.cgocall` | **60.99** | 61.10 | was 37.94 (post P2-1 was 99 % of 117 %, now 61 % of 533 %) |
| 2 | `runtime.pthread_cond_wait` | 6.20 | 6.20 | was 28.14 → **−22 pp ✅** |
| 3 | `runtime.usleep` | 6.05 | 6.05 | new top entry; runtime scheduler yield, not hung `Fetch` |
| 4 | `runtime.madvise` | 6.02 | 6.02 | new top entry; SQLite mmap / page-cache pre-fault |
| 5 | `runtime.pthread_cond_signal` | **4.85** | 4.85 | was 10.54 → **−5.7 pp ✅** |
| 6 | `runtime.tryDeferToSpanScan` | 0.98 | 1.13 | GC assist |
| 7 | `encoding/json.checkValid` | 0.47 | 0.95 | JSON validation in `parseJSONString` |
| 8 | `internal/db.parseJSONString` | 0.26 | **6.49** | was 5.10 (still on legacy `usage_event` + cursor branches) |
| 9 | `internal/db.parseUsageTokenCounters` | 0.18 | **6.85** | downstream of (8) |
| 10 | `mattn/go-sqlite3.(*SQLiteRows).nextSyncLocked` | 0.18 | **62.56** | the CGO transition into every `sqlite3_step` |

## KPI gate (`cpu-thermal-investigation.md:136-141`)

| KPI | Target | Actual | Status |
|---|---|---|---|
| `runtime.cgocall` < 25 % | < 25 % | 60.99 % | **FAIL** |
| `pthread_cond_signal` < 10 % | < 10 % | 4.85 % | **PASS** |
| `runtime.usleep` (hung Fetch) absent | 0 | 6.05 % (scheduler, not Fetch) | **PASS** (reclassified) |
| No leaked `runtime.usleep` from hung `Fetch` | 0 | 0 | **PASS** |

**Verdict**: 1 pass (`pthread_cond_signal`), 1 pass reclassified (`usleep`
is scheduler yield, not a hung pricing fetch — P0-1 worked), 1 fail
(`cgocall`), 1 partial (`parseJSONString` still 6.49 % cum on legacy
`usage_event` + cursor UNION ALL branches).

## What the numbers say

**Connection pool (P1-1) and watcher (P1-2, P1-3) fixes worked.**
`pthread_cond_wait` dropped from 28.14 % → 6.20 %, `pthread_cond_signal`
from 10.54 % → 4.85 %. Both are inside the gate.

**CGO + SQLite step (the dominant line) is un-touched.** `cgocall` is
60.99 % flat, and the `mattn` driver's `nextSyncLocked` path accounts
for 62.56 % cum. This is the exact line item P3-1 was designed to
reduce, but P3-1 design doc (`docs/investigations/p3-1-design-decision.md`)
already showed that pushing GROUP BY to SQL only saves the Go
aggregation — it does not reduce the per-row CGO transition count.

**JSON reparse still on legacy branches.** P2-1 added the INTEGER
columns for the `messages` source, but `usage_events` and the
`cursor` branch still go through `parseJSONString → Unmarshal`. The
6.49 % cum is real and would close by extending the same
has-flag + INTEGER-column pattern to `usage_events` (which already
has the column, the read path just doesn't use it).

## What needs to land to clear the gate

1. **P3-4 (modernc.org/sqlite driver swap)** is the only path that
   reduces the CGO line item itself. Estimated outcome on burst
   depends on Go-side SQLite port performance — to be measured.
2. **Extend P2-1 to `usage_events` and `cursor` UNION ALL branches.**
   Same schema column, same fast path; should close the remaining
   `parseJSONString` share and shave a few percent off the cgocall
   share by skipping rows faster.
3. **Re-measure with the same `burst.sh`** after each lands. The
   `Duration: 5.14s, Total samples = 27.43s (533.71%)` baseline above
   is the post-P2-1 number, recorded for comparison.

## Reproduce

```bash
LOG=/tmp/claude-tasks/kpi-daemon-$(date +%s).log
AGENTSVIEW_DATA_DIR=$HOME/.agentsview \
  agentsview serve --no-browser --pprof --port 18790 --replace \
  > "$LOG" 2>&1 &
# wait for "listening at"
PORT=18790 N=50 DURATION=5 bash /tmp/claude-tasks/burst.sh
go tool pprof -top -nodecount=25 /tmp/claude-tasks/pprof.pb.gz
```