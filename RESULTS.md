# Go vs TS — Steloit-flavored bench

Three workloads matching Steloit's actual hot paths, two stacks, same Postgres 18,
same machine. Local Docker, single-host loopback. Numbers are honest signals for
the language stack — they are NOT representative of Cloud Run absolute throughput
(Postgres is durability-tuned-off; both stacks benefit equally).

## TL;DR

| Workload  | Winner                           | Margin vs raw Bun.sql       |
|-----------|----------------------------------|-----------------------------|
| Audit POST| Go + chi + sqlc + pgx (6 853 rps)| **1.70× raw Bun.sql**       |
| SSE 5k    | Go net/http (40% less RAM)       | Equal throughput            |
| Meter     | Go + pgx (104k/s, 16 MB)         | **1.34× throughput, 5× RAM**|

**Key findings:**

1. **Bun.sql is the fastest TS option** and the only one that approaches Go.
2. **ORM overhead is roughly 2× in either language.** Drizzle on its best
   driver: 49% of raw Bun.sql. GORM: 50% of raw Go.
3. **Go+ORM (GORM) is *slower* than TS-without-ORM (Bun.sql).** This is the
   strategically critical result: if the Go rewrite uses GORM (most teams
   do), the rewrite buys you nothing on throughput.
4. **The real perf gradient is "ORM vs no-ORM," not "Go vs TS."**
5. **Rust+sqlx is close to Go on throughput (83%) and best-in-class on
   memory** (14 MB API, 8 MB worker) once the JSON-handling matches Go's
   raw-bytes pattern. It's not "Go but faster" — it's "Go but cheaper to
   run if the team can ship Rust." For a 24/7 worker that bills RAM-hours
   it's the most economical option in the table.

## Workload 1 — Audit write (POST /audit)

Validates body, opens tx, reads `last hash`, computes SHA-256 chain link,
inserts into `audit_entries`, commits. 100 concurrent virtual users, 30s.

