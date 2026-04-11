-- 001_init.sql — core schema for Semaphore

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT        PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- API keys issued to gateway clients.
-- key_hash is SHA-256(rawKey) stored as hex; the raw key is never persisted.
CREATE TABLE IF NOT EXISTS api_keys (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash   TEXT        UNIQUE NOT NULL,
    name       TEXT        NOT NULL DEFAULT '',
    tier       TEXT        NOT NULL DEFAULT 'default',
    owner      TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

-- Provider API keys managed by the gateway (used in Phase 6 key rotation).
CREATE TABLE IF NOT EXISTS provider_keys (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider   TEXT        NOT NULL,
    api_key_enc TEXT       NOT NULL,   -- encrypted at rest (Phase 6)
    priority   INT         NOT NULL DEFAULT 0,
    active     BOOLEAN     NOT NULL DEFAULT TRUE,
    rotated_at TIMESTAMPTZ
);

-- Per-request audit log.
-- api_key_id is nullable: NULL for bypass/unauthenticated requests.
CREATE TABLE IF NOT EXISTS requests (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    api_key_id        UUID        REFERENCES api_keys(id),
    request_id        TEXT,                      -- X-Request-ID header value
    provider          TEXT        NOT NULL DEFAULT '',
    model             TEXT        NOT NULL DEFAULT '',
    prompt_tokens     INT         NOT NULL DEFAULT 0,
    completion_tokens INT         NOT NULL DEFAULT 0,
    latency_ms        BIGINT      NOT NULL DEFAULT 0,
    cached            BOOLEAN     NOT NULL DEFAULT FALSE,
    status            INT         NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_requests_api_key_id  ON requests(api_key_id);
CREATE INDEX IF NOT EXISTS idx_requests_created_at  ON requests(created_at DESC);

-- Daily spend accumulator per API key.
-- Upserted after each successful proxied request.
CREATE TABLE IF NOT EXISTS spend (
    api_key_id        UUID   NOT NULL REFERENCES api_keys(id),
    day               DATE   NOT NULL,
    prompt_tokens     BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    usd_cents         BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (api_key_id, day)
);
