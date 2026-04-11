package middleware

import "net/http"

// Middleware is the standard Go middleware signature.
// Each middleware wraps the next handler, forming a chain.
type Middleware func(http.Handler) http.Handler

// Chain composes middlewares left-to-right: the first middleware listed
// is the outermost — the first to receive a request and last to return.
//
// Example:
//
//	Chain(Recover, RequestID, Auth)(proxyHandler)
//
// Request flow:  Recover → RequestID → Auth → proxyHandler
// Response flow: proxyHandler → Auth → RequestID → Recover
func Chain(middlewares ...Middleware) Middleware {
	return func(next http.Handler) http.Handler {
		// Wrap in reverse so the first entry ends up outermost.
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}
		return next
	}
}
