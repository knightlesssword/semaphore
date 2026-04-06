package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// anthropicStreamTranslator wraps an Anthropic SSE response body and translates
// each event into OpenAI SSE format on-the-fly without buffering the whole stream.
//
// Anthropic SSE events → OpenAI SSE chunks mapping:
//   message_start      → first chunk: delta{role:"assistant", content:""}
//   content_block_delta (text_delta) → chunk: delta{content:"<text>"}
//   message_delta      → final chunk: delta{}, finish_reason set
//   message_stop       → "data: [DONE]"
//   ping / content_block_start / content_block_stop → skipped
type anthropicStreamTranslator struct {
	scanner *bufio.Scanner
	pending bytes.Buffer
	msgID   string
	model   string
	created int64
	eof     bool
	closer  io.Closer
}

func newAnthropicStreamTranslator(rc io.ReadCloser) *anthropicStreamTranslator {
	return &anthropicStreamTranslator{
		scanner: bufio.NewScanner(rc),
		msgID:   fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		created: time.Now().Unix(),
		closer:  rc,
	}
}

// Read satisfies io.Reader. It drains pending translated bytes; if empty it
// reads and translates the next Anthropic SSE event first.
func (t *anthropicStreamTranslator) Read(p []byte) (int, error) {
	for t.pending.Len() == 0 && !t.eof {
		if err := t.readNext(); err != nil {
			if err == io.EOF {
				t.eof = true
				break
			}
			return 0, err
		}
	}
	if t.pending.Len() == 0 {
		return 0, io.EOF
	}
	return t.pending.Read(p)
}

func (t *anthropicStreamTranslator) Close() error { return t.closer.Close() }

// readNext reads one complete Anthropic SSE event (lines until blank line)
// and appends the translated OpenAI SSE bytes to t.pending.
func (t *anthropicStreamTranslator) readNext() error {
	var eventType, dataLine string
	hasContent := false

	for t.scanner.Scan() {
		line := t.scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			dataLine = strings.TrimPrefix(line, "data: ")
			hasContent = true
		case line == "" && hasContent:
			return t.translate(eventType, dataLine)
		}
	}

	if err := t.scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (t *anthropicStreamTranslator) translate(eventType, data string) error {
	switch eventType {
	case "message_start":
		// Extract message ID and model; emit the role-announcing first chunk.
		var ev struct {
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err == nil {
			if ev.Message.ID != "" {
				t.msgID = "chatcmpl-" + ev.Message.ID
			}
			if ev.Message.Model != "" {
				t.model = ev.Message.Model
			}
		}
		t.emitChunk(map[string]any{"role": "assistant", "content": ""}, "")

	case "content_block_delta":
		var ev struct {
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil // skip malformed chunks rather than aborting the stream
		}
		if ev.Delta.Type == "text_delta" {
			t.emitChunk(map[string]any{"content": ev.Delta.Text}, "")
		}

	case "message_delta":
		var ev struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		t.emitChunk(map[string]any{}, mapStopReason(ev.Delta.StopReason))

	case "message_stop":
		t.pending.WriteString("data: [DONE]\n\n")

	// ping, content_block_start, content_block_stop, error → skip or fall through
	case "error":
		// Forward error as a best-effort OpenAI-style error chunk
		t.pending.WriteString("data: " + data + "\n\n")
	}

	return nil
}

// emitChunk serialises one OpenAI SSE chunk into t.pending.
// finishReason is "" for mid-stream chunks.
func (t *anthropicStreamTranslator) emitChunk(delta map[string]any, finishReason string) {
	var fr any = nil
	if finishReason != "" {
		fr = finishReason
	}

	chunk := map[string]any{
		"id":      t.msgID,
		"object":  "chat.completion.chunk",
		"created": t.created,
		"model":   t.model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": fr,
		}},
	}

	b, _ := json.Marshal(chunk)
	t.pending.WriteString("data: ")
	t.pending.Write(b)
	t.pending.WriteString("\n\n")
}
