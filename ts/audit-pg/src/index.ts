import { Hono } from 'hono';
import pg from 'pg';
import { createHash } from 'node:crypto';

const pool = new pg.Pool({
  connectionString:
    process.env.DATABASE_URL ?? 'postgres://bench:bench@localhost:5544/bench',
  max: 50,
  min: 10,
});

const app = new Hono();

app.post('/audit', async (c) => {
  const body = await c.req.json<{
    kind: string;
    org_id: string;
    actor_id: string;
    payload: unknown;
  }>();

  const client = await pool.connect();
  try {
    await client.query('BEGIN');
    const prev = await client.query<{ hash: Buffer }>(
      'SELECT hash FROM audit_entries WHERE organization_id = $1 ORDER BY id DESC LIMIT 1',
      [body.org_id],
    );
    const prevHash = prev.rows[0]?.hash ?? Buffer.alloc(0);

    const h = createHash('sha256');
    h.update(prevHash);
    h.update(body.kind);
    h.update(JSON.stringify(body.payload));
    const hash = h.digest();

    const ins = await client.query<{ id: string; created_at: Date }>(
      `INSERT INTO audit_entries (organization_id, kind, actor_id, payload, prev_hash, hash)
       VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
      [body.org_id, body.kind, body.actor_id, body.payload, prevHash, hash],
    );
    await client.query('COMMIT');
    return c.json({ id: ins.rows[0].id, hash: hash.toString('hex') });
  } catch (e) {
    await client.query('ROLLBACK');
    return c.json({ error: String(e) }, 500);
  } finally {
    client.release();
  }
});

app.get('/healthz', (c) => c.text('ok'));

export default {
  port: 8092,
  fetch: app.fetch,
};
