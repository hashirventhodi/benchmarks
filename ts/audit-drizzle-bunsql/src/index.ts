import { Hono } from 'hono';
import { SQL } from 'bun';
import { drizzle } from 'drizzle-orm/bun-sql';
import { desc, eq } from 'drizzle-orm';
import { createHash } from 'node:crypto';
import { auditEntries } from './schema.ts';

const client = new SQL({
  url:
    process.env.DATABASE_URL ?? 'postgres://bench:bench@localhost:5544/bench',
  max: 50,
});
const db = drizzle({ client, schema: { auditEntries } });

const app = new Hono();

app.post('/audit', async (c) => {
  const body = await c.req.json<{
    kind: string;
    org_id: string;
    actor_id: string;
    payload: unknown;
  }>();

  try {
    const result = await db.transaction(async (tx) => {
      const prev = await tx
        .select({ hash: auditEntries.hash })
        .from(auditEntries)
        .where(eq(auditEntries.organizationId, body.org_id))
        .orderBy(desc(auditEntries.id))
        .limit(1);
      const prevHash = prev[0]?.hash ?? new Uint8Array();

      const h = createHash('sha256');
      h.update(prevHash);
      h.update(body.kind);
      h.update(JSON.stringify(body.payload));
      const hash = h.digest();

      const ins = await tx
        .insert(auditEntries)
        .values({
          organizationId: body.org_id,
          kind: body.kind,
          actorId: body.actor_id,
          payload: body.payload as object,
          prevHash,
          hash,
        })
        .returning({ id: auditEntries.id, createdAt: auditEntries.createdAt });

      return { id: ins[0].id, hash: hash.toString('hex') };
    });
    return c.json(result);
  } catch (e) {
    return c.json({ error: String(e) }, 500);
  }
});

app.get('/healthz', (c) => c.text('ok'));

export default {
  port: 8096,
  fetch: app.fetch,
};
