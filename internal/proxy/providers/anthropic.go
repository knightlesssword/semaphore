package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Anthropic translates between OpenAI format and Anthropic's Messages API.
type Anthropic struct {
	defaultModel string
}

func NewAnthropic(defaultModel string) *Anthropic {
	return &Anthropic{defaultModel: defaultModel}
}

func (p *Anthropic) Name() string { return "anthropic" }
func (p *Anthropic) Path() string { return "/v1/messages" }

func (p *Anthropic) SetAuth(req *http.Request, apiKey string) {
	req.Header.Del("Authorization")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

// ── Request translation ────────────────────────────────────────────────────

type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// oaiRequest is the OpenAI chat completions request the client sends.
type oaiRequest struct {
	Model       string      `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
	Stream      bool         `json:"stream,omitempty"`
}

// anthropicRequest is what we send upstream.
type anthropicRequest struct {
	Model       string       `json:"model"`
	System      string       `json:"system,omitempty"`
	Messages    []oaiMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens"`
	Temperature *float64     `json:"temperature,omitempty"`
	Stream      bool         `json:"stream,omitempty"`
}

func (p *Anthropic) TransformRequestBody(body []byte) ([]byte, error) {
	var req oaiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("anthropic: parse request: %w", err)
	}

	model := req.Model
	if p.defaultModel != "" {
		model = p.defaultModel
	}

	// Extract top-level system prompt; keep only user/assistant messages.
	var system string
	var msgs []oaiMessage
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			msgs = append(msgs, m)
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096 // Anthropic requires this field
	}

	out := anthropicRequest{
		Model:       model,
		System:      system,
		Messages:    msgs,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
	}

	return json.Marshal(out)
}

// ── Response translation (non-streaming) ──────────────────────────────────

type anthropicResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type oaiResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []oaiChoice `json:"choices"`
	Usage   oaiUsage    `json:"usage"`
}

type oaiChoice struct {
	Index        int        `json:"index"`
	Message      oaiMessage `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (p *Anthropic) TransformResponseBody(body []byte) ([]byte, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("anthropic: parse response: %w", err)
	}

	text := ""
	if len(resp.Content) > 0 {
		text = resp.Content[0].Text
	}

	out := oaiResponse{
		ID:      "chatcmpl-" + resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []oaiChoice{{
			Index:        0,
			Message:      oaiMessage{Role: "assistant", Content: text},
			FinishReason: mapStopReason(resp.StopReason),
		}},
		Usage: oaiUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	return json.Marshal(out)
}

// mapStopReason converts Anthropic stop reasons to OpenAI finish reasons.
func mapStopReason(r string) string {
	switch r {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return r
	}
}

// ── Streaming ──────────────────────────────────────────────────────────────

func (p *Anthropic) WrapStreamBody(body io.ReadCloser) io.ReadCloser {
	return newAnthropicStreamTranslator(body)
}
