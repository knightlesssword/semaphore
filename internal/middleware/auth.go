package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// KeyStore is the interface the auth middleware depends on.
// Phase 3 uses StaticKeyStore (keys from config).
// Phase 5 swaps in a PostgresKeyStore (keys from the api_keys table).
type KeyStore interface {
	Validate(ctx context.Context, rawKey string) bool
}

// Auth validates the Authorization: Bearer <key> header on every request.
// On success it passes through. On failure it returns 401.
// If bypass is true (dev mode), all requests are allowed without a key.
func Auth(store KeyStore, bypass bool, logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bypass {
				next.ServeHTTP(w, r)
				return
			}

			key, ok := bearerToken(r)
			if !ok {
				logger.Warn("auth: missing or malformed Authorization header",
					"request_id", GetRequestID(r.Context()),
					"path", r.URL.Path,
				)
				jsonUnauthorized(w, "missing or malformed Authorization header")
				return
			}

			if !store.Validate(r.Context(), key) {
				logger.Warn("auth: invalid API key",
					"request_id", GetRequestID(r.Context()),
					"path", r.URL.Path,
				)
				jsonUnauthorized(w, "invalid API key")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token from "Authorization: Bearer <token>".
// Returns ("", false) if the header is absent or not Bearer-prefixed.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}

func jsonUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":{"message":"` + msg + `","code":401}}`))
}

// ── Static key store (Phase 3) ────────────────────────────────────────────

// StaticKeyStore validates against a fixed set of keys defined in config.
// Replaced by PostgresKeyStore in Phase 5.
type StaticKeyStore struct {
	keys map[string]struct{}
}

func NewStaticKeyStore(keys []string) *StaticKeyStore {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k != "" {
			m[k] = struct{}{}
		}
	}
	return &StaticKeyStore{keys: m}
}

func (s *StaticKeyStore) Validate(_ context.Context, rawKey string) bool {
	_, ok := s.keys[rawKey]
	return ok
}
