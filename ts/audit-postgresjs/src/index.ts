import { Hono } from 'hono';
import postgres from 'postgres';
import { createHash } from 'node:crypto';

const sql = postgres(
  process.env.DATABASE_URL ?? 'postgres://bench:bench@localhost:5544/bench',
  { max: 50 },
);

const app = new Hono();

app.post('/audit', async (c) => {
  const body = await c.req.json<{
    kind: string;
    org_id: string;
    actor_id: string;
    payload: unknown;
  }>();

  try {
    const row = await sql.begin(async (tx) => {
      const prev = await tx<{ hash: Uint8Array }[]>`
        SELECT hash FROM audit_entries
        WHERE organization_id = ${body.org_id}
        ORDER BY id DESC LIMIT 1
      `;
      const prevHash = prev[0]?.hash ?? new Uint8Array();

      const h = createHash('sha256');
      h.update(prevHash);
      h.update(body.kind);
      h.update(JSON.stringify(body.payload));
      const hash = h.digest();

      const ins = await tx<{ id: string; created_at: Date }[]>`
        INSERT INTO audit_entries (organization_id, kind, actor_id, payload, prev_hash, hash)
        VALUES (${body.org_id}, ${body.kind}, ${body.actor_id}, ${sql.json(body.payload as object)}, ${prevHash}, ${hash})
        RETURNING id, created_at
      `;
      return { id: ins[0].id, hash: hash.toString('hex') };
    });
    return c.json(row);
  } catch (e) {
    return c.json({ error: String(e) }, 500);
  }
});

app.get('/healthz', (c) => c.text('ok'));

export default {
  port: 8093,
  fetch: app.fetch,
};
