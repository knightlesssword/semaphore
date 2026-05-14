package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/knightlesssword/semaphore/internal/config"
	"github.com/knightlesssword/semaphore/internal/middleware"
	"github.com/knightlesssword/semaphore/internal/proxy/providers"
)

type contextKey string

const (
	ctxProvider  contextKey = "provider"
	ctxStreaming contextKey = "streaming"
)

// Handler is an http.Handler that proxies OpenAI-format requests to the
// configured upstream provider, performing format translation as needed.
type Handler struct {
	cfg      *config.Config
	registry *providers.Registry
	logger   *slog.Logger
}

func NewHandler(cfg *config.Config, logger *slog.Logger) *Handler {
	return &Handler{
		cfg:      cfg,
		registry: providers.NewRegistry(cfg),
		logger:   logger,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Read the body once — we need it before handing off to ReverseProxy.
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		jsonError(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// 2. Detect streaming so ModifyResponse knows what to do.
	var chatReq struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &chatReq) //nolint:errcheck — malformed JSON handled by provider transform

	// 3. Select provider: X-Provider header → config default.
	providerName := r.Header.Get("X-Provider")
	if providerName == "" {
		providerName = h.cfg.Proxy.DefaultProvider
	}

	p := h.registry.Get(providerName)
	if p == nil {
		jsonError(w, fmt.Sprintf("unknown provider %q", providerName), http.StatusBadRequest)
		return
	}

	provCfg := h.cfg.Proxy.Providers[providerName]

	// 4. Transform the request body to provider format.
	transformed, err := p.TransformRequestBody(body)
	if err != nil {
		h.logger.Error("request transform failed", "provider", providerName, "err", err)
		jsonError(w, "request transformation failed", http.StatusInternalServerError)
		return
	}

	// 5. Restore the (now transformed) body on the request.
	r.Body = io.NopCloser(bytes.NewReader(transformed))
	r.ContentLength = int64(len(transformed))
	r.Header.Set("Content-Type", "application/json")

	// 6. Stash provider + streaming flag in context for ModifyResponse.
	ctx := context.WithValue(r.Context(), ctxProvider, p)
	ctx = context.WithValue(ctx, ctxStreaming, chatReq.Stream)

	// 7. Resolve the upstream base URL.
	baseURL, err := url.Parse(provCfg.BaseURL)
	if err != nil {
		jsonError(w, "invalid provider base URL", http.StatusInternalServerError)
		return
	}

	// 8. Build and run the reverse proxy.
	//
	// Director runs just before the upstream request is sent. At that point the
	// body is already set correctly (step 5), so Director only handles URL and headers.
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = baseURL.Scheme
			req.URL.Host = baseURL.Host
			req.URL.Path = p.Path()
			req.URL.RawQuery = ""
			req.Host = baseURL.Host

			p.SetAuth(req, provCfg.APIKey)
			req.Header.Del("X-Provider")
		},
		ModifyResponse: h.modifyResponse,
		ErrorHandler:   h.errorHandler,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	rp.ServeHTTP(w, r.WithContext(ctx))
}

// modifyResponse is called after the upstream response headers arrive but
// before the body is forwarded to the client.
func (h *Handler) modifyResponse(resp *http.Response) error {
	ctx := resp.Request.Context()
	p := ctx.Value(ctxProvider).(providers.Provider)
	streaming := ctx.Value(ctxStreaming).(bool)

	if streaming {
		// Wrap the body with a per-provider SSE translator.
		// httputil.ReverseProxy will io.Copy this to the client, so translation
		// happens chunk-by-chunk without buffering the full stream.
		resp.Body = p.WrapStreamBody(resp.Body)
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Header.Set("Cache-Control", "no-cache")
		resp.Header.Set("X-Accel-Buffering", "no") // disable nginx buffering
		resp.ContentLength = -1                     // unknown; streaming
		return nil
	}

	// Non-streaming: buffer, transform, replace.
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("reading upstream response: %w", err)
	}

	// Only translate successful responses; pass provider errors through as-is.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		translated, err := p.TransformResponseBody(body)
		if err != nil {
			h.logger.Error("response transform failed", "err", err)
			// Fall back to the raw body rather than returning a 502.
		} else {
			body = translated
		}
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return nil
}

func (h *Handler) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.Error("upstream error", "err", err, "path", r.URL.Path)
	jsonProviderError(w, "upstream request failed", http.StatusBadGateway)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	middleware.WriteError(w, msg, code, middleware.SourceSemaphore)
}

func jsonProviderError(w http.ResponseWriter, msg string, code int) {
	middleware.WriteError(w, msg, code, middleware.SourceProvider)
}
