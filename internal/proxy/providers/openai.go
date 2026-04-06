package providers

import (
	"io"
	"net/http"
)

// OpenAI speaks the native OpenAI format, so all transforms are pass-throughs.
type OpenAI struct{}

func NewOpenAI() *OpenAI { return &OpenAI{} }

func (p *OpenAI) Name() string { return "openai" }
func (p *OpenAI) Path() string { return "/v1/chat/completions" }

func (p *OpenAI) SetAuth(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

func (p *OpenAI) TransformRequestBody(body []byte) ([]byte, error)  { return body, nil }
func (p *OpenAI) TransformResponseBody(body []byte) ([]byte, error) { return body, nil }
func (p *OpenAI) WrapStreamBody(body io.ReadCloser) io.ReadCloser   { return body }
