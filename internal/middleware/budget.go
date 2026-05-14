package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/knightlesssword/semaphore/internal/config"
)

// BudgetStore is the subset of store.PostgresStore needed for budget checks.
type BudgetStore interface {
	DailyTokens(ctx context.Context, apiKeyID string, day time.Time) (int64, error)
	GetKeyTier(ctx context.Context, apiKeyID string) (string, error)
}

// BudgetChecker holds the store and config needed to enforce token budgets.
type BudgetChecker struct {
	store  BudgetStore
	cfg    *config.RateLimitConfig
	logger *slog.Logger
}

// NewBudgetChecker creates a BudgetChecker. store must be non-nil.
func NewBudgetChecker(store BudgetStore, cfg *config.RateLimitConfig, logger *slog.Logger) *BudgetChecker {
	return &BudgetChecker{store: store, cfg: cfg, logger: logger}
}

// BudgetCheck returns a Middleware that enforces the daily token budget per key.
// Requests with no key ID (bypass mode or static keys without UUIDs) are passed through.
// On store error, fails open.
func BudgetCheck(bc *BudgetChecker) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			keyID := GetKeyID(r.Context())
			if keyID == "" {
				next.ServeHTTP(w, r)
				return
			}

			limit := bc.limitForKey(r.Context(), keyID)
			if limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			used, err := bc.store.DailyTokens(r.Context(), keyID, time.Now().UTC())
			if err != nil {
				bc.logger.Warn("budget: store error, failing open",
					"request_id", GetRequestID(r.Context()),
					"key_id", keyID,
					"err", err,
				)
				next.ServeHTTP(w, r)
				return
			}

			if used >= int64(limit) {
				bc.logger.Warn("budget: daily token limit exceeded",
					"request_id", GetRequestID(r.Context()),
					"key_id", keyID,
					"used", used,
					"limit", limit,
				)
				jsonPaymentRequired(w, used, int64(limit))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// limitForKey resolves the tokens_per_day limit for a key.
// Looks up the key's tier, then checks for a tier-specific override in config.
// Returns 0 for unlimited.
func (bc *BudgetChecker) limitForKey(ctx context.Context, keyID string) int {
	tier, err := bc.store.GetKeyTier(ctx, keyID)
	if err != nil || tier == "" {
		tier = "default"
	}

	if t, ok := bc.cfg.Tiers[tier]; ok && t.TokensPerDay > 0 {
		return t.TokensPerDay
	}
	return bc.cfg.TokensPerDay
}

func jsonPaymentRequired(w http.ResponseWriter, used, limit int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	msg := fmt.Sprintf(
		`{"error":{"message":"daily token budget exceeded — %d of %d tokens used","code":402}}`,
		used, limit,
	)
	w.Write([]byte(msg))
}
