package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover catches any panic in downstream handlers, logs it with a stack
// trace, and returns a 500 to the client instead of crashing the server.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					logger.Error("panic recovered",
						"err", err,
						"stack", string(debug.Stack()),
						"method", r.Method,
						"path", r.URL.Path,
					)
					WriteError(w, "internal server error", http.StatusInternalServerError, SourceSemaphore)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
