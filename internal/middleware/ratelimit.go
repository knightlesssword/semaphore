package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/redis/go-redis/v9"
)

// slidingWindowScript atomically:
//   1. Removes entries older than the window.
//   2. Counts remaining entries.
//   3. If under limit: adds the new entry, sets key TTL.
//
// KEYS[1]  = rate-limit bucket key  (e.g. "rl:sha256:<key>")
// ARGV[1]  = now in milliseconds
// ARGV[2]  = window size in milliseconds
// ARGV[3]  = max requests per window
// ARGV[4]  = unique member ID for this request
//
// Returns [allowed (0|1), current_count, retry_after_ms].
var slidingWindowScript = redis.NewScript(`
local key      = KEYS[1]
local now      = tonumber(ARGV[1])
local window   = tonumber(ARGV[2])
local limit    = tonumber(ARGV[3])
local req_id   = ARGV[4]
local cutoff   = now - window

redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)

local count = redis.call('ZCARD', key)

if count >= limit then
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local retry_after = window
    if #oldest >= 2 then
        retry_after = math.ceil(tonumber(oldest[2]) + window - now)
    end
    return {0, count, retry_after}
end

redis.call('ZADD', key, now, req_id)
redis.call('PEXPIRE', key, window)

return {1, count + 1, 0}
`)

// RateLimiter performs sliding-window rate limiting backed by Redis.
// It is constructed once and shared across requests.
type RateLimiter struct {
	rdb    *redis.Client
	cfg    *config.RateLimitConfig
	logger *slog.Logger
}

// NewRateLimiter creates a RateLimiter. rdb must already be connected.
func NewRateLimiter(rdb *redis.Client, cfg *config.RateLimitConfig, logger *slog.Logger) *RateLimiter {
	return &RateLimiter{rdb: rdb, cfg: cfg, logger: logger}
}

// RateLimit returns a Middleware that enforces per-key sliding-window limits.
// The key identifier is derived from the Authorization Bearer token.
// If auth.bypass is active (no bearer token), rate limiting is skipped.
func RateLimit(rl *RateLimiter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := bearerToken(r)
			if !ok {
				// No bearer token means bypass mode — skip limiting.
				next.ServeHTTP(w, r)
				return
			}

			limit := rl.limitForKey(key)
			if limit <= 0 {
				// Unlimited
				next.ServeHTTP(w, r)
				return
			}

			allowed, remaining, retryAfterMs, err := rl.check(r.Context(), key, limit)
			if err != nil {
				// On Redis failure, fail open (allow the request) and log.
				rl.logger.Warn("rate-limit: redis error, failing open",
					"request_id", GetRequestID(r.Context()),
					"err", err,
				)
				next.ServeHTTP(w, r)
				return
			}

			setRateLimitHeaders(w, limit, remaining)

			if !allowed {
				retryAfterSec := int(retryAfterMs/1000) + 1
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSec))
				rl.logger.Warn("rate-limit: request denied",
					"request_id", GetRequestID(r.Context()),
					"limit", limit,
					"retry_after_sec", retryAfterSec,
				)
				jsonTooManyRequests(w, limit, retryAfterSec)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// check runs the Lua sliding-window script and returns:
//   - allowed: whether this request is within the limit
//   - remaining: requests used so far in this window (after this one)
//   - retryAfterMs: milliseconds until a slot opens (only meaningful when !allowed)
func (rl *RateLimiter) check(ctx context.Context, key string, limit int) (allowed bool, remaining int, retryAfterMs int64, err error) {
	bucketKey := fmt.Sprintf("rl:%s", key)
	nowMs := time.Now().UnixMilli()
	windowMs := int64(60_000) // 1 minute in milliseconds
	reqID := uniqueID()

	result, err := slidingWindowScript.Run(ctx, rl.rdb,
		[]string{bucketKey},
		nowMs, windowMs, limit, reqID,
	).Slice()
	if err != nil {
		return false, 0, 0, err
	}

	// Lua returns [allowed(0|1), count, retry_after_ms]
	allowedInt, _ := result[0].(int64)
	countInt, _   := result[1].(int64)
	retryInt, _   := result[2].(int64)

	return allowedInt == 1, int(countInt), retryInt, nil
}

// limitForKey returns the requests-per-minute limit for the given key.
// Phase 4 uses a single default limit for all keys.
// Phase 5/9 can extend this with per-key tier lookup from Postgres.
func (rl *RateLimiter) limitForKey(_ string) int {
	return rl.cfg.RequestsPerMinute
}

// uniqueID returns a short random hex string suitable for use as a sorted-set member.
// Using crypto/rand ensures uniqueness even when many requests arrive in the same millisecond.
func uniqueID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck // crypto/rand.Read never errors on supported platforms
	return hex.EncodeToString(b)
}

func setRateLimitHeaders(w http.ResponseWriter, limit, used int) {
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
}

func jsonTooManyRequests(w http.ResponseWriter, limit, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	msg := fmt.Sprintf(`{"error":{"message":"rate limit exceeded — %d req/min allowed","code":429}}`, limit)
	w.Write([]byte(msg))
}
