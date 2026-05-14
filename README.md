# Semaphore

> A self-hosted LLM gateway with auth, rate limiting, spend tracking, and multi-provider routing. Drop it in front of any AI API and own your traffic.

Semaphore sits between your applications and upstream LLM providers (OpenAI, Anthropic, Ollama). It presents a single OpenAI-compatible endpoint, handles authentication, enforces rate limits and token budgets, logs every request, and lets you manage API keys through an admin API — all without touching your client code.

---

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Providers](#providers)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Environment Variables](#environment-variables)
- [API Reference](#api-reference)
  - [Proxy Endpoint](#proxy-endpoint)
  - [Admin API](#admin-api)
- [Middleware Pipeline](#middleware-pipeline)
- [Rate Limiting](#rate-limiting)
- [Token Budget Enforcement](#token-budget-enforcement)
- [Tier System](#tier-system)
- [Audit Logging](#audit-logging)
- [Error Envelope](#error-envelope)
- [Database Schema](#database-schema)
- [Deployment](#deployment)
- [Building from Source](#building-from-source)
- [Roadmap](#roadmap)

---

## Features

| Feature | Details |
|---|---|
| **Multi-provider routing** | OpenAI, Anthropic, Ollama — switchable per-request via `X-Provider` header |
| **OpenAI-compatible API** | Clients need zero changes; Semaphore translates formats transparently |
| **Streaming support** | SSE pass-through with per-provider chunk translation |
| **Bearer token auth** | Static keys (config) or database-backed keys (Postgres) with SHA-256 hashing |
| **Sliding-window rate limiting** | Redis-backed, atomic Lua script, per-key, with `X-RateLimit-*` headers |
| **Per-key tier rate limits** | Named tiers (`free`, `pro`, etc.) mapped to different RPM limits |
| **Daily token budget** | Per-key `tokens_per_day` cap with tier overrides; returns `402` when exceeded |
| **Async audit logging** | Every proxied request written to Postgres via a 512-slot buffered channel |
| **Admin HTTP API** | Key management (create/list/revoke) on a separate port with its own auth |
| **Auto migrations** | Embedded SQL migrations run at startup; idempotent and transactional |
| **Structured logging** | `slog`-based, `text` or `json` format, configurable level |
| **Graceful shutdown** | SIGINT/SIGTERM drains in-flight requests before exit |
| **Health check** | `GET /healthz` — unauthenticated, returns `{"status":"ok"}` |
| **Fail-open design** | Redis/Postgres errors never block requests — they log and allow through |
| **Distroless container** | Minimal attack surface; no shell, no package manager |

---

## Architecture

```
Client
  │
  │  POST /v1/chat/completions
  │  Authorization: Bearer <key>
  ▼
┌─────────────────────────────────────────────┐
│                 Semaphore :8080              │
│                                             │
│  Recover (panic handler)                    │
│    └─ RequestID (X-Request-ID injection)    │
│         └─ Auth (Bearer token validation)   │
│              └─ BudgetCheck (daily tokens)  │
│                   └─ RateLimit (sliding RL) │
│                        └─ Audit (async log) │
│                             └─ Proxy        │
│                                  │          │
└──────────────────────────────────┼──────────┘
                                   │ HTTP reverse proxy
          ┌──────────┬─────────────┼─────────────┐
          ▼          ▼             ▼              ▼
       OpenAI    Anthropic       Ollama        (future)
       :443       :443          :11434

┌─────────────┐    ┌──────────────┐
│  Redis :6379│    │ Postgres :5432│
│  Rate limit │    │ Keys, audit,  │
│  buckets    │    │ spend, schema │
└─────────────┘    └──────────────┘

┌──────────────────────┐
│  Admin Server :9090  │
│  POST   /keys        │
│  GET    /keys        │
│  DELETE /keys/{id}   │
└──────────────────────┘
```

Each middleware in the chain is optional and activates only when its dependencies are present — Redis for rate limiting, Postgres for budget/audit/admin. Running without either gives you a pure authenticated proxy.

---

## Providers

| Provider | Header value | Notes |
|---|---|---|
| **OpenAI** | `openai` | Full pass-through; format is native |
| **Anthropic** | `anthropic` | Translates OpenAI → Messages API; supports streaming |
| **Ollama** | `ollama` | Routes to local Ollama instance; `default_model` override available |

Select the provider per-request with the `X-Provider` header, or set `proxy.default_provider` in config to route everything to one backend.

Anthropic supports an optional `default_model` override in config — useful when you want to pin all traffic to a specific Claude version regardless of what the client sends.

---

## Quick Start

### Docker Compose (recommended)

```bash
git clone https://github.com/knightlesssword/semaphore
cd semaphore

# Set your provider keys
export SEMAPHORE_PROXY_PROVIDERS_ANTHROPIC_API_KEY=sk-ant-...
export SEMAPHORE_PROXY_PROVIDERS_OPENAI_API_KEY=sk-...

docker compose -f deploy/docker-compose.yml up
```

This starts Semaphore on `:8080`, Redis on `:6379`, and Postgres on `:5432`. The admin server starts on `:9090`.

### Test the proxy

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer dev-key-local" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Health check

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

---

## Configuration

All configuration lives in `config.yaml`. Every value can be overridden with an environment variable (see [Environment Variables](#environment-variables)).

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  shutdown_timeout_seconds: 15

admin:
  enabled: false          # enable the admin HTTP server on :9090
  port: 9090
  token: "test-admin-token"

auth:
  bypass: false           # true = skip auth entirely (local dev only)
  static_keys:
    - "dev-key-local"     # used when postgres.enabled is false

proxy:
  default_provider: "anthropic"
  timeout_seconds: 120
  providers:
    openai:
      base_url: "https://api.openai.com"
      api_key: ""
    anthropic:
      base_url: "https://api.anthropic.com"
      api_key: ""
      default_model: "claude-sonnet-4-6"
    ollama:
      base_url: "http://localhost:11434"
      default_model: ""

rate_limit:
  enabled: false                # requires Redis
  requests_per_minute: 60
  tokens_per_day: 0             # 0 = unlimited

  # Optional per-tier overrides (requires Postgres for tier lookups):
  # tiers:
  #   free:
  #     requests_per_minute: 20
  #     tokens_per_day: 10000
  #   pro:
  #     requests_per_minute: 120
  #     tokens_per_day: 500000

redis:
  addr: "localhost:6379"
  password: ""
  db: 0

postgres:
  enabled: false
  dsn: "postgres://semaphore:semaphore@localhost:5432/semaphore?sslmode=disable"

log:
  level: "info"     # debug | info | warn | error
  format: "text"    # text | json
```

### Minimal setup (no infra)

Set `auth.bypass: true` (or add your keys to `auth.static_keys`) and point `proxy.default_provider` at a provider with an API key. Redis and Postgres are both optional.

### Full setup (production-grade)

Enable `postgres.enabled: true`, `rate_limit.enabled: true`, and `admin.enabled: true`. Set `postgres.dsn` and `redis.addr` for your infrastructure. Migrations run automatically at startup.

---

## Environment Variables

Every config key maps to an environment variable with the prefix `SEMAPHORE_` and dots replaced by underscores (uppercased). Examples:

| Environment Variable | Config key | Example |
|---|---|---|
| `SEMAPHORE_SERVER_PORT` | `server.port` | `8080` |
| `SEMAPHORE_ADMIN_ENABLED` | `admin.enabled` | `true` |
| `SEMAPHORE_ADMIN_TOKEN` | `admin.token` | `my-secret-token` |
| `SEMAPHORE_AUTH_BYPASS` | `auth.bypass` | `false` |
| `SEMAPHORE_PROXY_DEFAULT_PROVIDER` | `proxy.default_provider` | `anthropic` |
| `SEMAPHORE_PROXY_PROVIDERS_OPENAI_API_KEY` | `proxy.providers.openai.api_key` | `sk-...` |
| `SEMAPHORE_PROXY_PROVIDERS_ANTHROPIC_API_KEY` | `proxy.providers.anthropic.api_key` | `sk-ant-...` |
| `SEMAPHORE_RATE_LIMIT_ENABLED` | `rate_limit.enabled` | `true` |
| `SEMAPHORE_RATE_LIMIT_REQUESTS_PER_MINUTE` | `rate_limit.requests_per_minute` | `60` |
| `SEMAPHORE_RATE_LIMIT_TOKENS_PER_DAY` | `rate_limit.tokens_per_day` | `100000` |
| `SEMAPHORE_REDIS_ADDR` | `redis.addr` | `redis:6379` |
| `SEMAPHORE_POSTGRES_DSN` | `postgres.dsn` | `postgres://...` |
| `SEMAPHORE_LOG_LEVEL` | `log.level` | `info` |
| `SEMAPHORE_LOG_FORMAT` | `log.format` | `json` |

Pass a custom config file path with the `-config` flag:

```bash
./semaphore -config /etc/semaphore/config.yaml
```

---

## API Reference

### Proxy Endpoint

**`POST /v1/chat/completions`**

OpenAI-compatible chat completions endpoint. Accepts the standard OpenAI request body. Semaphore translates it to the target provider's native format before forwarding.

**Headers**

| Header | Required | Description |
|---|---|---|
| `Authorization` | Yes (unless bypass) | `Bearer <api-key>` |
| `Content-Type` | Yes | `application/json` |
| `X-Provider` | No | Override provider: `openai`, `anthropic`, `ollama` |

**Response headers set by Semaphore**

| Header | Description |
|---|---|
| `X-Request-ID` | Unique request identifier (UUID) |
| `X-RateLimit-Limit` | Requests allowed per minute |
| `X-RateLimit-Remaining` | Requests remaining in current window |
| `Retry-After` | Seconds to wait (only present on `429`) |

**Example request**

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Explain rate limiting."}],
    "stream": false
  }'
```

**Route to a specific provider**

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "X-Provider: anthropic" \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-sonnet-4-6", "messages": [...]}'
```

**Streaming**

Set `"stream": true` in the request body. Semaphore detects this, sets `Content-Type: text/event-stream`, disables nginx buffering, and streams provider SSE chunks directly to the client.

---

### Admin API

The admin server runs on `:9090` (configurable) and is completely separate from the proxy. All routes require `Authorization: Bearer <admin-token>`.

**Prerequisites:** `admin.enabled: true` and `postgres.enabled: true` must both be set.

---

#### `POST /keys` — Create API key

Creates a new API key. The raw key is returned **once** and never stored — Semaphore stores only the SHA-256 hash.

**Request body**

```json
{
  "name": "my-app",
  "tier": "pro",
  "owner": "team@example.com"
}
```

| Field | Required | Description |
|---|---|---|
| `name` | Yes | Human-readable label for the key |
| `tier` | No | Tier name matching `rate_limit.tiers` config; defaults to `"default"` |
| `owner` | No | Owner identifier (email, team name, etc.) |

**Response `200`**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "key": "sk-4a7f2c9e...",
  "name": "my-app",
  "tier": "pro",
  "owner": "team@example.com"
}
```

```bash
curl -X POST http://localhost:9090/keys \
  -H "Authorization: Bearer test-admin-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-app", "tier": "pro", "owner": "team@example.com"}'
```

---

#### `GET /keys` — List all keys

Returns all keys (active and revoked), newest first.

**Response `200`**

```json
{
  "count": 2,
  "keys": [
    {
      "id": "550e8400-...",
      "name": "my-app",
      "tier": "pro",
      "owner": "team@example.com",
      "created_at": "2026-05-14T10:00:00Z",
      "revoked_at": null
    },
    {
      "id": "661f9511-...",
      "name": "old-key",
      "tier": "free",
      "owner": "dev@example.com",
      "created_at": "2026-04-01T08:00:00Z",
      "revoked_at": "2026-05-01T12:00:00Z"
    }
  ]
}
```

```bash
curl http://localhost:9090/keys \
  -H "Authorization: Bearer test-admin-token"
```

---

#### `DELETE /keys/{id}` — Revoke a key

Soft-deletes the key by setting `revoked_at`. The key immediately stops validating. Idempotent attempts to revoke an already-revoked key return `404`.

**Response `200`**

```json
{"id": "550e8400-...", "status": "revoked"}
```

```bash
curl -X DELETE http://localhost:9090/keys/550e8400-e29b-41d4-a716-446655440000 \
  -H "Authorization: Bearer test-admin-token"
```

---

## Middleware Pipeline

Requests flow through this chain in order:

```
Recover → RequestID → Auth → BudgetCheck → RateLimit → Audit → Proxy
```

| Middleware | Always active | Requires | On failure |
|---|---|---|---|
| **Recover** | Yes | — | Catches panics, returns `500` |
| **RequestID** | Yes | — | Generates UUID, adds `X-Request-ID` header |
| **Auth** | Yes (unless bypass) | KeyStore (static or Postgres) | `401 Unauthorized` |
| **BudgetCheck** | No | Postgres + `tokens_per_day > 0` | `402 Payment Required` |
| **RateLimit** | No | Redis + `rate_limit.enabled: true` | `429 Too Many Requests` |
| **Audit** | No | Postgres | Fail open (logs error) |
| **Proxy** | Yes | Provider config | `502 Bad Gateway` |

Middleware that depends on unavailable infrastructure is simply omitted from the chain at startup — the binary never fails to start because Redis or Postgres is missing.

---

## Rate Limiting

Rate limiting uses a **sliding window** algorithm implemented in a Redis Lua script that runs atomically. The window is 1 minute.

**How it works:**

1. Each key maps to a Redis sorted set keyed as `rl:<bearer-token>`.
2. On each request, entries older than 60 seconds are removed.
3. If the remaining count is under the limit, the request is admitted and a new entry is added.
4. If at or over the limit, the request is rejected with `429` and a `Retry-After` header.

**Response headers on allowed requests:**

```
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 42
```

**Response on denial (`429`):**

```json
{
  "error": {
    "message": "rate limit exceeded — 60 req/min allowed",
    "code": 429,
    "source": "semaphore"
  }
}
```

Redis failures fail open — requests are allowed through and a warning is logged. This ensures Redis downtime never takes down your application.

---

## Token Budget Enforcement

When `rate_limit.tokens_per_day > 0` and Postgres is enabled, Semaphore enforces a daily token cap per API key.

- Token counts are read from the `spend` table, which is updated asynchronously by the audit logger after each request.
- Budget checks happen **before** the request is proxied, using the previous period's spend data.
- When the budget is exceeded, Semaphore returns `402 Payment Required`:

```json
{
  "error": {
    "message": "daily token budget exceeded — 95000 of 100000 tokens used",
    "code": 402,
    "source": "semaphore"
  }
}
```

Store errors fail open — if Postgres is unreachable, the budget check is skipped and the request is allowed.

---

## Tier System

Tiers let you assign different rate limits and token budgets to different API keys without restarting the server.

**1. Define tiers in config:**

```yaml
rate_limit:
  requests_per_minute: 60    # default (all keys not in a named tier)
  tokens_per_day: 50000

  tiers:
    free:
      requests_per_minute: 20
      tokens_per_day: 10000
    pro:
      requests_per_minute: 120
      tokens_per_day: 500000
```

**2. Assign a tier when creating a key:**

```bash
curl -X POST http://localhost:9090/keys \
  -H "Authorization: Bearer test-admin-token" \
  -d '{"name": "premium-client", "tier": "pro"}'
```

**Resolution order:**

1. Look up the key's `tier` column in `api_keys`.
2. Find a matching tier block in `rate_limit.tiers`.
3. Apply that tier's `requests_per_minute` / `tokens_per_day`.
4. Fall back to the top-level defaults if no tier match is found.

Tier lookups fail open — if Postgres is unavailable, the default limit applies.

---

## Audit Logging

Every request proxied through Semaphore (when Postgres is enabled) is written to the `requests` table. Logging is **fully asynchronous** — a background goroutine drains a 512-slot buffered channel, so the response path never waits on a database write.

**Captured per request:**

| Field | Description |
|---|---|
| `api_key_id` | UUID of the authenticated key (NULL for bypass/static) |
| `request_id` | Value of `X-Request-ID` |
| `provider` | `openai`, `anthropic`, or `ollama` |
| `model` | Model name from the request |
| `prompt_tokens` | Token count parsed from response |
| `completion_tokens` | Token count parsed from response |
| `latency_ms` | End-to-end latency in milliseconds |
| `status` | HTTP status code returned to client |
| `created_at` | UTC timestamp |

Token counts for streaming responses are recorded as `0` (the response body is not buffered for streaming requests).

Spend totals are also upserted into the `spend` table with a daily granularity, enabling the budget enforcement middleware to query cumulative usage.

---

## Error Envelope

All errors from Semaphore (auth failures, rate limits, budget exceeded, panics) follow a consistent JSON envelope. The `source` field distinguishes Semaphore-generated errors from upstream provider errors passed through transparently.

```json
{
  "error": {
    "message": "human-readable description",
    "code": 429,
    "source": "semaphore"
  }
}
```

**Source values:**

| Source | Meaning |
|---|---|
| `"semaphore"` | Error originated inside Semaphore (auth, rate limit, budget, panic) |
| `"provider"` | Upstream provider was unreachable or returned an error |

Provider errors (4xx/5xx from OpenAI, Anthropic, Ollama) are passed through as-is with their original status codes. Only unreachable upstream errors are wrapped with `source: "provider"`.

---

## Database Schema

Semaphore manages its own schema via embedded SQL migrations that run at startup. Migrations are idempotent and transactional — a failed migration rolls back cleanly.

**Tables:**

```sql
-- API keys issued to clients
api_keys (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  key_hash    TEXT NOT NULL UNIQUE,   -- SHA-256 of the raw key
  name        TEXT NOT NULL,
  tier        TEXT NOT NULL DEFAULT 'default',
  owner       TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at  TIMESTAMPTZ             -- NULL = active
)

-- Provider credentials (schema placeholder for future rotation)
provider_keys (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  provider    TEXT NOT NULL,
  api_key_enc TEXT NOT NULL,          -- AES-GCM encrypted (future)
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
)

-- Per-request audit log
requests (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  api_key_id        UUID REFERENCES api_keys(id),
  request_id        TEXT,
  provider          TEXT,
  model             TEXT,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  latency_ms        BIGINT NOT NULL DEFAULT 0,
  cached            BOOLEAN NOT NULL DEFAULT FALSE,
  status            INTEGER NOT NULL DEFAULT 0,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
)

-- Daily aggregated spend per key
spend (
  api_key_id        UUID NOT NULL REFERENCES api_keys(id),
  day               DATE NOT NULL,
  prompt_tokens     BIGINT NOT NULL DEFAULT 0,
  completion_tokens BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (api_key_id, day)
)
```

---

## Deployment

### Docker Compose

The included `deploy/docker-compose.yml` runs Semaphore with Redis and Postgres pre-configured:

```bash
# Clone and start everything
git clone https://github.com/knightlesssword/semaphore
cd semaphore

docker compose -f deploy/docker-compose.yml up -d

# Check logs
docker compose -f deploy/docker-compose.yml logs -f semaphore
```

Services and ports:

| Service | Port |
|---|---|
| Semaphore proxy | `8080` |
| Semaphore admin | `9090` |
| Redis | `6379` |
| Postgres | `5432` |

Postgres data is persisted in a named Docker volume (`postgres_data`).

### Standalone binary

```bash
./semaphore -config /path/to/config.yaml
```

### Production checklist

- Set `SEMAPHORE_ADMIN_TOKEN` to a strong random secret
- Set `SEMAPHORE_PROXY_PROVIDERS_*_API_KEY` via environment (never commit keys to config files)
- Set `log.format: json` for structured log ingestion
- Firewall port `9090` — the admin API should never be public-facing
- Use a connection pooler (PgBouncer) in front of Postgres for high-concurrency workloads
- Set `auth.bypass: false` (the default) in all non-development environments

---

## Building from Source

**Requirements:** Go 1.25+

```bash
git clone https://github.com/knightlesssword/semaphore
cd semaphore

# Run
go run ./cmd/semaphore -config internal/config/config.yaml

# Build binary
go build -o semaphore ./cmd/semaphore

# Run tests
go test ./...

# Build Docker image
docker build -f deploy/Dockerfile -t semaphore:latest .
```

The binary is statically linked (`CGO_ENABLED=0`) and runs on the distroless base image with no shell or package manager.

---

## Roadmap

| Phase | Status | Description |
|---|---|---|
| 1–4 | Done | Core proxy, auth, static keys, OpenAI/Anthropic/Ollama support |
| 5 | Done | Postgres schema, migration runner, async audit logging |
| 6 | Done | Admin HTTP server — key create/list/revoke |
| 7 | Done | Daily token budget enforcement (`402` on overage) |
| 8 | Done | Semaphore-native error envelope with `source` field |
| 9 | Done | Per-key tier rate limiting via `api_keys.tier` |
| 10 | Planned | Prometheus metrics on `:9091` — requests, tokens, latency histograms |
| 11 | Planned | Redis-backed prompt caching (hash of model + messages) |
| 12 | Planned | MinIO / S3-compatible full request+response body archival |
| F1 | Future | Webhook alerts on spend threshold |
| F2 | Future | Model routing and fallback chains |
| F3 | Future | Web dashboard (spend, usage, key management UI) |
| F4 | Future | Provider key rotation with AES-GCM encryption |

---

## License

MIT
