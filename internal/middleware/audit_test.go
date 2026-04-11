package middleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/knightlesssword/semaphore/internal/middleware"
	"github.com/knightlesssword/semaphore/internal/store"
)

// fakeStore records calls to InsertRequest and UpsertSpend without touching a real DB.
type fakeStore struct {
	records []store.AuditRecord
	spends  []spendCall
}

type spendCall struct {
	keyID            string
	promptTokens     int
	completionTokens int
}

func (f *fakeStore) InsertRequest(_ context.Context, rec store.AuditRecord) error {
	f.records = append(f.records, rec)
	return nil
}

func (f *fakeStore) UpsertSpend(_ context.Context, apiKeyID string, _ time.Time, pt, ct int) error {
	f.spends = append(f.spends, spendCall{keyID: apiKeyID, promptTokens: pt, completionTokens: ct})
	return nil
}

// fakeAuditLogger is a minimal AuditLogger that uses the fakeStore synchronously.
// We bypass the channel so tests don't need to sleep.
type fakeAuditLogger struct {
	store  *fakeStore
	logger interface{ Warn(string, ...any) }
}

// Since AuditLogger is unexported-field based, we test the behaviour through the
// full public API: NewAuditLogger + Audit middleware on a real handler.

func TestAudit_RecordsStatusAndLatency(t *testing.T) {
	fs := &fakeStore{}
	al := newSyncAuditLogger(fs)
	mw := middleware.Audit(al, "openai")

	body := `{"model":"gpt-4","messages":[]}`
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}

	// Give the async goroutine a moment to flush.
	time.Sleep(20 * time.Millisecond)

	if len(fs.records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(fs.records))
	}
	rec := fs.records[0]
	if rec.Status != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Status)
	}
	if rec.LatencyMs < 0 {
		t.Error("latency should be non-negative")
	}
	if rec.Model != "gpt-4" {
		t.Errorf("model: got %q, want %q", rec.Model, "gpt-4")
	}
	if rec.PromptTokens != 10 {
		t.Errorf("prompt_tokens: got %d, want 10", rec.PromptTokens)
	}
	if rec.CompletionTokens != 5 {
		t.Errorf("completion_tokens: got %d, want 5", rec.CompletionTokens)
	}
}

func TestAudit_SkipsTokenExtractionForStreaming(t *testing.T) {
	fs := &fakeStore{}
	al := newSyncAuditLogger(fs)
	mw := middleware.Audit(al, "anthropic")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"claude-3","stream":true,"messages":[]}`))
	handler.ServeHTTP(w, r)

	time.Sleep(20 * time.Millisecond)

	if len(fs.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(fs.records))
	}
	rec := fs.records[0]
	if rec.PromptTokens != 0 || rec.CompletionTokens != 0 {
		t.Errorf("expected 0 tokens for streaming, got prompt=%d completion=%d",
			rec.PromptTokens, rec.CompletionTokens)
	}
}

func TestAudit_ProviderFromHeader(t *testing.T) {
	fs := &fakeStore{}
	al := newSyncAuditLogger(fs)
	mw := middleware.Audit(al, "openai")

	handler := mw(okHandler())

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[]}`))
	r.Header.Set("X-Provider", "anthropic")
	handler.ServeHTTP(w, r)

	time.Sleep(20 * time.Millisecond)

	if len(fs.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(fs.records))
	}
	if fs.records[0].Provider != "anthropic" {
		t.Errorf("provider: got %q, want %q", fs.records[0].Provider, "anthropic")
	}
}

// newSyncAuditLogger returns an AuditLogger backed by the fakeStore.
// The background goroutine is started immediately so the 20 ms sleep
// in each test is enough for the record to land.
func newSyncAuditLogger(fs *fakeStore) *middleware.AuditLogger {
	al := middleware.NewAuditLoggerWithStore(fs, noopLogger())
	al.Start(context.Background())
	return al
}
