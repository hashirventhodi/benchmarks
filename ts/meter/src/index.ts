import postgres from 'postgres';

const BATCH = 500;
const WORKERS = 8;

const sql = postgres(
  process.env.DATABASE_URL ?? 'postgres://bench:bench@localhost:5544/bench',
  { max: 16 },
);

let processed = 0;
const start = Date.now();

async function drainBatch(): Promise<number> {
  return await sql.begin(async (tx) => {
    const rows = await tx<
      { id: string; organization_id: string; tokens_in: number; tokens_out: number }[]
    >`
      SELECT id, organization_id, tokens_in, tokens_out
      FROM pending_events
      WHERE claimed_at IS NULL
      ORDER BY id
      LIMIT ${BATCH}
      FOR UPDATE SKIP LOCKED
    `;
    if (rows.length === 0) return 0;

    const bucket = new Date(Math.floor(Date.now() / 60000) * 60000);
    const agg = new Map<string, { in: bigint; out: bigint; events: bigint }>();
    for (const r of rows) {
      const cur = agg.get(r.organization_id) ?? { in: 0n, out: 0n, events: 0n };
      cur.in += BigInt(r.tokens_in);
      cur.out += BigInt(r.tokens_out);
      cur.events += 1n;
      agg.set(r.organization_id, cur);
    }

    for (const [org, v] of agg) {
      await tx`
        INSERT INTO meter_aggregates (organization_id, bucket, tokens_in, tokens_out, events)
        VALUES (${org}, ${bucket}, ${v.in}, ${v.out}, ${v.events})
        ON CONFLICT (organization_id, bucket)
        DO UPDATE SET tokens_in = meter_aggregates.tokens_in + EXCLUDED.tokens_in,
                      tokens_out = meter_aggregates.tokens_out + EXCLUDED.tokens_out,
                      events = meter_aggregates.events + EXCLUDED.events
      `;
    }

    const ids = rows.map((r) => Number(r.id));
    await tx`
      UPDATE pending_events
      SET claimed_at = now(), processed_at = now()
      WHERE id = ANY(${ids})
    `;
    return rows.length;
  });
}

async function worker(id: number) {
  while (true) {
    try {
      const n = await drainBatch();
      if (n === 0) await Bun.sleep(20);
      else processed += n;
    } catch (e) {
      console.error(`worker ${id}:`, e);
      await Bun.sleep(50);
    }
  }
}

for (let i = 0; i < WORKERS; i++) worker(i);

setInterval(() => {
  const elapsed = (Date.now() - start) / 1000;
  console.log(`processed=${processed} throughput=${(processed / elapsed).toFixed(0)}/s`);
}, 1000);
