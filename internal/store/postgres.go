package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/knightlesssword/semaphore/internal/config"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore wraps a pgxpool.Pool and exposes the query helpers used by
// the gateway (audit logging, spend tracking, key validation).
type PostgresStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPostgresStore creates a connection pool, verifies connectivity, and runs
// any pending migrations. Returns an error if the database is unreachable or
// migrations fail.
func NewPostgresStore(cfg *config.PostgresConfig, logger *slog.Logger) (*PostgresStore, error) {
	pool, err := pgxpool.New(context.Background(), cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	s := &PostgresStore{pool: pool, logger: logger}
	if err := s.runMigrations(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return s, nil
}

// Close releases the connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// ── Migration runner ───────────────────────────────────────────────────────

// runMigrations executes any SQL files in migrations/ that haven't been applied
// yet, in lexicographic order.  Each file is wrapped in a transaction so a
// partial failure leaves the DB in a clean state.
func (s *PostgresStore) runMigrations(ctx context.Context) error {
	// Ensure the tracking table exists (bootstrap case).
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT        PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`)
	if err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	// Collect SQL files in sorted order.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("reading migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version := entry.Name()

		// Skip already-applied migrations.
		var exists bool
		err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("checking migration %s: %w", version, err)
		}
		if exists {
			continue
		}

		sql, err := migrationsFS.ReadFile("migrations/" + version)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", version, err)
		}

		// Run in a transaction for atomicity.
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("executing migration %s: %w", version, err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("recording migration %s: %w", version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing migration %s: %w", version, err)
		}

		s.logger.Info("migration applied", "version", version)
	}

	return nil
}

// ── Audit / spend ─────────────────────────────────────────────────────────

// AuditRecord holds the data captured for a single proxied request.
type AuditRecord struct {
	ID               string    // UUID — set by the caller or left "" for DB default
	APIKeyID         string    // UUID from api_keys, or "" for bypass/static-key requests
	RequestID        string    // X-Request-ID header value
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	LatencyMs        int64
	Cached           bool
	Status           int
	CreatedAt        time.Time
}

// InsertRequest writes one audit record to the requests table.
// api_key_id is written as NULL when APIKeyID is empty.
func (s *PostgresStore) InsertRequest(ctx context.Context, rec AuditRecord) error {
	var keyID interface{}
	if rec.APIKeyID != "" {
		keyID = rec.APIKeyID
	}

	createdAt := rec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO requests
			(api_key_id, request_id, provider, model,
			 prompt_tokens, completion_tokens, latency_ms, cached, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		keyID, rec.RequestID, rec.Provider, rec.Model,
		rec.PromptTokens, rec.CompletionTokens, rec.LatencyMs,
		rec.Cached, rec.Status, createdAt,
	)
	return err
}

// UpsertSpend increments the daily spend counters for a key.
// api_key_id must be a valid UUID string (non-empty); if empty, the call is a no-op.
func (s *PostgresStore) UpsertSpend(ctx context.Context, apiKeyID string, day time.Time, promptTokens, completionTokens int) error {
	if apiKeyID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO spend (api_key_id, day, prompt_tokens, completion_tokens)
		VALUES ($1, $2::date, $3, $4)
		ON CONFLICT (api_key_id, day) DO UPDATE SET
			prompt_tokens     = spend.prompt_tokens     + EXCLUDED.prompt_tokens,
			completion_tokens = spend.completion_tokens + EXCLUDED.completion_tokens`,
		apiKeyID, day.UTC().Format("2006-01-02"),
		promptTokens, completionTokens,
	)
	return err
}

// ── Key management ────────────────────────────────────────────────────────

// KeyRow is a row from the api_keys table (raw key is never returned after creation).
type KeyRow struct {
	ID        string
	Name      string
	Tier      string
	Owner     string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// CreateKey inserts a new API key. rawKey is hashed; the hash is stored.
// Returns the new UUID.
func (s *PostgresStore) CreateKey(ctx context.Context, rawKey, name, tier, owner string) (string, error) {
	hash := hashKey(rawKey)
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO api_keys (key_hash, name, tier, owner)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text`,
		hash, name, tier, owner,
	).Scan(&id)
	return id, err
}

// RevokeKey sets revoked_at to now for the given key UUID.
// Returns false if no such active key exists.
func (s *PostgresStore) RevokeKey(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE api_keys SET revoked_at = NOW()
		WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ListKeys returns all keys (active and revoked), newest first.
func (s *PostgresStore) ListKeys(ctx context.Context) ([]KeyRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name, tier, owner, created_at, revoked_at
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []KeyRow
	for rows.Next() {
		var k KeyRow
		if err := rows.Scan(&k.ID, &k.Name, &k.Tier, &k.Owner, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// ── PostgresKeyStore ───────────────────────────────────────────────────────

// PostgresKeyStore validates API keys against the api_keys table.
// It is a drop-in replacement for StaticKeyStore.
type PostgresKeyStore struct {
	pool *pgxpool.Pool
}

// NewPostgresKeyStore returns a KeyStore backed by the given pool.
// The pool must already be connected (use NewPostgresStore first).
func NewPostgresKeyStore(s *PostgresStore) *PostgresKeyStore {
	return &PostgresKeyStore{pool: s.pool}
}

// Validate hashes rawKey with SHA-256 and looks it up in api_keys.
// Returns the UUID string and true on success; ("", false) otherwise.
func (ks *PostgresKeyStore) Validate(ctx context.Context, rawKey string) (string, bool) {
	hash := hashKey(rawKey)
	var id string
	err := ks.pool.QueryRow(ctx,
		`SELECT id::text FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`,
		hash,
	).Scan(&id)
	if err != nil {
		return "", false
	}
	return id, true
}

// DailyTokens returns the total tokens (prompt + completion) consumed by a key
// on the given UTC day. Returns 0 with no error if no spend row exists yet.
func (s *PostgresStore) DailyTokens(ctx context.Context, apiKeyID string, day time.Time) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0)
		FROM spend
		WHERE api_key_id = $1 AND day = $2::date`,
		apiKeyID, day.UTC().Format("2006-01-02"),
	).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// GetKeyTier returns the tier name for the given key UUID.
// Returns ("default", nil) if the key is not found.
func (s *PostgresStore) GetKeyTier(ctx context.Context, apiKeyID string) (string, error) {
	var tier string
	err := s.pool.QueryRow(ctx,
		`SELECT tier FROM api_keys WHERE id = $1 AND revoked_at IS NULL`,
		apiKeyID,
	).Scan(&tier)
	if err != nil {
		return "default", nil
	}
	return tier, nil
}

// HashKey returns the SHA-256 hex digest of a raw API key.
// Exported so callers can pre-hash keys for insertion.
func HashKey(rawKey string) string { return hashKey(rawKey) }

func hashKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return fmt.Sprintf("%x", sum)
}
