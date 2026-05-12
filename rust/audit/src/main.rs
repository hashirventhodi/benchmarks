use axum::{
    extract::State,
    http::StatusCode,
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
    org_id: Uuid,
    actor_id: Uuid,
    // Box<RawValue> keeps the original JSON bytes (no parse + reserialize).
    // Matches Go's `json.RawMessage` semantics for a fair comparison.
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

    let addr = "0.0.0.0:8085";
    let listener = tokio::net::TcpListener::bind(addr).await?;
    eprintln!("rust audit listening on {addr}");
    axum::serve(listener, app).await?;
    Ok(())
}

async fn create_audit(
    State(state): State<AppState>,
    Json(req): Json<WriteReq>,
) -> Result<Json<WriteResp>, (StatusCode, String)> {
    let mut tx = state
        .pool
        .begin()
        .await
        .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?;

    let prev: Option<Vec<u8>> = sqlx::query(
        "SELECT hash FROM audit_entries WHERE organization_id = $1 ORDER BY id DESC LIMIT 1",
    )
    .bind(req.org_id)
    .fetch_optional(&mut *tx)
    .await
    .map_err(|e| (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()))?
    .map(|row: PgRow| row.get::<Vec<u8>, _>("hash"));
    let prev_hash = prev.unwrap_or_default();

    let mut hasher = Sha256::new();
    hasher.update(&prev_hash);
    hasher.update(req.kind.as_bytes());
    hasher.update(req.payload.get().as_bytes()); // raw JSON bytes, no reserialize
    let hash = hasher.finalize().to_vec();

    let row = sqlx::query(
        r#"INSERT INTO audit_entries
              (organization_id, kind, actor_id, payload, prev_hash, hash)
           VALUES ($1, $2, $3, $4, $5, $6)
           RETURNING id"#,
    )
    .bind(req.org_id)
    .bind(&req.kind)
    .bind(req.actor_id)
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

