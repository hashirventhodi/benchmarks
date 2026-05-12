package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const batchSize = 500

var processed atomic.Int64

type PendingEvent struct {
	ID             int64      `gorm:"primaryKey"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;column:organization_id"`
	AgentRunID     uuid.UUID  `gorm:"type:uuid;column:agent_run_id"`
	TokensIn       int        `gorm:"column:tokens_in"`
	TokensOut      int        `gorm:"column:tokens_out"`
	ClaimedAt      *time.Time `gorm:"column:claimed_at"`
	ProcessedAt    *time.Time `gorm:"column:processed_at"`
}

func (PendingEvent) TableName() string { return "pending_events" }

type MeterAggregate struct {
	OrganizationID uuid.UUID `gorm:"primaryKey;type:uuid;column:organization_id"`
	Bucket         time.Time `gorm:"primaryKey;column:bucket"`
	TokensIn       int64     `gorm:"column:tokens_in"`
	TokensOut      int64     `gorm:"column:tokens_out"`
	Events         int64     `gorm:"column:events"`
}

func (MeterAggregate) TableName() string { return "meter_aggregates" }

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://bench:bench@localhost:5544/bench?sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:      logger.Default.LogMode(logger.Silent),
		PrepareStmt: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(16)

	for i := 0; i < 8; i++ {
		go worker(ctx, db, i)
	}

	start := time.Now()
	t := time.NewTicker(1 * time.Second)
	for range t.C {
		elapsed := time.Since(start).Seconds()
		p := processed.Load()
		fmt.Printf("processed=%d throughput=%.0f/s\n", p, float64(p)/elapsed)
	}
}

func worker(ctx context.Context, db *gorm.DB, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := drainBatch(ctx, db)
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

func drainBatch(ctx context.Context, db *gorm.DB) (int, error) {
	var n int
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch []PendingEvent
		if err := tx.Clauses(clause.Locking{
			Strength: "UPDATE",
			Options:  "SKIP LOCKED",
		}).Where("claimed_at IS NULL").
			Order("id").
			Limit(batchSize).
			Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}

		// Aggregate by (org, minute bucket).
		bucket := time.Now().UTC().Truncate(time.Minute)
		agg := map[uuid.UUID]MeterAggregate{}
		for _, e := range batch {
			v := agg[e.OrganizationID]
			v.OrganizationID = e.OrganizationID
			v.Bucket = bucket
			v.TokensIn += int64(e.TokensIn)
			v.TokensOut += int64(e.TokensOut)
			v.Events++
			agg[e.OrganizationID] = v
		}
		rows := make([]MeterAggregate, 0, len(agg))
		for _, v := range agg {
			rows = append(rows, v)
		}

		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "organization_id"}, {Name: "bucket"}},
			DoUpdates: clause.Assignments(map[string]any{
				"tokens_in":  gorm.Expr("meter_aggregates.tokens_in + EXCLUDED.tokens_in"),
				"tokens_out": gorm.Expr("meter_aggregates.tokens_out + EXCLUDED.tokens_out"),
				"events":     gorm.Expr("meter_aggregates.events + EXCLUDED.events"),
			}),
		}).Create(&rows).Error; err != nil {
			return err
		}

		ids := make([]int64, len(batch))
		for i, e := range batch {
			ids[i] = e.ID
		}
		if err := tx.Exec(`
			UPDATE pending_events
			SET claimed_at = now(), processed_at = now()
			WHERE id IN ?
		`, ids).Error; err != nil {
			return err
		}
		n = len(batch)
		return nil
	})
	if err != nil {
		return 0, err
	}
	processed.Add(int64(n))
	return n, nil
}
