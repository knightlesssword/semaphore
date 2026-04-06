package providers

import (
	"encoding/json"
	"io"
	"net/http"
)

// Ollama exposes an OpenAI-compatible endpoint (/v1/chat/completions) since v0.1.24.
// We use that so format translation is unnecessary — the only difference from OpenAI
// is that there's no authentication and we may need to override the model name.
type Ollama struct {
	defaultModel string
}

func NewOllama(defaultModel string) *Ollama {
	return &Ollama{defaultModel: defaultModel}
}

func (p *Ollama) Name() string { return "ollama" }
func (p *Ollama) Path() string { return "/v1/chat/completions" }

func (p *Ollama) SetAuth(req *http.Request, _ string) {
	req.Header.Del("Authorization") // local deployment, no auth
}

func (p *Ollama) TransformRequestBody(body []byte) ([]byte, error) {
	if p.defaultModel == "" {
		return body, nil // use whatever model the client sent
	}
	// Patch just the "model" key without re-marshalling the whole request.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	modelJSON, _ := json.Marshal(p.defaultModel)
	m["model"] = modelJSON
	return json.Marshal(m)
}

func (p *Ollama) TransformResponseBody(body []byte) ([]byte, error) { return body, nil }
func (p *Ollama) WrapStreamBody(body io.ReadCloser) io.ReadCloser   { return body }
