package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type contextKey string

const CtxRequestID contextKey = "request_id"

// RequestID injects a unique X-Request-ID header into every request and
// stores the ID in the request context so downstream handlers can log it.
// If the client already sends X-Request-ID, that value is used as-is.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if id == "" {
				id = newRequestID()
			}

			// Propagate to client and downstream context.
			w.Header().Set("X-Request-ID", id)
			ctx := context.WithValue(r.Context(), CtxRequestID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetRequestID retrieves the request ID from a context, or "" if absent.
func GetRequestID(ctx context.Context) string {
	if v, ok := ctx.Value(CtxRequestID).(string); ok {
		return v
	}
	return ""
}

func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck — crypto/rand.Read never errors on supported platforms
	return hex.EncodeToString(b)
}
