import { Hono } from 'hono';
import { SQL } from 'bun';
import { createHash } from 'node:crypto';

const sql = new SQL({
  url:
    process.env.DATABASE_URL ?? 'postgres://bench:bench@localhost:5544/bench',
  max: 50,
});

const app = new Hono();

app.post('/audit', async (c) => {
  const body = await c.req.json<{
    kind: string;
    org_id: string;
    actor_id: string;
    payload: unknown;
  }>();

  try {
    const result = await sql.begin(async (tx) => {
      const prev = await tx`
        SELECT hash FROM audit_entries
        WHERE organization_id = ${body.org_id}
        ORDER BY id DESC LIMIT 1
      `;
      const prevHash: Uint8Array = prev[0]?.hash ?? new Uint8Array();

      const h = createHash('sha256');
      h.update(prevHash);
      h.update(body.kind);
      h.update(JSON.stringify(body.payload));
      const hash = h.digest();

      const ins = await tx`
        INSERT INTO audit_entries (organization_id, kind, actor_id, payload, prev_hash, hash)
        VALUES (${body.org_id}, ${body.kind}, ${body.actor_id},
                ${JSON.stringify(body.payload)}::jsonb, ${prevHash}, ${hash})
        RETURNING id, created_at
      `;
      return { id: String(ins[0].id), hash: hash.toString('hex') };
    });
    return c.json(result);
  } catch (e) {
    return c.json({ error: String(e) }, 500);
  }
});

app.get('/healthz', (c) => c.text('ok'));

export default {
  port: 8095,
  fetch: app.fetch,
};
