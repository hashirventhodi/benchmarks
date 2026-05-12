-- Bench schema. Mirrors Steloit shapes loosely; tuned to make the
-- DB NOT the bottleneck so we measure the language stack.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE organizations (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL
);

CREATE TABLE audit_entries (
  id               bigserial PRIMARY KEY,
  organization_id  uuid NOT NULL REFERENCES organizations(id),
  kind             text NOT NULL,
  actor_id         uuid NOT NULL,
  payload          jsonb NOT NULL,
  prev_hash        bytea,
  hash             bytea NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_org_created_idx ON audit_entries (organization_id, created_at DESC);

-- Lightweight queue for the metering worker.
CREATE TABLE pending_events (
  id               bigserial PRIMARY KEY,
  organization_id  uuid NOT NULL REFERENCES organizations(id),
  agent_run_id     uuid NOT NULL,
  tokens_in        int NOT NULL,
  tokens_out       int NOT NULL,
  claimed_at       timestamptz,
  processed_at     timestamptz
);
CREATE INDEX pending_unclaimed_idx ON pending_events (id) WHERE claimed_at IS NULL;

CREATE TABLE meter_aggregates (
  organization_id  uuid NOT NULL,
  bucket           timestamptz NOT NULL,
  tokens_in        bigint NOT NULL DEFAULT 0,
  tokens_out       bigint NOT NULL DEFAULT 0,
  events           bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (organization_id, bucket)
);

-- Seed: one org we can hammer.
INSERT INTO organizations (id, name)
VALUES ('00000000-0000-0000-0000-000000000001', 'Bench Org');
