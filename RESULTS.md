# Go vs TS — Steloit-flavored bench

Three workloads matching Steloit's actual hot paths, two stacks, same Postgres 18,
same machine. Local Docker, single-host loopback. Numbers are honest signals for
the language stack — they are NOT representative of Cloud Run absolute throughput
(Postgres is durability-tuned-off; both stacks benefit equally).

## TL;DR

| Workload  | Go winner-margin           | TS (best variant)       | TS (production stack)   |
|-----------|----------------------------|-------------------------|-------------------------|
| Audit POST| **1.7× RPS vs Bun.sql, 5.6× vs Drizzle** | **Bun.sql (4 030 rps)** | Bun+Hono+Drizzle (1214 rps) |
| SSE 5k    | **40% less memory, equal** | Bun+Hono (191 MB)       | same                    |
| Meter     | **1.34× throughput, 5× less RAM** | Bun.sql (77.7k/s, 74 MB) | postgres.js (77k/s, 102 MB) |

**Two new findings:**

1. **Bun.sql changes the picture meaningfully.** It's 1.4× faster than `pg`,
   1.6× faster than `postgres.js`, 3.3× faster than Drizzle. Latency tail is
   tighter than any other TS variant (max 68 ms vs 125–209 ms). Memory is in
   the same band as postgres.js but the throughput-per-MB is the best TS option.
2. **Drizzle remains the worst choice on the TS side** by a wide margin.

## Workload 1 — Audit write (POST /audit)

Validates body, opens tx, reads `last hash`, computes SHA-256 chain link,
inserts into `audit_entries`, commits. 100 concurrent virtual users, 30s.

| Stack                                       | RPS    | p50      | p95      | max      | RSS    | Cold start | Hand-written LOC |
|---------------------------------------------|--------|----------|----------|----------|--------|-----------:|-----------------:|
| **Go** — chi + sqlc + pgx                   | **6 853** | **13.4 ms** | **25.4 ms** | 104 ms   | **26 MB** | 14 ms      | 144 (+134 generated) |
| **Bun + Hono + `Bun.sql`** (native)         | **4 030**  | **24.0 ms** | **34.7 ms** | **68 ms** | 116 MB | 79 ms      | **55**           |
| Bun + Hono + `pg`                           | 2 826  | 33.5 ms  | 51.4 ms  | 125 ms   | 108 MB | 11 ms      | 57               |
| Bun + Hono + `postgres.js`                  | 2 463  | 38.7 ms  | 60.7 ms  | 179 ms   | 132 MB | 13 ms      | 53               |
| Bun + Hono + Drizzle (on `postgres.js`)     | 1 214  | 79.4 ms  | 115.9 ms | 209 ms   | 147 MB | 16 ms      | 94               |

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
| **Go** — net/http    | 5 000           | 86 057           | 0      | **137 MB** |
| Bun + Hono streamSSE | 4 997           | 86 948           | 0      | 191 MB |

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
| **Go** — pgx          | 957 ms  | **104 493/s**| **16 MB** |
| Bun + `Bun.sql`       | 1 287 ms| 77 700/s     | **74 MB** |
| Bun + `postgres.js`   | 1 307 ms| 76 511/s     | 102 MB |

Bun.sql ties postgres.js on throughput but uses **28% less memory** for the
long-running worker — the metric that matters most for a 24/7 process.

**Reading:** Go is 1.37× faster on throughput. The gap is smaller than the
audit workload because this is DB-bound — most of each batch is Postgres
round-trip time. RAM is 6× lower, which matters because metering is
long-lived: you pay for that memory all month, not just per-request.

## Lines of code (hand-written)

| Implementation              | LOC | Notes                                            |
|-----------------------------|----:|--------------------------------------------------|
| Go audit                    | 144 | Plus 134 generated by sqlc                       |
| TS audit-drizzle            | 94  | Includes 29-line schema.ts                       |
| TS audit-pg                 | 57  |                                                  |
| TS audit-bunsql             | 55  | Zero deps beyond Hono; native Bun                |
| TS audit-postgresjs         | 53  | Most concise; tagged-template SQL is clean       |
| Go stream                   | 105 |                                                  |
| TS stream                   | 64  |                                                  |
| Go meter                    | 162 |                                                  |
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

**What I'd actually do, post-bench (revised after Bun.sql results):**

1. Stay on Hono + Bun for `apps/api` and `apps/mcp`. Throughput is fine for
   alpha and the LOC + shared-types story is real.
2. **Drop Drizzle. Use `Bun.sql`.** This is now the highest-leverage change.
   Bun.sql is the fastest TS option, the tightest-tail TS option, has zero
   external deps, and is roughly tied with Drizzle on LOC despite needing
   no schema-as-TS layer. It closes the gap to Go from 5.6× to 1.7× on the
   audit hot path.
3. **Write the metering worker in Go** — *still*. Even with Bun.sql closing
   the throughput gap, Go uses 5× less RAM (16 MB vs 74 MB) on the
   long-running worker, and the worker has no Better-Auth surface to fight.
   Narrow contract, big economic win over a year of uptime. ~160 lines.
4. Defer rewriting the rest of the API in Go unless (a) audit write rate
   genuinely exceeds 4k rps in production, or (b) a Go hire wants to.

**The "drop Drizzle for Bun.sql" change is unambiguously good** — it's the
fastest, lowest-LOC, zero-dep TS path with the tightest p95/max. There is no
reason to keep Drizzle once Bun.sql exists for this workload shape.

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
(cd go/audit  && go build -o /tmp/bench-go-audit .)
(cd go/stream && go build -o /tmp/bench-go-stream .)
(cd go/meter  && go build -o /tmp/bench-go-meter .)
(cd bench     && go build -o /tmp/bench-sse-client sse-client.go)

# Install TS
for d in ts/audit-pg ts/audit-postgresjs ts/audit-drizzle ts/stream ts/meter; do
  (cd $d && bun install)
done

# Audit (each ~45s)
./bench/run-audit.sh go-audit            8081 /tmp/bench-go-audit
./bench/run-audit.sh ts-audit-pg         8092 bun run ts/audit-pg/src/index.ts
./bench/run-audit.sh ts-audit-postgresjs 8093 bun run ts/audit-postgresjs/src/index.ts
./bench/run-audit.sh ts-audit-drizzle    8091 bun run ts/audit-drizzle/src/index.ts
./bench/run-audit.sh ts-audit-bunsql     8095 bun run ts/audit-bunsql/src/index.ts

# Stream (each ~30s, holds 5000 conns)
./bench/run-stream.sh go-stream 8182 5000 20 /tmp/bench-go-stream
./bench/run-stream.sh ts-stream 8194 5000 20 bun run ts/stream/src/index.ts

# Meter (each ~5s)
./bench/run-meter.sh go-meter /tmp/bench-go-meter
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
