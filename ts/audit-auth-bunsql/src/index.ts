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
  // 1. Bearer.
  const authz = c.req.header('authorization') ?? '';
  if (!authz.startsWith('Bearer ')) return c.json({ error: 'missing bearer' }, 401);
  const token = authz.slice(7);

  const body = await c.req.json<{ kind: string; payload: unknown }>();

  try {
    const result = await sql.begin(async (tx) => {
      // 2. Session.
      const sess = await tx`
        SELECT user_id, organization_id FROM sessions
        WHERE token = ${token} AND expires_at > now()
      `;
      if (sess.length === 0) throw new HttpError(401, 'invalid token');
      const userId: string = sess[0].user_id;
      const orgId: string = sess[0].organization_id;

      // 3. Membership + permission.
      const mem = await tx`
        SELECT role, permissions FROM members
        WHERE user_id = ${userId} AND organization_id = ${orgId}
      `;
      if (mem.length === 0) throw new HttpError(403, 'no membership');
      const perms: string[] = mem[0].permissions;
      if (!perms.includes('audit:write')) throw new HttpError(403, 'forbidden');

      // 4. Set GUCs.
      await tx`
        SELECT set_config('app.user_id', ${userId}, true),
               set_config('app.organization_id', ${orgId}, true)
      `;

      // 5. Last hash (RLS active).
      const prev = await tx`
        SELECT hash FROM audit_entries
        WHERE organization_id = ${orgId}
        ORDER BY id DESC LIMIT 1
      `;
      const prevHash: Uint8Array = prev[0]?.hash ?? new Uint8Array();

      const h = createHash('sha256');
      h.update(prevHash);
      h.update(body.kind);
      h.update(JSON.stringify(body.payload));
      const hash = h.digest();

      // 6. Insert (RLS active).
      const ins = await tx`
        INSERT INTO audit_entries (organization_id, kind, actor_id, payload, prev_hash, hash)
        VALUES (${orgId}, ${body.kind}, ${userId},
                ${JSON.stringify(body.payload)}::jsonb, ${prevHash}, ${hash})
        RETURNING id
      `;
      return { id: String(ins[0].id), hash: hash.toString('hex') };
    });
    return c.json(result);
  } catch (e) {
    if (e instanceof HttpError) return c.json({ error: e.message }, e.status as never);
    return c.json({ error: String(e) }, 500);
  }
});

app.get('/healthz', (c) => c.text('ok'));

class HttpError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export default {
  port: 9095,
  fetch: app.fetch,
};
