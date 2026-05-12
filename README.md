# bench-go-vs-ts

Steloit-flavored benchmark comparing Go (chi+sqlc+pgx) against TS (Bun+Hono with
three driver variants: pg, postgres.js, Drizzle).

See `RESULTS.md` for the full write-up. Three workloads, four metrics:

1. **Audit write** — POST endpoint with tx + hash-chain insert.
2. **SSE fan-out** — server holds 5000 concurrent SSE clients.
3. **Metering worker** — drains 100k pending events through `SKIP LOCKED`.

Metrics: throughput (RPS / events-per-sec), p50/p95 latency, RSS memory,
cold-start, lines of code.

## Layout

```
docker-compose.yml   Postgres 18 on port 5544, tmpfs, durability off
schema.sql           audit_entries + pending_events + meter_aggregates
go/                  chi + sqlc + pgx services
ts/                  Bun + Hono services, three audit variants
bench/               k6 + custom SSE client + shell runners
results/             per-run k6 JSON, RSS samples, server logs, summaries
```

## Repro

See "Repro" section in `RESULTS.md`.
