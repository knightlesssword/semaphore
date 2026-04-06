package providers

import (
	"io"
	"net/http"

	"github.com/knightlesssword/semaphore/internal/config"
)

// Provider abstracts a single upstream LLM provider.
// All methods operate on OpenAI-format data at the boundary — providers that
// use a different wire format (Anthropic) implement the Transform methods to
// convert in both directions.
type Provider interface {
	Name() string
	// Path is the upstream endpoint for chat completions (e.g. "/v1/chat/completions")
	Path() string
	// SetAuth injects the provider's authentication headers into the outgoing request.
	SetAuth(req *http.Request, apiKey string)
	// TransformRequestBody converts an OpenAI-format request body to the provider's format.
	// For providers that already speak OpenAI (OpenAI, Ollama), this is a no-op.
	TransformRequestBody(body []byte) ([]byte, error)
	// TransformResponseBody converts a non-streaming provider response to OpenAI format.
	TransformResponseBody(body []byte) ([]byte, error)
	// WrapStreamBody wraps the upstream SSE response body, translating chunks to
	// OpenAI SSE format on-the-fly. For pass-through providers this returns body unchanged.
	WrapStreamBody(body io.ReadCloser) io.ReadCloser
}

// Registry holds all configured providers, keyed by name.
type Registry struct {
	providers map[string]Provider
}

func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{providers: make(map[string]Provider)}

	for name, pcfg := range cfg.Proxy.Providers {
		switch name {
		case "openai":
			r.providers[name] = NewOpenAI()
		case "anthropic":
			r.providers[name] = NewAnthropic(pcfg.DefaultModel)
		case "ollama":
			r.providers[name] = NewOllama(pcfg.DefaultModel)
		}
	}

	return r
}

// Get returns the provider for the given name, or nil if not configured.
func (r *Registry) Get(name string) Provider {
	return r.providers[name]
}
