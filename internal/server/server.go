package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/knightlesssword/semaphore/internal/middleware"
	"github.com/knightlesssword/semaphore/internal/proxy"
	"github.com/knightlesssword/semaphore/internal/store"
	"github.com/redis/go-redis/v9"
)

type Server struct {
	http   *http.Server
	cfg    *config.ServerConfig
	logger *slog.Logger
}

// Deps bundles optional infrastructure clients.
// Fields are nil when the corresponding subsystem is disabled.
type Deps struct {
	Redis    *redis.Client
	Postgres *store.PostgresStore
}

// New wires the mux, middleware chain, and proxy handler.
func New(cfg *config.Config, deps Deps, logger *slog.Logger) *Server {
	mux := http.NewServeMux()

	// Health check — unauthenticated, but still panic-safe
	mux.HandleFunc("GET /healthz", handleHealthz)

	// Key store: Postgres if available, static fallback.
	var keyStore middleware.KeyStore
	if deps.Postgres != nil {
		keyStore = store.NewPostgresKeyStore(deps.Postgres)
	} else {
		keyStore = middleware.NewStaticKeyStore(cfg.Auth.StaticKeys)
	}

	chain := []middleware.Middleware{
		middleware.RequestID(),
		middleware.Auth(keyStore, cfg.Auth.Bypass, logger),
	}

	if cfg.RateLimit.Enabled && deps.Redis != nil {
		rl := middleware.NewRateLimiter(deps.Redis, &cfg.RateLimit, logger)
		chain = append(chain, middleware.RateLimit(rl))
	}

	if deps.Postgres != nil {
		al := middleware.NewAuditLogger(deps.Postgres, logger)
		al.Start(context.Background())
		chain = append(chain, middleware.Audit(al, cfg.Proxy.DefaultProvider))
	}

	protected := middleware.Chain(chain...)
	proxyHandler := proxy.NewHandler(cfg, logger)
	mux.Handle("POST /v1/chat/completions", protected(proxyHandler))

	// Recover wraps the entire mux — catches panics in any route
	handler := middleware.Recover(logger)(mux)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return &Server{
		http:   httpServer,
		cfg:    &cfg.Server,
		logger: logger,
	}
}

// Start begins listening. It blocks until the context is cancelled,
// then performs a graceful shutdown.
func (s *Server) Start(ctx context.Context) error {
	listenErr := make(chan error, 1)

	go func() {
		s.logger.Info("server listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			listenErr <- err
		}
	}()

	select {
	case err := <-listenErr:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		s.logger.Info("shutting down server")
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(s.cfg.ShutdownTimeout)*time.Second,
	)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	s.logger.Info("server stopped")
	return nil
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