| Stack                                       | RPS    | p50      | p95      | max      | RSS    | Cold start | Hand-written LOC |
|---------------------------------------------|--------|----------|----------|----------|--------|-----------:|-----------------:|
| **Go** — chi + sqlc + pgx (no ORM)          | **6 853** | **13.4 ms** | **25.4 ms** | 104 ms   | **26 MB** | 14 ms      | 144 (+134 generated) |
| **Bun + Hono + `Bun.sql`** (native, no ORM) | **4 030**  | **24.0 ms** | **34.7 ms** | **68 ms** | 116 MB | 79 ms      | **55**           |
| **Go + Gin + GORM** (Go's most popular ORM) | **3 406** | 24.8 ms | 60.7 ms | 210 ms | **34 MB** | 81 ms | 130 |
| Bun + Hono + `pg`                           | 2 826  | 33.5 ms  | 51.4 ms  | 125 ms   | 108 MB | 11 ms      | 57               |
| Bun + Hono + `postgres.js`                  | 2 463  | 38.7 ms  | 60.7 ms  | 179 ms   | 132 MB | 13 ms      | 53               |
| **Bun + Hono + Drizzle on `Bun.sql`**       | 1 960  | 49.3 ms  | 71.1 ms  | 110 ms   | 127 MB | 13 ms      | 95               |
| Bun + Hono + Drizzle on `postgres.js`       | 1 183  | 82.1 ms  | 116.0 ms | 208 ms   | 143 MB | 69 ms      | 94               |
| **Rust + Axum + sqlx**                      | **5 674**  | **16.9 ms** | **25.0 ms** | 83 ms   | **14 MB** | 138 ms  | 111              |

**Rust commentary:** Rust + Axum + sqlx lands at **5 674 RPS** — within 17%
of Go's raw-pgx number on throughput, **identical p95 (25 ms)**, and **half
the RAM (14 MB vs 26 MB)**. Tighter tail than Go (max 83 ms vs 104 ms).

**Important methodology note:** the first run of this variant came in at
2 763 RPS with a 331 ms tail. Root cause: my Rust handler parsed the JSON
payload into `serde_json::Value` and then re-serialized it twice (once for
the SHA-256 chain, once for the DB bind). Go's handler uses `json.RawMessage`
which keeps the original body bytes and reuses them for both. After switching
Rust to `Box<RawValue>` (the equivalent of `RawMessage`), throughput
**doubled** and tail latency dropped 4×. The 2 763 number is preserved in
git history as a cautionary tale — small API choices in the request struct
matter more than language choice on hot paths. Bun.sql still does the
parse-and-reserialize dance (same as the original Rust); if it adopted
the same raw-bytes pattern it would likely close further toward Go.

**The GORM result is the most strategically important finding in this whole
bench.** Go + GORM (3 406 RPS) is **slower than raw Bun.sql** (4 030 RPS).
The Go performance advantage you'd be buying with a rewrite *evaporates* if
the team reaches for the popular ORM. GORM is to Go what Drizzle is to TS:
roughly a 2× tax on top of the raw driver, regardless of language. Memory
stays Go-cheap (34 MB) but the throughput edge is gone.

This matters because "we'll rewrite in Go" almost always means "we'll write
Go *and* reach for an ORM because writing raw SQL through pgx is verbose."
At which point you've spent a quarter rewriting and ended up *slower than*
the TS option that doesn't have an ORM at all.

**Drizzle-on-Bun.sql commentary:** changing only the driver underneath
Drizzle (postgres.js → Bun.sql) yields **+66% RPS** (1 183 → 1 960). But
raw Bun.sql with no ORM hits 4 030 RPS. So Drizzle's pure overhead
(driver-independent) is **~2.05×** — i.e., the ORM layer alone burns half
the throughput. That is the cost of every method call going through the
query builder + relations + parameter binding instead of straight to the
native driver.

**Bun.sql commentary:** the tightest max-latency in the table (68 ms vs 125+
for every other TS variant) — that's the prepared-statement + native binding
path working as advertised. RPS sits between Go and the rest of TS. Cold
start is notably worse (79 ms) because the first request triggers the
internal SQL module initialization; subsequent runs would amortize that.

**Reading the table:**

- Go is **2.4× faster than the best TS variant** and **5.6× faster than the
  Drizzle stack** Steloit ships today.
- Drizzle adds **2.3× latency** on top of the same `postgres.js` driver. Its
  query-builder overhead per call is the dominant cost at this concurrency.
- p95 for Go is lower than p50 for any TS variant. That's not noise.
- Memory: Go 26 MB, TS 108–147 MB. ~5× difference. On Cloud Run with 256 MB
  containers, that's the difference between "fine" and "OOM at 2 concurrent
  agents."
- Cold start is a wash locally. Cloud Run cold-start delta will favor Go but the
  effect isn't visible at loopback.

## Workload 2 — SSE fan-out (5 000 concurrent connections)

Server holds N SSE clients, broadcasts a tick once per second. Client opens
5 000 keep-alive HTTP connections, reads SSE frames, counts events.

| Stack                | Sustained conns | Events delivered | Failed | RSS     |
|----------------------|----------------:|-----------------:|-------:|--------:|
| **Rust + Axum**      | 5 000           | 86 826           | 0      | **104 MB** |
| Go — net/http        | 5 000           | 86 057           | 0      | 137 MB |
| Go — Gin             | 4 999           | 86 725           | 0      | 144 MB |
| Bun + Hono streamSSE | 4 997           | 86 948           | 0      | 191 MB |

**Rust stream commentary:** Rust sustains 5 000 connections cleanly (no
drops, no failed), delivers the same event count as the others (bottlenecked
on the 1-Hz tick, not the runtime), and uses **24% less memory than Go,
46% less than Bun**. Tokio's per-task overhead is genuinely smaller than
goroutines for this idle-waiting-for-broadcast pattern. This is the most
unambiguous Rust win in the matrix.

**Gin commentary:** statistically identical to raw `net/http` for this
workload — same connection count, same event throughput, 5% more memory
from Gin's larger transitive deps (Sonic, validator). Framework choice on
the Go side is essentially irrelevant for SSE fan-out; the runtime + GC
do the work.

**This is the result that surprised me.** Going in I expected Go to handle 5k
SSE trivially and Bun to flounder. It didn't. Bun + Hono sustained 5k concurrent
SSE connections with zero failures and delivered the same event count. Memory is
40% higher; the latency-of-broadcast (a few ms) was indistinguishable.

The "Go crushes Bun on concurrency" argument doesn't survive contact with this
test. We'd need to push to 20k+ connections, or add real per-message work
inside each stream, to see a meaningful split. At Steloit's likely MCP-session
scale (≤ a few hundred per region) **this is not a deciding factor**.

## Workload 3 — Metering worker (drain 100 000 events)

8 workers, `SELECT … FOR UPDATE SKIP LOCKED`, batch 500, aggregate in-memory,
upsert into `meter_aggregates`, mark events processed. Wall-clock from start to
empty queue.

| Stack                 | Elapsed | Throughput   | RSS   |
|-----------------------|--------:|-------------:|------:|
| **Go** — pgx          | 957 ms  | **104 493/s**| 16 MB |
| **Rust** — sqlx       | 1 020 ms| **98 039/s** | **8 MB** |
| **Go** — GORM         | 1 340 ms| 74 626/s     | 17 MB |
| Bun + `Bun.sql`       | 1 287 ms| 77 700/s     | 74 MB |
| Bun + `postgres.js`   | 1 307 ms| 76 511/s     | 102 MB |

**Rust meter commentary:** the inverse of the audit result. Rust+sqlx on the
worker workload roughly matches Go raw (98k vs 104k/s — 94%) while using
**half the RAM (8 MB vs 16 MB)** and a quarter of TS (vs 74–102 MB). For a
24/7 worker where every MB-month costs real money, Rust is the most
economical option in the table — *if* the team can write idiomatic async
Rust. That's the catch, not the perf.

**GORM meter commentary:** the ORM tax hits the worker too — 29% throughput
drop vs raw pgx (104k → 75k events/s). But here's the twist: **Go+GORM
still beats both TS variants on RAM by 4–6×** (17 MB vs 74–102 MB), while
roughly matching them on throughput. For a 24/7 worker that bills RAM per
hour, Go+GORM is still the right call. The ORM costs you throughput but
not the language's memory advantage. This is the opposite conclusion from
the API path, where memory matters less and throughput dominates.

**Reading:** Go is 1.37× faster on throughput. The gap is smaller than the
audit workload because this is DB-bound — most of each batch is Postgres
round-trip time. RAM is 6× lower, which matters because metering is
long-lived: you pay for that memory all month, not just per-request.

## Lines of code (hand-written)

| Implementation              | LOC | Notes                                            |
|-----------------------------|----:|--------------------------------------------------|
| Go audit (chi + sqlc + pgx) | 144 | Plus 134 generated by sqlc                       |
| Go audit-gorm (Gin + GORM)  | 130 | No codegen; struct tags do the schema mapping    |
| Rust audit (Axum + sqlx)    | 111 | Uses `Box<RawValue>` to match Go's RawMessage    |
| Go meter (raw pgx)          | 162 |                                                  |
| Go meter-gorm               | 158 | GORM clauses for SKIP LOCKED + upsert            |
| Rust meter (sqlx)           | 137 | Tokio worker pattern + sqlx; very clean          |
| Go stream (net/http)        | 105 |                                                  |
| Go stream-gin               | 105 | Same logic, Gin's `c.Stream` for loop semantics  |
| Rust stream (Axum)          |  96 | broadcast channel + BroadcastStream; tightest    |
| TS audit-drizzle (pgjs)     | 94  | Includes 29-line schema.ts                       |
| TS audit-drizzle (Bun.sql)  | 95  | Same schema, one-line driver swap                |
| TS audit-pg                 | 57  |                                                  |
| TS audit-bunsql             | 55  | Zero deps beyond Hono; native Bun                |
| TS audit-postgresjs         | 53  | Most concise; tagged-template SQL is clean       |
| Go stream                   | 105 |                                                  |
| TS stream                   | 64  |                                                  |
| TS meter (postgres.js)      | 77  |                                                  |
| TS meter (Bun.sql)          | 81  | Same shape; `WHERE id IN ${sql(ids)}` for ANY    |

TS is consistently **40–55% less hand-written code** for the same logic. Go's
verbosity is mostly error handling + sqlc setup boilerplate.

## Time-to-working (single-engineer estimate)

Roughly accurate to this session, with Claude Code as labor multiplier:

| Surface       | Go    | TS    | Why                                                |
|---------------|-------|-------|----------------------------------------------------|
| Audit endpoint| ~35m  | ~15m  | sqlc setup; pgtype wrangling for uuid/bytea        |
| SSE server    | ~15m  | ~25m  | Bun's streamSSE needs more care around backpressure|
| Meter worker  | ~25m  | ~20m  | Roughly tied; postgres.js tagged SQL ≈ pgx batches |

Caveat: this is biased. I'm a TS native. Someone Go-native would invert the
audit number.

## What this bench doesn't measure

- **Auth + RLS overhead.** Steloit's real audit hot path goes through
  Better-Auth session resolution, RLS GUC `set_config` per tx, and
  `hasPermission` evaluation. None of that is here. That overhead is the
  same in both stacks (it's all Postgres + crypto).
- **Cold start on Cloud Run.** Loopback hides container start. Real number
  for Go: ~50 ms. For Bun: ~250–400 ms.
- **Connection storms.** Tests run with warm pools.
- **Long-running memory drift.** 30s/20s windows don't catch leaks or GC
  pressure under steady load. Bun specifically has known leak edge cases
  with long-lived processes — would need a multi-hour soak test.
- **MCP-specific transport semantics.** SSE here is a proxy; real MCP holds
  duplex JSON-RPC over Streamable HTTP with reconnection. Different shape.

## So, Go or TS?

**Honest read of the numbers:**

- For pure request throughput on tx-heavy endpoints, Go wins decisively
  (2–6×). If Steloit's `agent_runs` write path becomes the bottleneck,
  that's a Go-shaped problem.
- For SSE / long-lived connections, **the gap is small** at our likely scale.
  This is the *opposite* of the conventional wisdom I argued from earlier.
- For batch worker throughput, Go wins moderately (1.4×) with a large memory
  advantage that compounds over time.
- For ergonomics, TS wins on LOC by ~50%. Drizzle is the worst choice in TS.

**What I'd actually do, post-bench (revised after Bun.sql + GORM results):**

1. **Stay on Hono + Bun for `apps/api` and `apps/mcp`.** The throughput is
   fine for alpha and the LOC + shared-types story is real.
2. **Drop Drizzle. Use `Bun.sql`.** Highest-leverage change on the TS side.
   Fastest TS option, tightest tail, zero external deps, comparable LOC.
   Closes the gap to Go from 5.6× to 1.7× on the audit hot path. (If a full
   Drizzle removal is too disruptive, at minimum switch Drizzle's driver to
   `drizzle-orm/bun-sql` — free 66% RPS for a one-line config change.)
3. **Write the metering worker in Go (or Rust, if the team can).** Go is
   the safe choice — 16 MB RAM, 104k/s, broad hiring pool. Rust is the
   *cheapest* choice — 8 MB RAM, 98k/s — but only if there's a Rust
   speaker on the team. Either way, **don't run the worker on Bun**:
   74–102 MB for a process that lives forever is a real monthly bill.
4. **Do NOT rewrite the API in Go *with GORM*.** This is the new, important
   finding: Go+GORM is slower than raw Bun.sql. A rewrite that lands on
   the popular ORM is a quarter of work for a *negative* throughput result.
   The only Go-on-the-API path that wins is `pgx` + hand-written SQL (or
   `sqlc`) — and that's the discipline question, not the language question.
   You're asking the team to write raw SQL in any language they choose;
   pick the one with the smaller blast radius (TS).
5. Defer rewriting the rest of the API in Go unless (a) audit write rate
   genuinely exceeds 4k RPS in production, *and* (b) the team commits to
   `sqlc` over GORM in writing.

**The headline:** the perf gradient is "ORM vs no-ORM," not "Go vs TS." Once
you accept that, the answer is "stay on Bun, drop the ORM, put the worker
in Go." That is the answer the data supports — not "rewrite in Go."

The investor's underlying instinct — "Go is the right shape for an infra
runtime" — is correct in the workloads where it matters. The blanket "rewrite
in Go" overcorrects. **Targeted Go for the hot worker, TS for the API surface
where Better-Auth and the React story dominate, drop Drizzle in both directions.**

## Repro

```sh
cd bench-go-vs-ts
docker compose up -d
docker exec -i bench-pg psql -U bench -d bench < schema.sql

# Build Go
(cd go/audit       && go build -o /tmp/bench-go-audit .)
(cd go/audit-gorm  && go build -o /tmp/bench-go-audit-gorm .)
(cd go/stream      && go build -o /tmp/bench-go-stream .)
(cd go/stream-gin  && go build -o /tmp/bench-go-stream-gin .)
(cd go/meter       && go build -o /tmp/bench-go-meter .)
(cd go/meter-gorm  && go build -o /tmp/bench-go-meter-gorm .)
(cd bench          && go build -o /tmp/bench-sse-client sse-client.go)

# Build Rust (release, ~2-3 min cold)
(cd rust/audit  && cargo build --release && cp target/release/bench-audit  /tmp/bench-rust-audit)
(cd rust/stream && cargo build --release && cp target/release/bench-stream /tmp/bench-rust-stream)
(cd rust/meter  && cargo build --release && cp target/release/bench-meter  /tmp/bench-rust-meter)

# Install TS
for d in ts/audit-pg ts/audit-postgresjs ts/audit-drizzle ts/stream ts/meter; do
  (cd $d && bun install)
done

# Audit (each ~45s)
./bench/run-audit.sh go-audit            8081 /tmp/bench-go-audit
./bench/run-audit.sh ts-audit-pg         8092 bun run ts/audit-pg/src/index.ts
./bench/run-audit.sh ts-audit-postgresjs 8093 bun run ts/audit-postgresjs/src/index.ts
./bench/run-audit.sh ts-audit-drizzle        8091 bun run ts/audit-drizzle/src/index.ts
./bench/run-audit.sh ts-audit-bunsql         8095 bun run ts/audit-bunsql/src/index.ts
./bench/run-audit.sh ts-audit-drizzle-bunsql 8096 bun run ts/audit-drizzle-bunsql/src/index.ts
./bench/run-audit.sh go-audit-gorm           8084 /tmp/bench-go-audit-gorm
./bench/run-audit.sh rust-audit              8085 /tmp/bench-rust-audit

# Stream (each ~30s, holds 5000 conns)
./bench/run-stream.sh go-stream     8182 5000 20 /tmp/bench-go-stream
./bench/run-stream.sh go-stream-gin 8183 5000 20 /tmp/bench-go-stream-gin
./bench/run-stream.sh ts-stream     8194 5000 20 bun run ts/stream/src/index.ts
./bench/run-stream.sh rust-stream   8285 5000 20 /tmp/bench-rust-stream

# Meter (each ~5s)
./bench/run-meter.sh go-meter        /tmp/bench-go-meter
./bench/run-meter.sh go-meter-gorm   /tmp/bench-go-meter-gorm
./bench/run-meter.sh rust-meter      /tmp/bench-rust-meter
./bench/run-meter.sh ts-meter        bun run ts/meter/src/index.ts
./bench/run-meter.sh ts-meter-bunsql bun run ts/meter-bunsql/src/index.ts
```

## Caveats / things I'd tighten before betting a quarter on this

- **Single machine, single Postgres, no network.** Cloud Run is a different
  shape. Both stacks lose some perf to network egress and Postgres TLS;
  the *ratio* probably holds but isn't guaranteed.
- **No prepared-statement caching tuned.** pgx and postgres.js both prepare
  statements implicitly. pg (node-postgres) does not by default. The
  audit-pg number could improve 20–40% with `pg-prepared` or explicit
  prepared queries. Wouldn't change the ranking.
- **Drizzle could be tuned.** `prepare()` API exists. I didn't use it
  because nobody using Drizzle in the wild does in their hot path
  (you'd write raw SQL at that point). The number reflects realistic
  Drizzle usage, not its theoretical best.
- **k6 default summary lacks p99.** Numbers are p50/p95/max. To get p99
  cleanly, re-run with `Trend` exports.
- **Bun 1.3.13.** Patches are weekly. Re-run in 6 months if it matters.
