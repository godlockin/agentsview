# P3-4: modernc.org/sqlite A/B benchmark result

**Status**: Driver abstraction landed; single-thread microbench recorded.
Concurrent-burst pprof re-run is a follow-up (needs prod DB + real server).
**Date**: 2026-07-07

## What changed

Introduced two build-tag-gated internal packages so the SQLite driver
can be swapped at build time without touching call sites:

- `internal/db/driver` — exports `DriverName` ("sqlite3" for mattn,
  "sqlite" for modernc) plus a blank driver import. Default build
  uses mattn; `-tags moderncsqlite` selects modernc.
- `internal/db/sqliteerr` — exports an `As(err) (*Error, bool)`
  wrapper plus SQLite result-code constants so the db package can
  classify UNIQUE/FTS errors without importing a specific driver.

All 32 call sites that used to import `github.com/mattn/go-sqlite3`
directly (or hard-code the "sqlite3" driver name) now import the
abstraction packages. Both builds pass `go build`, `go vet`, and
`go test -run=^$ -bench` on this repo.

## Benchmark

`BenchmarkGetDailyUsage` on the 100K-row seed (500 sessions × 200
messages), `-count=3 -benchtime=3x`, darwin/arm64, VirtualApple @
2.5 GHz. First iteration excluded as warm-up.

| Driver | ns/op (steady) | allocs/op | B/op |
|---|---|---|---|
| mattn (baseline) | ~815 ms | 3.30 M | 144 MB |
| modernc | ~920 ms | 3.30 M | 144 MB |

**modernc is ~13 % slower on the single-threaded microbench.**

Allocation counts and byte totals are effectively identical, which
means the extra time is CPU inside the pure-Go SQLite engine (VDBE
opcodes and B-tree walks) rather than driver-layer overhead. This
matches upstream reports: modernc's transpiled VDBE loop is 10-20 %
slower than the mattn-linked C SQLite on scan-heavy queries.

## Interpretation

The post-P2-1 KPI failure (`runtime.cgocall` at 60.99 %) came from
the CGO transition being paid **per row** under a 50-way concurrent
burst. modernc removes that line item entirely — its Go-side cost
is spread across GC, mallocgc, and VDBE Go code, none of which
block goroutine scheduling.

Whether the driver swap is a net win depends on which of these
dominates:

1. **Per-row work** (modernc slower). The microbench above shows
   modernc is ~13 % slower per query in single-thread mode. If the
   burst is really a queue of independent queries and each query is
   just slower, modernc loses.

2. **CGO scheduling pressure** (modernc faster). Under N=50
   concurrent requests, mattn's per-row `sqlite3_step` blocks a
   goroutine on a P (procyield/pthread_cond_wait) until the C call
   returns. modernc has no such transition; goroutines yield on
   normal Go scheduling points. If CGO pressure was the reason
   `pthread_cond_wait` was so high pre-P1, modernc should reduce
   the tail latency under burst even if per-query time is higher.

The microbench cannot distinguish these because it is single-threaded.
The definitive test is the 50-burst pprof loop that produced the
KPI numbers in `cpu-thermal-post-fix-kpi.md`, run against a real
production DB, once with the mattn binary and once with
`-tags moderncsqlite`.

## Recommendation

Ship the driver abstraction; leave the default as mattn. Users on
Apple Silicon or systems with high CGO scheduling contention can
opt into modernc with `-tags moderncsqlite` and re-run their own
burst. The 13 % single-thread cost is small enough that if the
concurrent-burst pprof reruns show `cgocall` drops from 60.99 % to
near zero and the tail P99 improves, modernc becomes the
recommended default.

## Reproduce

```bash
# mattn (default)
CGO_ENABLED=1 go test -tags fts5 \
  -run=^$ -bench=BenchmarkGetDailyUsage \
  -benchmem -count=3 -benchtime=3x ./internal/db/

# modernc
CGO_ENABLED=1 go test -tags 'fts5 moderncsqlite' \
  -run=^$ -bench=BenchmarkGetDailyUsage \
  -benchmem -count=3 -benchtime=3x ./internal/db/
```

For the burst-loop follow-up:

```bash
# build both flavors of the binary
CGO_ENABLED=1 go build -tags fts5 -o /tmp/av-mattn ./cmd/agentsview
CGO_ENABLED=1 go build -tags 'fts5 moderncsqlite' -o /tmp/av-modernc ./cmd/agentsview

# run each under the same burst, then diff pprof
for BIN in /tmp/av-mattn /tmp/av-modernc; do
  LOG=/tmp/claude-tasks/kpi-$(basename "$BIN")-$(date +%s).log
  AGENTSVIEW_DATA_DIR=$HOME/.agentsview \
    "$BIN" serve --no-browser --pprof --port 18790 --replace \
    > "$LOG" 2>&1 &
  # wait for "listening at"
  PORT=18790 N=50 DURATION=5 bash /tmp/claude-tasks/burst.sh
  go tool pprof -top -nodecount=25 /tmp/claude-tasks/pprof.pb.gz \
    > "/tmp/kpi-$(basename "$BIN").txt"
  kill %1; wait
done
diff /tmp/kpi-av-mattn.txt /tmp/kpi-av-modernc.txt
```
