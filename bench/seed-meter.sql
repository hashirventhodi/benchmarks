-- Pre-seed the pending_events table for the meter bench.
-- Run via: docker exec -i bench-pg psql -U bench -d bench < seed-meter.sql
TRUNCATE pending_events RESTART IDENTITY;
TRUNCATE meter_aggregates;

INSERT INTO pending_events (organization_id, agent_run_id, tokens_in, tokens_out)
SELECT
  '00000000-0000-0000-0000-000000000001'::uuid,
  gen_random_uuid(),
  (random() * 5000)::int,
  (random() * 5000)::int
FROM generate_series(1, 100000);

ANALYZE pending_events;
SELECT count(*) AS seeded FROM pending_events;
