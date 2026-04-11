package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/knightlesssword/semaphore/internal/store"
)

const auditChannelSize = 512

// AuditStore is the storage interface required by AuditLogger.
// *store.PostgresStore satisfies this interface; tests may inject a fake.
type AuditStore interface {
	InsertRequest(ctx context.Context, rec store.AuditRecord) error
	UpsertSpend(ctx context.Context, apiKeyID string, day time.Time, promptTokens, completionTokens int) error
}

// AuditLogger asynchronously writes request audit records to Postgres.
// A background goroutine drains the channel; the middleware never blocks
// the client response waiting for a DB write.
type AuditLogger struct {
	store  AuditStore
	ch     chan store.AuditRecord
	logger *slog.Logger
}

// NewAuditLogger creates an AuditLogger backed by the given PostgresStore.
// Call Start before using the middleware.
func NewAuditLogger(s *store.PostgresStore, logger *slog.Logger) *AuditLogger {
	return newAuditLogger(s, logger)
}

// NewAuditLoggerWithStore creates an AuditLogger using any AuditStore implementation.
// Intended for testing with a fake store.
func NewAuditLoggerWithStore(s AuditStore, logger *slog.Logger) *AuditLogger {
	return newAuditLogger(s, logger)
}

func newAuditLogger(s AuditStore, logger *slog.Logger) *AuditLogger {
	al := &AuditLogger{
		store:  s,
		ch:     make(chan store.AuditRecord, auditChannelSize),
		logger: logger,
	}
	return al
}

// Start launches the background writer goroutine.  It stops when ctx is
// cancelled, draining any remaining records before returning.
func (al *AuditLogger) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case rec := <-al.ch:
				al.write(rec)
			case <-ctx.Done():
				// Drain in-flight records before exit.
				for len(al.ch) > 0 {
					al.write(<-al.ch)
				}
				return
			}
		}
	}()
}

func (al *AuditLogger) write(rec store.AuditRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := al.store.InsertRequest(ctx, rec); err != nil {
		al.logger.Warn("audit: failed to write request record", "err", err)
		return
	}

	// Only upsert spend when we have token data and a real key ID.
	if rec.APIKeyID != "" && (rec.PromptTokens > 0 || rec.CompletionTokens > 0) {
		if err := al.store.UpsertSpend(ctx, rec.APIKeyID, rec.CreatedAt, rec.PromptTokens, rec.CompletionTokens); err != nil {
			al.logger.Warn("audit: failed to upsert spend", "err", err)
		}
	}
}

// Audit returns a Middleware that captures every proxied request and queues
// an async audit record.  It should sit after Auth in the chain so that
// GetKeyID is available.
func Audit(al *AuditLogger, defaultProvider string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Sniff the request body for the model field, then restore it
			// so the proxy handler still sees the full body.
			model, providerHint := sniffRequest(r, defaultProvider)

			cw := &captureWriter{ResponseWriter: w}
			next.ServeHTTP(cw, r)

			latency := time.Since(start)
			promptTokens, completionTokens := cw.extractTokens()

			rec := store.AuditRecord{
				APIKeyID:         GetKeyID(r.Context()),
				RequestID:        GetRequestID(r.Context()),
				Provider:         providerHint,
				Model:            model,
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				LatencyMs:        latency.Milliseconds(),
				Status:           cw.statusCode(),
				CreatedAt:        start,
			}

			select {
			case al.ch <- rec:
			default:
				al.logger.Warn("audit: channel full, dropping record",
					"request_id", rec.RequestID,
				)
			}
		})
	}
}

// sniffRequest reads up to 4 KB of the request body to extract the model name
// and provider hint, then rewinds the body so the proxy handler receives it intact.
func sniffRequest(r *http.Request, defaultProvider string) (model, provider string) {
	provider = r.Header.Get("X-Provider")
	if provider == "" {
		provider = defaultProvider
	}

	if r.Body == nil {
		return "", provider
	}

	const maxSniff = 4096
	sniff, err := io.ReadAll(io.LimitReader(r.Body, maxSniff))
	r.Body.Close()

	// Always restore — even on error — so downstream handler isn't left with nil body.
	r.Body = io.NopCloser(bytes.NewReader(sniff))
	r.ContentLength = -1 // let the proxy re-measure after it transforms the body

	if err != nil || len(sniff) == 0 {
		return "", provider
	}

	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(sniff, &req) //nolint:errcheck — best-effort
	return req.Model, provider
}

// ── captureWriter ──────────────────────────────────────────────────────────

// captureWriter wraps http.ResponseWriter to capture the status code and,
// for non-streaming responses, the response body so we can extract token usage.
type captureWriter struct {
	http.ResponseWriter
	status        int
	buf           bytes.Buffer
	streaming     bool
	headerChecked bool
}

func (cw *captureWriter) WriteHeader(code int) {
	cw.status = code
	// By the time WriteHeader is called, Content-Type is already set in the
	// header map (httputil.ReverseProxy copies upstream headers first).
	if strings.Contains(cw.Header().Get("Content-Type"), "text/event-stream") {
		cw.streaming = true
	}
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *captureWriter) Write(b []byte) (int, error) {
	if !cw.headerChecked {
		cw.headerChecked = true
		// Implicit 200 path: WriteHeader was never called explicitly.
		if cw.status == 0 {
			cw.status = http.StatusOK
		}
		if strings.Contains(cw.Header().Get("Content-Type"), "text/event-stream") {
			cw.streaming = true
		}
	}
	if !cw.streaming {
		cw.buf.Write(b)
	}
	return cw.ResponseWriter.Write(b)
}

// Flush implements http.Flusher so SSE streaming works through this wrapper.
func (cw *captureWriter) Flush() {
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (cw *captureWriter) statusCode() int {
	if cw.status == 0 {
		return http.StatusOK
	}
	return cw.status
}

// extractTokens parses the buffered response body for OpenAI-format usage fields.
// Returns (0, 0) for streaming responses or when the body can't be parsed.
func (cw *captureWriter) extractTokens() (prompt, completion int) {
	if cw.streaming || cw.buf.Len() == 0 {
		return 0, 0
	}
	var resp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(cw.buf.Bytes(), &resp) //nolint:errcheck — best-effort
	return resp.Usage.PromptTokens, resp.Usage.CompletionTokens
}
