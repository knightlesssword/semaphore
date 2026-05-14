# Semaphore — Task Document

---

## Current Session

- [x] Phase 6 — Admin HTTP server, key management (POST /keys, GET /keys, DELETE /keys/{id}), static token auth on :9090
- [x] Phase 7 — Token budget enforcement (daily `tokens_per_day` limit, distinct 402 error)
- [x] Phase 8 — Semaphore-native error codes (distinct error envelope from external provider errors)

---

## Pending Phases (next sessions, in order)

### Phase 9 — Per-key tier rate limiting
Wire `api_keys.tier` column to `RateLimiter.limitForKey`. Named tiers from config (`rate_limit.tiers`) apply; fallback to default. No new table needed.

### Phase 10 — Prometheus metrics
`/metrics` endpoint (separate port `:9091`). Counters: requests total, tokens total, cache hits. Histograms: latency. Per-provider + per-key labels.  
*Note: defer integration with external infra. Add after all core phases complete.*

### Phase 11 — Response caching
Redis-backed prompt cache (hash of model + messages → cached response). Optional feature flag (`cache.enabled`). Uses existing Redis connection, separate DB index. Docker/Redis already required for rate limiting so no new infra.  
*Note: same Redis instance, separate DB to keep concerns isolated.*

### Phase 12 — MinIO / local object storage logging
Stream full request+response bodies to S3-compatible storage (MinIO for local, real S3/GCS for prod). Optional (`storage.enabled`). Useful for audit replay and debugging.  
*Note: local-first via MinIO Docker service, same docker-compose pattern as Redis/Postgres.*

---

## Future Scope (prioritized by progression)

### F1 — Webhook spend threshold alerts
Trigger an HTTP callback when a key's daily spend crosses a configured threshold. Simple outbound POST, no new infra. Low complexity, high utility for multi-tenant use.

### F2 — Model routing / fallback rules
Route by model name pattern (e.g. `gpt-4*` → anthropic, fallback chain on upstream error). Config-driven rules evaluated in proxy handler before provider selection.

### F3 — Web dashboard
Spend, usage, key management UI. Reads from Postgres. Likely a separate service or embedded SPA. Depends on admin API being solid first.

### F4 — Provider key rotation (Phase 6 schema placeholder)
Implement `api_key_enc` encryption (AES-GCM, master key from env). Rotation logic: mark old key inactive, insert new, zero-downtime. Schema already present.

### F5 — Request/response archival to S3/GCS (production)
Promote Phase 12 MinIO integration to real cloud object storage with lifecycle policies, compression, and retrieval API.

---

## Decisions / Open Questions

- **Admin port**: Decided — separate `:9090` admin port, own auth middleware, firewall-friendly. Main API on `:8080`.
- **Admin auth**: Decided — static admin token in config (`admin.token`), overridable via `SEMAPHORE_ADMIN_TOKEN` env var.
- **Admin binary**: Decided — subcommand of main binary (`semaphore admin <cmd>`), not a separate binary.
- **Tier storage**: `api_keys.tier` (TEXT) maps to named tiers in config. Sufficient for now; revisit if dynamic tier CRUD is needed.
- **Token budget error code**: Decided — 402 with Semaphore-native error envelope (Phase 8).
- **Prometheus**: After Phase 12, not before.

### Future Scope additions
- **F6 — Admin `is_admin` flag**: Promote admin auth from static token to `api_keys` table with `is_admin` boolean. After F3 (dashboard) is in scope.
- **F7 — mTLS on admin port**: Strongest admin auth option, highest ops overhead. After F6.
