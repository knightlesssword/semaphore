package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/knightlesssword/semaphore/internal/middleware"
	"github.com/knightlesssword/semaphore/internal/store"
)

// Server is the admin HTTP server bound to the admin port.
// All routes require the static admin bearer token.
type Server struct {
	http   *http.Server
	logger *slog.Logger
}

// New wires the admin mux and returns a Server ready to Start.
func New(cfg *config.AdminConfig, pg *store.PostgresStore, logger *slog.Logger) *Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.Handle("POST /keys", authMiddleware(cfg.Token, logger)(http.HandlerFunc(handleCreateKey(pg, logger))))
	mux.Handle("GET /keys", authMiddleware(cfg.Token, logger)(http.HandlerFunc(handleListKeys(pg, logger))))
	mux.Handle("DELETE /keys/{id}", authMiddleware(cfg.Token, logger)(http.HandlerFunc(handleRevokeKey(pg, logger))))

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return &Server{http: httpServer, logger: logger}
}

// Start listens and blocks until ctx is cancelled, then shuts down gracefully.
func (s *Server) Start(ctx context.Context) error {
	listenErr := make(chan error, 1)
	go func() {
		s.logger.Info("admin server listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			listenErr <- err
		}
	}()

	select {
	case err := <-listenErr:
		return fmt.Errorf("admin server error: %w", err)
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.http.Shutdown(shutCtx)
}

// ── Handlers ───────────────────────────────────────────────────────────────

type createKeyRequest struct {
	Name  string `json:"name"`
	Tier  string `json:"tier"`
	Owner string `json:"owner"`
}

type createKeyResponse struct {
	ID    string `json:"id"`
	Key   string `json:"key"`  // shown once — never stored
	Name  string `json:"name"`
	Tier  string `json:"tier"`
	Owner string `json:"owner"`
}

func handleCreateKey(pg *store.PostgresStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			jsonErr(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Tier == "" {
			req.Tier = "default"
		}

		rawKey := generateKey()
		id, err := pg.CreateKey(r.Context(), rawKey, req.Name, req.Tier, req.Owner)
		if err != nil {
			logger.Error("admin: create key failed", "err", err)
			jsonErr(w, "failed to create key", http.StatusInternalServerError)
			return
		}

		logger.Info("admin: key created", "id", id, "name", req.Name, "tier", req.Tier)
		jsonOK(w, createKeyResponse{ID: id, Key: rawKey, Name: req.Name, Tier: req.Tier, Owner: req.Owner})
	}
}

func handleListKeys(pg *store.PostgresStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keys, err := pg.ListKeys(r.Context())
		if err != nil {
			logger.Error("admin: list keys failed", "err", err)
			jsonErr(w, "failed to list keys", http.StatusInternalServerError)
			return
		}

		type keyItem struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Tier      string  `json:"tier"`
			Owner     string  `json:"owner"`
			CreatedAt string  `json:"created_at"`
			RevokedAt *string `json:"revoked_at,omitempty"`
		}

		items := make([]keyItem, len(keys))
		for i, k := range keys {
			item := keyItem{
				ID:        k.ID,
				Name:      k.Name,
				Tier:      k.Tier,
				Owner:     k.Owner,
				CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
			}
			if k.RevokedAt != nil {
				s := k.RevokedAt.UTC().Format(time.RFC3339)
				item.RevokedAt = &s
			}
			items[i] = item
		}

		jsonOK(w, map[string]any{"keys": items, "count": len(items)})
	}
}

func handleRevokeKey(pg *store.PostgresStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			jsonErr(w, "id is required", http.StatusBadRequest)
			return
		}

		ok, err := pg.RevokeKey(r.Context(), id)
		if err != nil {
			logger.Error("admin: revoke key failed", "id", id, "err", err)
			jsonErr(w, "failed to revoke key", http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonErr(w, "key not found or already revoked", http.StatusNotFound)
			return
		}

		logger.Info("admin: key revoked", "id", id)
		jsonOK(w, map[string]string{"id": id, "status": "revoked"})
	}
}

// ── Auth middleware ────────────────────────────────────────────────────────

func authMiddleware(token string, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			parts := strings.SplitN(h, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || strings.TrimSpace(parts[1]) != token {
				logger.Warn("admin: unauthorized request", "path", r.URL.Path, "remote", r.RemoteAddr)
				jsonErr(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

func generateKey() string {
	b := make([]byte, 32)
	rand.Read(b) //nolint:errcheck
	return "sk-" + hex.EncodeToString(b)
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	middleware.WriteError(w, msg, code, middleware.SourceSemaphore)
}
