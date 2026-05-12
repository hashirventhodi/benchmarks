import {
  bigserial,
  customType,
  jsonb,
  pgTable,
  text,
  timestamp,
  uuid,
} from 'drizzle-orm/pg-core';

const bytea = customType<{ data: Uint8Array; default: false }>({
  dataType: () => 'bytea',
});

export const organizations = pgTable('organizations', {
  id: uuid('id').primaryKey().defaultRandom(),
  name: text('name').notNull(),
});

export const auditEntries = pgTable('audit_entries', {
  id: bigserial('id', { mode: 'number' }).primaryKey(),
  organizationId: uuid('organization_id').notNull(),
  kind: text('kind').notNull(),
  actorId: uuid('actor_id').notNull(),
  payload: jsonb('payload').notNull(),
  prevHash: bytea('prev_hash'),
  hash: bytea('hash').notNull(),
  createdAt: timestamp('created_at', { withTimezone: true }).notNull().defaultNow(),
});
