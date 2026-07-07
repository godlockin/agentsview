# P3-4: modernc.org/sqlite becomes the default driver

**Status**: Default swapped to modernc. mattn kept as `-tags mattnsqlite`
opt-in. Concurrent-burst pprof reruns validate the swap.
**Date**: 2026-07-07

## What changed

- `internal/db/driver`: default build tag now selects modernc; mattn
  moved behind `-tags mattnsqlite`.
- `internal/db/sqliteerr`: same tag flip.
- `Makefile` build targets untouched — they build the new default
  (modernc) with `CGO_ENABLED=1` (still needed for the DuckDB
  transitive dep).

## Burst pprof A/B (production DB, N=50, 5s sample)

Both runs used the same 796 MiB `~/.agentsview/sessions.db`, same
`burst.sh` (5 hot endpoints × 50 concurrent curls), same
`--pprof --port 18790` server.

### mattn (old default)

```
Duration: 5.16s, Total samples = 30.64s (594.23%)
      flat  flat%   sum%        cum   cum%
    18.26s 59.60% 59.60%     18.29s 59.69%  runtime.cgocall
     1.58s  5.16% 64.75%      1.58s  5.16%  runtime.usleep
     1.54s  5.03% 69.78%      1.54s  5.03%  runtime.pthread_cond_signal
     1.38s  4.50% 74.28%      1.38s  4.50%  runtime.madvise
     1.38s  4.50% 78.79%      1.38s  4.50%  runtime.pthread_cond_wait
```

burst elapsed 15.76 s, 23 curls hit the 15 s timeout.

### modernc (new default)

```
Duration: 5.11s, Total samples = 22.95s (449.13%)
      flat  flat%   sum%        cum   cum%
     7.87s 34.29% 34.29%      7.87s 34.29%  syscall.rawsyscalln
     6.87s 29.93% 64.23%      6.87s 29.93%  runtime.usleep
     5.06s 22.05% 86.27%      5.06s 22.05%  runtime.pthread_cond_wait
     1.91s  8.32% 94.60%      1.91s  8.32%  runtime.pthread_cond_signal
     0.13s  0.57% 95.16%      0.13s  0.57%  runtime.memmove
```

`runtime.cgocall` no longer appears in the top 25 — the line item
is gone. burst elapsed 15.45 s, 4 curls hit the timeout.

## KPI gate

| KPI | Target | mattn | modernc | Verdict |
|---|---|---|---|---|
| `runtime.cgocall` share | < 25 % | 59.60 % | 0 % | modernc **PASS** |
| `pthread_cond_signal` share | < 10 % | 5.03 % | 8.32 % | both PASS |
| `usleep` from hung Fetch | 0 | 0 | 0 | both PASS |
| Total 5 s CPU | — | 30.64 s | 22.95 s | modernc **−25 %** |
| Burst curls timing out (>15 s) | — | 23 | 4 | modernc **−83 %** |

## Trade-off

modernc shifts cost from CGO transitions to Go-side goroutine
scheduling. `pthread_cond_wait` (22 %) and `usleep` (30 %) are
higher on modernc because 50 goroutines contend on the SQLite
pager mutex inside `modernc.org/sqlite/lib.mutexEnter`; the mattn
version paid the same cost as `pthread_mutex_lock` inside the C
call. Net effect on wall-clock burst time is 15.76 s → 15.45 s
(2 % faster) and tail-latency timeouts 23 → 4 (83 % better),
because goroutines can be preempted while waiting instead of
holding an OS thread.

The single-thread microbench (`BenchmarkGetDailyUsage`) still
shows modernc 13 % slower per op, but that shape does not
represent the workload that causes CPU spikes. Under real
concurrent load the driver swap is a clear win.

## What did not go away

Both drivers still spend the majority of their time in the
SQLite pager (VDBE walking the same 800K-row scan). The remaining
work is:

- P2-2: extend the INTEGER fast path to the `usage_events` and
  `cursor` UNION ALL branches (still 6.5 % cum on
  `parseJSONString` post-P2-1).
- Reader-pool sizing: verify short SELECTs use the reader path
  rather than the writer lock in the modernc code path (mutex
  contention share suggests some queries still hit the writer).
- Pre-aggregated daily-usage rollup: turn the 800K-row scan into
  a bucket lookup. This is the only fix that will reduce
  `_sqlite3VdbeExec`'s 75 % cum share.

## Reverting to mattn

```bash
CGO_ENABLED=1 go build -tags 'fts5 mattnsqlite' ./cmd/agentsview
```

The abstraction packages (`internal/db/driver`,
`internal/db/sqliteerr`) hide the choice from every call site,
so the flip is one build tag.
