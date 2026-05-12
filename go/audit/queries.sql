-- name: LastHash :one
SELECT hash FROM audit_entries
WHERE organization_id = $1
ORDER BY id DESC LIMIT 1;

-- name: InsertAudit :one
INSERT INTO audit_entries (organization_id, kind, actor_id, payload, prev_hash, hash)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at;
