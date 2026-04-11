package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/knightlesssword/semaphore/internal/server"
	"github.com/knightlesssword/semaphore/internal/store"
)

func main() {
	cfgFile := flag.String("config", "", "path to config file (default: config.yaml in working dir)")
	flag.Parse()

	cfg, err := config.Load(*cfgFile)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	logger := buildLogger(cfg)
	logger.Info("semaphore starting", "version", "dev")

	var deps server.Deps

	// Connect to Redis only when rate limiting is enabled.
	if cfg.RateLimit.Enabled {
		rdb, err := store.NewRedisClient(&cfg.Redis)
		if err != nil {
			logger.Error("failed to connect to Redis", "addr", cfg.Redis.Addr, "err", err)
			os.Exit(1)
		}
		logger.Info("redis connected", "addr", cfg.Redis.Addr)
		defer rdb.Close()
		deps.Redis = rdb
	}

	// Connect to Postgres when explicitly enabled.
	if cfg.Postgres.Enabled {
		pg, err := store.NewPostgresStore(&cfg.Postgres, logger)
		if err != nil {
			logger.Error("failed to connect to Postgres", "err", err)
			os.Exit(1)
		}
		logger.Info("postgres connected")
		defer pg.Close()
		deps.Postgres = pg
	}

	// Root context cancelled on SIGINT / SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, deps, logger)
	if err := srv.Start(ctx); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func buildLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.Log.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if cfg.Log.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}
