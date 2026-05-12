use axum::{
    extract::State,
    http::{HeaderMap, StatusCode},
    routing::{get, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sha2::{Digest, Sha256};
use sqlx::postgres::{PgPoolOptions, PgRow};
use sqlx::types::Json as PgJson;
use sqlx::{PgPool, Row};
use std::env;
use uuid::Uuid;

#[derive(Deserialize)]
struct WriteReq {
    kind: String,
    payload: Box<RawValue>,
}

#[derive(Serialize)]
struct WriteResp {
    id: i64,
    hash: String,
}

#[derive(Clone)]
struct AppState {
    pool: PgPool,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let dsn = env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://bench:bench@localhost:5544/bench".to_string());

    let pool = PgPoolOptions::new()
        .max_connections(50)
        .min_connections(10)
        .connect(&dsn)
        .await?;

    let app = Router::new()
        .route("/audit", post(create_audit))
        .route("/healthz", get(|| async { "ok" }))
        .with_state(AppState { pool });

    let addr = "0.0.0.0:9085";
    let listener = tokio::net::TcpListener::bind(addr).await?;
    eprintln!("rust audit-auth listening on {addr}");
    axum::serve(listener, app).await?;
    Ok(())
}

async fn create_audit(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(req): Json<WriteReq>,
) -> Result<Json<WriteResp>, (StatusCode, String)> {
    // 1. Bearer.
    let token = headers
        .get("authorization")
        .and_then(|h| h.to_str().ok())
        .and_then(|s| s.strip_prefix("Bearer "))
        .ok_or((StatusCode::UNAUTHORIZED, "missing bearer".into()))?;

    let mut tx = state
        .pool
        .begin()
        .await
        .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;

    // 2. Session.
    let sess_row: Option<PgRow> = sqlx::query(
        "SELECT user_id, organization_id FROM sessions WHERE token = $1 AND expires_at > now()",
    )
    .bind(token)
    .fetch_optional(&mut *tx)
    .await
    .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;
    let sess = sess_row.ok_or((StatusCode::UNAUTHORIZED, "invalid token".into()))?;
    let user_id: Uuid = sess.get("user_id");
    let org_id: Uuid = sess.get("organization_id");

    // 3. Membership + permission.
    let mem_row: Option<PgRow> = sqlx::query(
        "SELECT role, permissions FROM members WHERE user_id = $1 AND organization_id = $2",
    )
    .bind(user_id)
    .bind(org_id)
    .fetch_optional(&mut *tx)
    .await
    .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;
    let mem = mem_row.ok_or((StatusCode::FORBIDDEN, "no membership".into()))?;
    let perms: Vec<String> = mem.get("permissions");
    if !perms.iter().any(|p| p == "audit:write") {
        return Err((StatusCode::FORBIDDEN, "forbidden".into()));
    }

    // 4. Set GUCs.
    sqlx::query(
        "SELECT set_config('app.user_id', $1, true), set_config('app.organization_id', $2, true)",
    )
    .bind(user_id.to_string())
    .bind(org_id.to_string())
    .execute(&mut *tx)
    .await
    .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;

    // 5. Last hash (RLS active).
    let prev: Option<Vec<u8>> = sqlx::query(
        "SELECT hash FROM audit_entries WHERE organization_id = $1 ORDER BY id DESC LIMIT 1",
    )
    .bind(org_id)
    .fetch_optional(&mut *tx)
    .await
    .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?
    .map(|row: PgRow| row.get::<Vec<u8>, _>("hash"));
    let prev_hash = prev.unwrap_or_default();

    let mut hasher = Sha256::new();
    hasher.update(&prev_hash);
    hasher.update(req.kind.as_bytes());
    hasher.update(req.payload.get().as_bytes());
    let hash = hasher.finalize().to_vec();

    // 6. Insert (RLS active).
    let row = sqlx::query(
        r#"INSERT INTO audit_entries
              (organization_id, kind, actor_id, payload, prev_hash, hash)
           VALUES ($1, $2, $3, $4, $5, $6)
           RETURNING id"#,
    )
    .bind(org_id)
    .bind(&req.kind)
    .bind(user_id)
    .bind(PgJson(&req.payload))
    .bind(&prev_hash)
    .bind(&hash)
    .fetch_one(&mut *tx)
    .await
    .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;
    let id: i64 = row.get("id");

    tx.commit()
        .await
        .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;

    Ok(Json(WriteResp {
        id,
        hash: hex::encode(&hash),
    }))
}
