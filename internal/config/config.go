package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Auth      AuthConfig      `mapstructure:"auth"`
	Proxy     ProxyConfig     `mapstructure:"proxy"`
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Postgres  PostgresConfig  `mapstructure:"postgres"`
	Log       LogConfig       `mapstructure:"log"`
}

// AuthConfig controls the auth middleware.
// In Phase 3 keys are listed here. Phase 5 moves validation to Postgres.
type AuthConfig struct {
	Bypass     bool     `mapstructure:"bypass"`      // true = skip auth (dev only)
	StaticKeys []string `mapstructure:"static_keys"` // valid Bearer tokens
}

type ProxyConfig struct {
	DefaultProvider string                    `mapstructure:"default_provider"`
	TimeoutSeconds  int                       `mapstructure:"timeout_seconds"`
	Providers       map[string]ProviderConfig `mapstructure:"providers"`
}

type ProviderConfig struct {
	BaseURL      string `mapstructure:"base_url"`
	APIKey       string `mapstructure:"api_key"`
	DefaultModel string `mapstructure:"default_model"`
}

type ServerConfig struct {
	Host            string `mapstructure:"host"`
	Port            int    `mapstructure:"port"`
	ShutdownTimeout int    `mapstructure:"shutdown_timeout_seconds"`
}

// RateLimitConfig controls the sliding-window rate limiter (Phase 4).
// Limits are enforced per authenticated API key.
type RateLimitConfig struct {
	Enabled           bool             `mapstructure:"enabled"`
	RequestsPerMinute int              `mapstructure:"requests_per_minute"` // default tier
	TokensPerDay      int              `mapstructure:"tokens_per_day"`      // 0 = unlimited
	Tiers             map[string]Tier  `mapstructure:"tiers"`               // named tier overrides
}

// Tier holds per-tier rate limit values.
type Tier struct {
	RequestsPerMinute int `mapstructure:"requests_per_minute"`
	TokensPerDay      int `mapstructure:"tokens_per_day"` // 0 = unlimited
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type PostgresConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	DSN     string `mapstructure:"dsn"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`  // debug, info, warn, error
	Format string `mapstructure:"format"` // text, json
}

// Load reads config.yaml (searched in working dir and /etc/semaphore)
// and allows any key to be overridden via env vars prefixed with SEMAPHORE_.
// Example: SEMAPHORE_SERVER_PORT=9090 overrides server.port.
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.shutdown_timeout_seconds", 15)
	v.SetDefault("rate_limit.enabled", false)
	v.SetDefault("rate_limit.requests_per_minute", 60)
	v.SetDefault("rate_limit.tokens_per_day", 0)
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.db", 0)
	v.SetDefault("postgres.enabled", false)
	v.SetDefault("postgres.dsn", "postgres://semaphore:semaphore@localhost:5432/semaphore?sslmode=disable")
	v.SetDefault("auth.bypass", false)
	v.SetDefault("proxy.default_provider", "openai")
	v.SetDefault("proxy.timeout_seconds", 120)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")

	// Config file
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./internal/config")
		v.AddConfigPath("/etc/semaphore")
	}

	if err := v.ReadInConfig(); err != nil {
		// Missing config file is fine — defaults + env vars are enough
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	// Env overrides: SEMAPHORE_SERVER_PORT → server.port
	v.SetEnvPrefix("SEMAPHORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}
