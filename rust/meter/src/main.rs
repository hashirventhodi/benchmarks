use chrono::{DateTime, DurationRound, TimeDelta, Utc};
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Row};
use std::collections::HashMap;
use std::env;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};
use uuid::Uuid;

const BATCH: i64 = 500;
const WORKERS: usize = 8;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let dsn = env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://bench:bench@localhost:5544/bench".to_string());

    let pool = PgPoolOptions::new()
        .max_connections(16)
        .connect(&dsn)
        .await?;

    let processed = Arc::new(AtomicU64::new(0));
    let start = Instant::now();

    for id in 0..WORKERS {
        let pool = pool.clone();
        let processed = Arc::clone(&processed);
        tokio::spawn(async move {
            worker(id, pool, processed).await;
        });
    }

    let report = tokio::spawn({
        let processed = Arc::clone(&processed);
        async move {
            let mut tick = tokio::time::interval(Duration::from_secs(1));
            tick.tick().await;
            loop {
                tick.tick().await;
                let p = processed.load(Ordering::Relaxed);
                let elapsed = start.elapsed().as_secs_f64();
                println!("processed={p} throughput={:.0}/s", p as f64 / elapsed);
            }
        }
    });

    report.await?;
    Ok(())
}

async fn worker(id: usize, pool: PgPool, processed: Arc<AtomicU64>) {
    loop {
        match drain_batch(&pool).await {
            Ok(n) => {
                if n == 0 {
                    tokio::time::sleep(Duration::from_millis(20)).await;
                } else {
                    processed.fetch_add(n as u64, Ordering::Relaxed);
                }
            }
            Err(e) => {
                eprintln!("worker {id}: {e}");
                tokio::time::sleep(Duration::from_millis(50)).await;
            }
        }
    }
}

async fn drain_batch(pool: &PgPool) -> Result<usize, sqlx::Error> {
    let mut tx = pool.begin().await?;

    let rows = sqlx::query(
        r#"SELECT id, organization_id, tokens_in, tokens_out
           FROM pending_events
           WHERE claimed_at IS NULL
           ORDER BY id
           LIMIT $1
           FOR UPDATE SKIP LOCKED"#,
    )
    .bind(BATCH)
    .fetch_all(&mut *tx)
    .await?;

    if rows.is_empty() {
        tx.commit().await?;
        return Ok(0);
    }

    let bucket: DateTime<Utc> = Utc::now()
        .duration_trunc(TimeDelta::minutes(1))
        .unwrap();

    let mut agg: HashMap<Uuid, (i64, i64, i64)> = HashMap::new();
    let mut ids: Vec<i64> = Vec::with_capacity(rows.len());
    for row in &rows {
        let id: i64 = row.get("id");
        let org: Uuid = row.get("organization_id");
        let ti: i32 = row.get("tokens_in");
        let to: i32 = row.get("tokens_out");
        ids.push(id);
        let entry = agg.entry(org).or_insert((0, 0, 0));
        entry.0 += ti as i64;
        entry.1 += to as i64;
        entry.2 += 1;
    }

    for (org, (ti, to, events)) in &agg {
        sqlx::query(
            r#"INSERT INTO meter_aggregates
                  (organization_id, bucket, tokens_in, tokens_out, events)
               VALUES ($1, $2, $3, $4, $5)
               ON CONFLICT (organization_id, bucket) DO UPDATE
                 SET tokens_in  = meter_aggregates.tokens_in  + EXCLUDED.tokens_in,
                     tokens_out = meter_aggregates.tokens_out + EXCLUDED.tokens_out,
                     events     = meter_aggregates.events     + EXCLUDED.events"#,
        )
        .bind(org)
        .bind(bucket)
        .bind(ti)
        .bind(to)
        .bind(events)
        .execute(&mut *tx)
        .await?;
    }

    sqlx::query(
        "UPDATE pending_events SET claimed_at = now(), processed_at = now() WHERE id = ANY($1)",
    )
    .bind(&ids)
    .execute(&mut *tx)
    .await?;

    tx.commit().await?;
    Ok(rows.len())
}
