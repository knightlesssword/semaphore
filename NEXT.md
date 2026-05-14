# Semaphore — Next Sessions Checklist

Phases are ordered by dependency. Complete in sequence unless marked independent.

---

## Core Phases (must complete before Future Scope)

- [x] **Phase 9** — Per-key tier rate limiting
  - Wire `api_keys.tier` → `RateLimiter.limitForKey`
  - `GetKeyTier` query already exists (added in Phase 7)
  - Named tiers from `rate_limit.tiers` config; fallback to default
  - No new table or migration needed

- [ ] **Phase 10** — Prometheus metrics
  - `/metrics` endpoint on separate port `:9091`
  - Counters: requests total, tokens total, cache hits
  - Histograms: request latency
  - Labels: per-provider, per-key
  - No external infra dependency (just `prometheus/client_golang`)

- [ ] **Phase 11** — Response caching
  - Redis-backed prompt cache: hash(model + messages) → cached response
  - Feature flag: `cache.enabled`
  - Separate Redis DB index from rate limiter
  - No new infra (Redis already required for rate limiting)

- [ ] **Phase 12** — MinIO / local object storage logging
  - Stream full request + response bodies to S3-compatible storage
  - Feature flag: `storage.enabled`
  - Local-first via MinIO Docker service (same docker-compose pattern)
  - Prerequisite for F5

---

## Future Scope (after all core phases)

- [ ] **F1** — Webhook spend threshold alerts
  - Outbound HTTP POST when daily spend crosses configured threshold
  - No new infra; low complexity

- [ ] **F2** — Model routing / fallback rules
  - Config-driven rules: match model name pattern → select provider
  - Fallback chain on upstream error
  - Evaluated in proxy handler before provider selection

- [ ] **F3** — Web dashboard
  - Spend, usage, key management UI
  - Reads from Postgres
  - Depends on admin API being stable (Phase 6 ✅)

- [ ] **F4** — Provider key rotation
  - Encrypt `api_key_enc` with AES-GCM (master key from env)
  - Rotation: mark old inactive, insert new, zero-downtime
  - Schema placeholder already in `provider_keys` table

- [ ] **F5** — Request/response archival to S3/GCS (production)
  - Promote Phase 12 MinIO integration to real cloud storage
  - Add lifecycle policies, compression, retrieval API
  - Depends on Phase 12

- [ ] **F6** — Admin `is_admin` flag
  - Promote admin auth from static token to `api_keys.is_admin` column
  - Depends on F3 (dashboard)

- [ ] **F7** — mTLS on admin port
  - Strongest admin auth option; highest ops overhead
  - Depends on F6
