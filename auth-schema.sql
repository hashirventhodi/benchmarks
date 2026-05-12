-- Additional schema for the auth+RLS bench. Simulates the real Steloit
-- hot path: session lookup → membership/permission check → GUC set → RLS-protected insert.

CREATE TABLE sessions (
  token            text PRIMARY KEY,
  user_id          uuid NOT NULL,
  organization_id  uuid NOT NULL,
  expires_at       timestamptz NOT NULL
);

CREATE TABLE members (
  user_id          uuid NOT NULL,
  organization_id  uuid NOT NULL,
  role             text NOT NULL,
  permissions      text[] NOT NULL DEFAULT '{}',
  PRIMARY KEY (user_id, organization_id)
);

-- RLS on audit_entries: only see/insert rows for the current tenant.
ALTER TABLE audit_entries ENABLE ROW LEVEL SECURITY;

CREATE POLICY audit_org_isolation ON audit_entries
  FOR ALL
  USING (organization_id::text = current_setting('app.organization_id', true))
  WITH CHECK (organization_id::text = current_setting('app.organization_id', true));

-- Bypass for the bench user (non-restricted) so we can also seed and inspect.
ALTER TABLE audit_entries FORCE ROW LEVEL SECURITY;

-- Seed 10 tokens rotating over the same org+user. k6 will pick one per request.
INSERT INTO sessions (token, user_id, organization_id, expires_at)
SELECT
  'bench-token-' || i,
  '00000000-0000-0000-0000-000000000002'::uuid,
  '00000000-0000-0000-0000-000000000001'::uuid,
  now() + interval '1 day'
FROM generate_series(1, 10) AS s(i);

INSERT INTO members (user_id, organization_id, role, permissions)
VALUES (
  '00000000-0000-0000-0000-000000000002'::uuid,
  '00000000-0000-0000-0000-000000000001'::uuid,
  'admin',
  ARRAY['audit:write', 'audit:read']
);
