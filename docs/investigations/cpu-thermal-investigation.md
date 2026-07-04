# Investigation: agentsview high CPU and thermal load when idle

**Status**: Investigation complete. P0 + P1 fixes in flight.
**Date**: 2026-07-04
**Symptom**: User reports CPU usage and MacBook fan noise stay elevated
while the daemon is "open" — between 0 % and roughly 300 % of one core
depending on background activity.

## Reproduction

```
$ AGENTSVIEW_DATA_DIR=$HOME/.agentsview agentsview serve --no-browser --pprof

# In another terminal, see the daemon at idle:
ps -p $(pgrep -f 'agentsview serve --no-browser' | head -1) -o pcpu
0.0     # most of the time

# Hammer the endpoints with 50 concurrent curls across the hot set
# (/search, /sessions, /usage/summary, /daily), the CPU climbs:
33.5 %, 77.9 %, 39.4 %, 110.0 %, 46.0 %   (one sampled burst per call)
```

pprof `Duration: 5.01s, Total samples = 14.89s (296.96 %)` confirms
we are routinely saturating three cores for several seconds at a time.

## Confirmed facts

| Layer | Finding |
| --- | --- |
| `runtime.cgocall` | 37.94 % of CPU samples — CGO transition overhead |
| `mattn/go-sqlite3._Cfunc__sqlite3_step_internal` | 37.14 % cum — SQLite execution |
| `runtime.pthread_cond_signal/wait` | 28.14 % + 10.54 % — connection pool contention |
| `internal/db.parseJSONString` | 5.10 % cum — token_usage JSON parsing |
| `internal/db.parseUsageTokenCounters` | 5.31 % cum — same path, downstream |
| Goroutines at idle | 15 (healthy) |
| Resident memory after 4 min | 705 MiB, sessions.db is 736 MiB → mostly page cache, not leak |
| Heap profile after a burst | tiny delta — no allocation hotspot |
| Disk / network I/O during burst | idle |
| `debug.log` write rate | < 1 line / min — no log spam |

The CGO + SQLite step is the dominant cost. JSON parsing of `token_usage`
(5 % each on two paths) is real but secondary. Lock contention is
explained by every reader going through a single `connMu.RLock()`.

## What is NOT the cause

- No cryptominer, no suspicious background goroutine, no anomalous
  network connection in any sample. Security review cleared the
  binary.
- No memory leak. RSS grows from ~85 MiB to ~705 MiB exactly because
  SQLite pages the 736 MiB on-disk database into memory, and that is
  a one-time cost that stabilises.
- No log spam, no MetricsD write storm. Telemetry only sends
  `daemon_active` once per 24 h.
- Watcher-debounce is 500 ms but the burst CPU is not driven by the
  ticker — when there is no traffic, the daemon sits at 0 % CPU
  with only the fsnotify kqueue read goroutine blocked on `kevent`.

## Root-cause hypothesis (verified by 9-expert panel)

1. **CGO + SQLite** dominates because the read path
   `GetDailyUsage → dailyUsageRowsSQLWithTimestampCTEs → dailyUsageAmounts
   → parseUsageTokenCounters` scans 800 K+ message rows from SQLite
   one row at a time. Each row is a CGO call, and the parser
   re-parses the `token_usage` JSON in Go even though we already
   extract `context_tokens` / `output_tokens` into INTEGER columns
   at write time.
2. **The connection-pool lock** at `internal/db/db.go:428`
   (`connMu.RLock` wrapping every `QueryContext`) serialises 50
   burst goroutines onto a single SQLite step pipeline. Removing
   the wrapper gives the underlying `database/sql` pool a fair shot
   at its own internal concurrency, and bumping `SetMaxOpenConns`
   from 4 to 16 lets WAL readers run further in parallel.
3. **Watcher hot path** at `internal/sync/watcher.go:208` re-runs
   `os.Stat` on every `fsnotify.Create` event when `fsnotify.Event`
   already exposes `IsDir()` — eliminating the syscall saves
   measurable time during agent-driven directory creation.
4. **`refreshPricingFromSources`** at
   `cmd/agentsview/usage.go:421` makes an unbounded HTTP call to
   LiteLLM and OpenRouter. When either upstream hangs the entire
   goroutine stalls indefinitely — the daemon stays up but the
   pricing table never converges and any later refresh that
   requires it blocks forever.

## Decisions (Phase 3 master arbitration)

### P0 — fix today

