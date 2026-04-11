package middleware_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/knightlesssword/semaphore/internal/middleware"
	"github.com/redis/go-redis/v9"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestLimiter starts a miniredis server and returns a RateLimiter wired to it.
func newTestLimiter(t *testing.T, rpm int) (*middleware.RateLimiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })

	cfg := &config.RateLimitConfig{
		Enabled:           true,
		RequestsPerMinute: rpm,
	}
	return middleware.NewRateLimiter(rdb, cfg, noopLogger()), mr
}

// requestWithBearer builds a POST request with an Authorization header.
func requestWithBearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	rl, _ := newTestLimiter(t, 5)
	mw := middleware.RateLimit(rl)
	handler := mw(okHandler())

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, requestWithBearer("test-key"))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, w.Code)
		}
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	rl, _ := newTestLimiter(t, 3)
	mw := middleware.RateLimit(rl)
	handler := mw(okHandler())

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, requestWithBearer("test-key"))
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, w.Code)
		}
	}

	// 4th request should be rate limited
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, requestWithBearer("test-key"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request: got %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429")
	}
}

func TestRateLimit_SetsHeaders(t *testing.T) {
	rl, _ := newTestLimiter(t, 10)
	mw := middleware.RateLimit(rl)
	handler := mw(okHandler())

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, requestWithBearer("test-key"))

	if got := w.Header().Get("X-RateLimit-Limit"); got != "10" {
		t.Errorf("X-RateLimit-Limit: got %q, want %q", got, "10")
	}
	remaining, err := strconv.Atoi(w.Header().Get("X-RateLimit-Remaining"))
	if err != nil || remaining < 0 {
		t.Errorf("X-RateLimit-Remaining: got %q, expected non-negative integer", w.Header().Get("X-RateLimit-Remaining"))
	}
}

func TestRateLimit_IsolatesKeys(t *testing.T) {
	rl, _ := newTestLimiter(t, 2)
	mw := middleware.RateLimit(rl)
	handler := mw(okHandler())

	// Exhaust key-a
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, requestWithBearer("key-a"))
		if w.Code != http.StatusOK {
			t.Fatalf("key-a request %d: got %d", i+1, w.Code)
		}
	}

	// key-a is now rate limited
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, requestWithBearer("key-a"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("key-a 3rd: got %d, want 429", w.Code)
	}

	// key-b should still be allowed (separate bucket)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, requestWithBearer("key-b"))
	if w.Code != http.StatusOK {
		t.Fatalf("key-b: got %d, want 200", w.Code)
	}
}

func TestRateLimit_SkipsWithNoBearer(t *testing.T) {
	rl, _ := newTestLimiter(t, 1)
	mw := middleware.RateLimit(rl)
	handler := mw(okHandler())

	// No Authorization header — rate limiter should pass through
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("no-bearer request %d: got %d, want 200", i+1, w.Code)
		}
	}
}

func TestRateLimit_ResetsAfterWindow(t *testing.T) {
	rl, mr := newTestLimiter(t, 2)
	mw := middleware.RateLimit(rl)
	handler := mw(okHandler())

	// Exhaust limit
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, requestWithBearer("test-key"))
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, requestWithBearer("test-key"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}

	// Fast-forward miniredis clock by 61 seconds (past the 60s window)
	mr.FastForward(61e9) // nanoseconds

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, requestWithBearer("test-key"))
	if w.Code != http.StatusOK {
		t.Fatalf("after window reset: got %d, want 200", w.Code)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
