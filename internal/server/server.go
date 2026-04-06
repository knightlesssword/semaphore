package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/knightlesssword/semaphore/internal/proxy"
)

type Server struct {
	http   *http.Server
	cfg    *config.ServerConfig
	logger *slog.Logger
}

func New(cfg *config.Config, logger *slog.Logger) *Server {
	mux := http.NewServeMux()

	// Health check — no auth, no middleware
	mux.HandleFunc("GET /healthz", handleHealthz)

	// Proxy — OpenAI-compatible chat completions endpoint
	proxyHandler := proxy.NewHandler(cfg, logger)
	mux.Handle("POST /v1/chat/completions", proxyHandler)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      mux,
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
