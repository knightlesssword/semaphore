package middleware

import (
	"fmt"
	"net/http"
)

const (
	SourceSemaphore = "semaphore"
	SourceProvider  = "provider"
)

// WriteError writes a JSON error envelope to w.
// source distinguishes Semaphore-generated errors from upstream provider errors.
//
//	{"error":{"message":"...","code":N,"source":"semaphore|provider"}}
func WriteError(w http.ResponseWriter, msg string, code int, source string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":{"message":%q,"code":%d,"source":%q}}`, msg, code, source)
}
