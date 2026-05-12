package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"

	"bench/audit/dbq"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type writeReq struct {
	Kind    string          `json:"kind"`
	ActorID string          `json:"actor_id"`
	OrgID   string          `json:"org_id"`
	Payload json.RawMessage `json:"payload"`
}

type writeResp struct {
	ID   int64  `json:"id"`
	Hash string `json:"hash"`
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	ctx := context.Background()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://bench:bench@localhost:5544/bench?sslmode=disable"
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatal(err)
	}
	cfg.MaxConns = 50
	cfg.MinConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := dbq.New(pool)

	r := chi.NewRouter()
	r.Post("/audit", func(w http.ResponseWriter, r *http.Request) {
		var req writeReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		orgID, err := uuid.Parse(req.OrgID)
		if err != nil {
			http.Error(w, "bad org", 400)
			return
		}
		actorID, err := uuid.Parse(req.ActorID)
		if err != nil {
			http.Error(w, "bad actor", 400)
			return
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer tx.Rollback(r.Context())
		qtx := q.WithTx(tx)

		orgPG := pgtype.UUID{Bytes: orgID, Valid: true}

		prev, err := qtx.LastHash(r.Context(), orgPG)
		if err != nil && err.Error() != "no rows in result set" {
			http.Error(w, err.Error(), 500)
			return
		}

		h := sha256.New()
		h.Write(prev)
		h.Write([]byte(req.Kind))
		h.Write(req.Payload)
		hash := h.Sum(nil)

		row, err := qtx.InsertAudit(r.Context(), dbq.InsertAuditParams{
			OrganizationID: orgPG,
			Kind:           req.Kind,
			ActorID:        pgtype.UUID{Bytes: actorID, Valid: true},
			Payload:        req.Payload,
			PrevHash:       prev,
			Hash:           hash,
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(writeResp{ID: row.ID, Hash: string(hash)})
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	addr := ":8081"
	log.Println("go audit listening on", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}
