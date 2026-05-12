package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const batchSize = 500

var processed atomic.Int64

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://bench:bench@localhost:5544/bench?sslmode=disable"
	}
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	// Workers
	for i := 0; i < 8; i++ {
		go worker(ctx, pool, i)
	}

	// Report tick
	start := time.Now()
	t := time.NewTicker(1 * time.Second)
	for range t.C {
		elapsed := time.Since(start).Seconds()
		p := processed.Load()
		fmt.Printf("processed=%d throughput=%.0f/s\n", p, float64(p)/elapsed)
	}
}

func worker(ctx context.Context, pool *pgxpool.Pool, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := drainBatch(ctx, pool)
		if err != nil {
			log.Printf("worker %d: %v", id, err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if n == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func drainBatch(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Claim a batch with SKIP LOCKED.
	rows, err := tx.Query(ctx, `
		SELECT id, organization_id, tokens_in, tokens_out
		FROM pending_events
		WHERE claimed_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, batchSize)
	if err != nil {
		return 0, err
	}

	type ev struct {
		id        int64
		orgID     [16]byte
		tokIn     int32
		tokOut    int32
	}
	var batch []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.id, &e.orgID, &e.tokIn, &e.tokOut); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, e)
	}
	rows.Close()
	if len(batch) == 0 {
		return 0, tx.Commit(ctx)
	}

	// Aggregate by (org, minute bucket).
	type key struct {
		org    [16]byte
		bucket time.Time
	}
	agg := map[key]struct {
		in, out int64
		events  int64
	}{}
	bucket := time.Now().UTC().Truncate(time.Minute)
	for _, e := range batch {
		k := key{org: e.orgID, bucket: bucket}
		v := agg[k]
		v.in += int64(e.tokIn)
		v.out += int64(e.tokOut)
		v.events++
		agg[k] = v
	}

	// Upsert aggregates.
	for k, v := range agg {
		_, err := tx.Exec(ctx, `
			INSERT INTO meter_aggregates (organization_id, bucket, tokens_in, tokens_out, events)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (organization_id, bucket)
			DO UPDATE SET tokens_in = meter_aggregates.tokens_in + EXCLUDED.tokens_in,
			              tokens_out = meter_aggregates.tokens_out + EXCLUDED.tokens_out,
			              events = meter_aggregates.events + EXCLUDED.events
		`, k.org, k.bucket, v.in, v.out, v.events)
		if err != nil {
			return 0, err
		}
	}

	// Mark events processed.
	ids := make([]int64, len(batch))
	for i, e := range batch {
		ids[i] = e.id
	}
	_, err = tx.Exec(ctx, `
		UPDATE pending_events
		SET claimed_at = now(), processed_at = now()
		WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	processed.Add(int64(len(batch)))
	return len(batch), nil
}
