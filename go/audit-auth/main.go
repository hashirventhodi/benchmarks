package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type writeReq struct {
	Kind    string          `json:"kind"`
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

	r := chi.NewRouter()
	r.Post("/audit", func(w http.ResponseWriter, r *http.Request) {
		// 1. Extract bearer token.
		authz := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(authz, "Bearer ")
		if !ok {
			http.Error(w, "missing bearer", 401)
			return
		}

		var req writeReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		tx, err := pool.Begin(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer tx.Rollback(r.Context())

		// 2. Session lookup.
		var userID, orgID uuid.UUID
		err = tx.QueryRow(r.Context(),
			`SELECT user_id, organization_id FROM sessions
			 WHERE token = $1 AND expires_at > now()`, token,
		).Scan(&userID, &orgID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				http.Error(w, "invalid token", 401)
				return
			}
			http.Error(w, err.Error(), 500)
			return
		}

		// 3. Membership + permission check.
		var role string
		var perms []string
		err = tx.QueryRow(r.Context(),
			`SELECT role, permissions FROM members
			 WHERE user_id = $1 AND organization_id = $2`, userID, orgID,
		).Scan(&role, &perms)
		if err != nil {
			http.Error(w, "no membership", 403)
			return
		}
		if !slicesContains(perms, "audit:write") {
			http.Error(w, "forbidden", 403)
			return
		}

		// 4. Set GUCs for RLS.
		_, err = tx.Exec(r.Context(),
			`SELECT set_config('app.user_id', $1, true),
			        set_config('app.organization_id', $2, true)`,
			userID.String(), orgID.String())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		// 5. Last-hash lookup (RLS applies).
		var prev []byte
		err = tx.QueryRow(r.Context(),
			`SELECT hash FROM audit_entries
			 WHERE organization_id = $1 ORDER BY id DESC LIMIT 1`, orgID,
		).Scan(&prev)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, err.Error(), 500)
			return
		}

		h := sha256.New()
		h.Write(prev)
		h.Write([]byte(req.Kind))
		h.Write(req.Payload)
		hash := h.Sum(nil)

		// 6. Insert (RLS applies).
		var id int64
		err = tx.QueryRow(r.Context(),
			`INSERT INTO audit_entries
			    (organization_id, kind, actor_id, payload, prev_hash, hash)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING id`,
			orgID, req.Kind, userID, req.Payload, prev, hash,
		).Scan(&id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(writeResp{ID: id, Hash: string(hash)})
	})
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	addr := ":9081"
	log.Println("go audit-auth listening on", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
