package main

import (
	"crypto/sha256"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type AuditEntry struct {
	ID             int64           `gorm:"primaryKey;autoIncrement"`
	OrganizationID uuid.UUID       `gorm:"type:uuid;not null;column:organization_id"`
	Kind           string          `gorm:"not null"`
	ActorID        uuid.UUID       `gorm:"type:uuid;not null;column:actor_id"`
	Payload        json.RawMessage `gorm:"type:jsonb;not null"`
	PrevHash       []byte          `gorm:"type:bytea;column:prev_hash"`
	Hash           []byte          `gorm:"type:bytea;not null"`
	CreatedAt      time.Time       `gorm:"not null;default:now();column:created_at"`
}

func (AuditEntry) TableName() string { return "audit_entries" }

type writeReq struct {
	Kind    string          `json:"kind"`
	ActorID string          `json:"actor_id"`
	OrgID   string          `json:"org_id"`
	Payload json.RawMessage `json:"payload"`
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://bench:bench@localhost:5544/bench?sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: false,
		PrepareStmt:            true,
	})
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	r.POST("/audit", func(c *gin.Context) {
		var req writeReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		orgID, err := uuid.Parse(req.OrgID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad org"})
			return
		}
		actorID, err := uuid.Parse(req.ActorID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad actor"})
			return
		}

		var entry AuditEntry
		err = db.Transaction(func(tx *gorm.DB) error {
			var last AuditEntry
			res := tx.Where("organization_id = ?", orgID).
				Order("id DESC").
				Limit(1).
				Select("hash").
				Find(&last)
			if res.Error != nil {
				return res.Error
			}
			prevHash := last.Hash

			h := sha256.New()
			h.Write(prevHash)
			h.Write([]byte(req.Kind))
			h.Write(req.Payload)
			hash := h.Sum(nil)

			entry = AuditEntry{
				OrganizationID: orgID,
				Kind:           req.Kind,
				ActorID:        actorID,
				Payload:        req.Payload,
				PrevHash:       prevHash,
				Hash:           hash,
			}
			return tx.Create(&entry).Error
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"id":   entry.ID,
			"hash": string(entry.Hash),
		})
	})

	r.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	addr := ":8084"
	log.Println("go audit-gorm listening on", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}