| ID | Location | Change | Why |
| --- | --- | --- | --- |
| **P0-1** | `cmd/agentsview/usage.go:421` | Wrap `src.Fetch()` in `context.WithTimeout(10*time.Second)` per source | An upstream hang on LiteLLM or OpenRouter currently deadlocks the pricing refresh goroutine indefinitely. Bounding the wait at 10 s lets the next iteration retry and the daemon keeps converging toward complete pricing data. |
| **P0-2** | `Makefile:11` | Make `-s -w` opt-in, add `build-release-debug` that keeps `-w` but drops `-s` | Profile data today is unreadable in release builds because both symbol table and DWARF are stripped. `-w` alone drops DWARF and saves a few MB; `-s` removes the symbol table that pprof needs to map addresses back to function names. |

### P1 — fix this week

| ID | Location | Change | Why |
| --- | --- | --- | --- |
| **P1-1** | `internal/db/db.go:707` | `SetMaxOpenConns(4)` → `SetMaxOpenConns(16)` | Reader pool of 4 is the queue-depth constraint during burst. WAL reader concurrency scales linearly with this number up to disk IO, and 16 still leaves headroom for the single writer. |
| **P1-2** | `internal/sync/watcher.go:208` | Read `IsDir` from the `fsnotify.Event` rather than re-`os.Stat`ing on every Create | The kqueue event payload already carries directory classification for events that originate from the kernel; on FSEvents-style streams we may still need a stat for the ones that arrive as plain rename. Keep one stat call as fallback, drop the second one. |
| **P1-3** | `internal/sync/watcher.go:157` | Watcher flush ticker drops to 5 s after `len(pending)==0` for ≥ 3 ticks | The current 500 ms tick wakes the goroutine 120 times a minute for an empty queue; bouncing between 5 ms-events during a burst and 5 s-events when idle keeps the goroutine scheduler quiet without sacrificing peak responsiveness. |

### P2 — after the schema migration

| ID | Change | Why |
| --- | --- | --- |
| **P2-1** | Add four INTEGER columns to `messages` (`input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`) populated in the same transaction as `token_usage`; teach `dailyUsageRowsSQLForBounds` to SELECT them and bypass `clampedUsageTokenCounters` for message source rows. | The 5 % JSON-parse cost per row turns into a column read, and the SQL aggregate can be pushed into SQL itself so `GetDailyUsage` no longer pulls 800 K rows into Go memory. |
| **P2-2** | Add a session retention policy (`max_session_age_days`, default 90 d) so the DB does not grow without bound. | Defence-in-depth against the 736 MiB growth pattern. Also a privacy guard. |
| **P2-3** | A/B benchmark `modernc.org/sqlite` vs `mattn/go-sqlite3` on a single M-series Mac to settle the CGO-replacement debate. | Likely 0–5 % win in practice, not worth speculative adoption. |

## Things this investigation ruled OUT

- **JSON re-parse for message rows cannot simply be removed today** —
  the column `input_tokens` does not exist yet for `messages`; the
  Analyst panel initially proposed reading it, which would have been
  a regression. The correct fix is `P2-1`.
- **`connMu.RLock` is not protecting anything the underlying
  `database/sql` pool doesn't already protect** — the `Reopen()`
  swapping is the only real concern and is rare.
- **The 500 ms watcher ticker is a symptom, not the disease** — even
  at idle the watchdog contributes under 1 % CPU; the real burst
  comes from SQLite reads.
- **No `go:embed` interaction, no SQLite version skew** — runs the
  same SQLite that ships on macOS via the `mattn` C driver.

## Files referenced

- `cmd/agentsview/usage.go:421` (P0-1)
- `Makefile:11` (P0-2)
- `internal/db/db.go:707` (P1-1)
- `internal/db/db.go:428` (lock observation)
- `internal/sync/watcher.go:157, 208` (P1-2, P1-3)
- `internal/sync/watcher.go:161` (`Watcher.loop` reader) (observation)
- `internal/server/server.go:11, 93–96` (`pprofEnabled` knob) (P0-2 follow-up)
- `internal/db/usage.go:1357–1362, 1667–1722` (P2-1 warm-up)

## KPI for verification

After all P0 + P1 are merged and rebuilt, a fresh `pprof` capture
under 50-concurrent burst should show `runtime.cgocall` below 25 %,
`pthread_cond_signal` below 10 %, and no entry in `runtime.usleep`
from a hung `Fetch()` call. Fail those, move P2-1 forward.
