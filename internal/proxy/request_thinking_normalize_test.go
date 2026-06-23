package proxy

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// blockField returns the raw JSON value for key in a content block, or false
// when the key is absent.
func blockField(t *testing.T, block json.RawMessage, key string) (string, bool) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(block, &fields); err != nil {
		t.Fatalf("unmarshal block %s: %v", block, err)
	}
	v, ok := fields[key]
	return string(v), ok
}

func contentBlocks(t *testing.T, content json.RawMessage) []json.RawMessage {
	t.Helper()
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		t.Fatalf("unmarshal content blocks %s: %v", content, err)
	}
	return blocks
}

func TestNormalizeAnthropicThinkingBlocks(t *testing.T) {
	t.Run("inserts empty thinking and preserves other fields", func(t *testing.T) {
		messages := []AnthropicMessage{{
			Role:    "assistant",
			Content: json.RawMessage(`[{"signature":"SIG","type":"thinking"},{"type":"text","text":"hi"}]`),
		}}

		normalizeAnthropicThinkingBlocks(context.Background(), messages)

		blocks := contentBlocks(t, messages[0].Content)
		if len(blocks) != 2 {
			t.Fatalf("blocks = %d, want 2", len(blocks))
		}

		thinking, ok := blockField(t, blocks[0], "thinking")
		if !ok {
			t.Fatalf("thinking field not inserted: %s", blocks[0])
		}
		if thinking != `""` {
			t.Fatalf("thinking = %s, want empty string", thinking)
		}
		if sig, _ := blockField(t, blocks[0], "signature"); sig != `"SIG"` {
			t.Fatalf("signature = %s, want \"SIG\" (not preserved)", sig)
		}
		if typ, _ := blockField(t, blocks[0], "type"); typ != `"thinking"` {
			t.Fatalf("type = %s, want \"thinking\"", typ)
		}

		// The text block must be untouched.
		if text, _ := blockField(t, blocks[1], "text"); text != `"hi"` {
			t.Fatalf("text block changed: %s", blocks[1])
		}
	})

	t.Run("leaves existing thinking value unchanged", func(t *testing.T) {
		original := json.RawMessage(`[{"type":"thinking","thinking":"already here","signature":"SIG"}]`)
		messages := []AnthropicMessage{{Role: "assistant", Content: original}}

		normalizeAnthropicThinkingBlocks(context.Background(), messages)

		if string(messages[0].Content) != string(original) {
			t.Fatalf("content changed: got %s want %s", messages[0].Content, original)
		}
	})

	t.Run("leaves redacted_thinking untouched", func(t *testing.T) {
		original := json.RawMessage(`[{"type":"redacted_thinking","data":"ENC"}]`)
		messages := []AnthropicMessage{{Role: "assistant", Content: original}}

		normalizeAnthropicThinkingBlocks(context.Background(), messages)

		if string(messages[0].Content) != string(original) {
			t.Fatalf("redacted_thinking changed: got %s want %s", messages[0].Content, original)
		}
		if _, ok := blockField(t, contentBlocks(t, messages[0].Content)[0], "thinking"); ok {
			t.Fatalf("thinking key wrongly added to redacted_thinking: %s", messages[0].Content)
		}
	})

	t.Run("leaves string content untouched", func(t *testing.T) {
		original := json.RawMessage(`"plain text"`)
		messages := []AnthropicMessage{{Role: "user", Content: original}}

		normalizeAnthropicThinkingBlocks(context.Background(), messages)

		if string(messages[0].Content) != string(original) {
			t.Fatalf("string content changed: got %s", messages[0].Content)
		}
	})

	t.Run("handles single-object content", func(t *testing.T) {
		messages := []AnthropicMessage{{
			Role:    "assistant",
			Content: json.RawMessage(`{"type":"thinking","signature":"SIG"}`),
		}}

		normalizeAnthropicThinkingBlocks(context.Background(), messages)

		thinking, ok := blockField(t, messages[0].Content, "thinking")
		if !ok || thinking != `""` {
			t.Fatalf("single-object thinking not normalized: %s", messages[0].Content)
		}
	})

	t.Run("normalizes multiple thinking blocks across messages", func(t *testing.T) {
		messages := []AnthropicMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","signature":"A"}]`)},
			{Role: "user", Content: json.RawMessage(`"q"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","signature":"B"},{"type":"thinking","signature":"C"}]`)},
		}

		normalizeAnthropicThinkingBlocks(context.Background(), messages)

		for _, idx := range []int{0, 2} {
			for _, block := range contentBlocks(t, messages[idx].Content) {
				if _, ok := blockField(t, block, "thinking"); !ok {
					t.Fatalf("message %d block missing thinking: %s", idx, block)
				}
			}
		}
	})
}

// TestNormalizeAnthropicThinkingBlocksLogsWarn verifies a WARN log is emitted
// for each inserted "thinking" field, and that no warning fires when there is
// nothing to repair.
func TestNormalizeAnthropicThinkingBlocksLogsWarn(t *testing.T) {
	t.Run("warns on each insertion", func(t *testing.T) {
		logs := captureWarnLogs(t, func() {
			messages := []AnthropicMessage{
				{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","signature":"A"},{"type":"thinking","signature":"B"}]`)},
			}
			normalizeAnthropicThinkingBlocks(context.Background(), messages)
		})

		if got := strings.Count(logs, `Inserted empty "thinking" field`); got != 2 {
			t.Fatalf("WARN log count = %d, want 2\nlogs:\n%s", got, logs)
		}
		if !strings.Contains(logs, "message[0] content block[0]") || !strings.Contains(logs, "message[0] content block[1]") {
			t.Fatalf("WARN logs missing block indices:\n%s", logs)
		}
	})

	t.Run("no warning when nothing to repair", func(t *testing.T) {
		logs := captureWarnLogs(t, func() {
			messages := []AnthropicMessage{
				{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"x","signature":"A"},{"type":"text","text":"hi"}]`)},
			}
			normalizeAnthropicThinkingBlocks(context.Background(), messages)
		})

		if strings.Contains(logs, `Inserted empty "thinking" field`) {
			t.Fatalf("unexpected WARN log:\n%s", logs)
		}
	})
}

// TestParseAnthropicRequestInsertsThinkingField verifies the normalization runs
// as part of request parsing so every downstream forwarding path sees the
// repaired thinking block.
func TestParseAnthropicRequestInsertsThinkingField(t *testing.T) {
	SetupTestLogger(t)

	config := &Config{
		Provider:       constants.ProviderAnthropic,
		ProviderKeySet: ProviderKeySet{AnthropicAPIKey: "k"},
	}
	s := NewMinimalTestServer(t, config)

	body := `{"model":"claude-opus-4-7","max_tokens":10,"messages":[{"role":"assistant","content":[{"signature":"SIG","type":"thinking"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))

	parsed, err := s.parseAnthropicRequest(req)
	if err != nil {
		t.Fatalf("parseAnthropicRequest: %v", err)
	}

	thinking, ok := blockField(t, contentBlocks(t, parsed.Messages[0].Content)[0], "thinking")
	if !ok || thinking != `""` {
		t.Fatalf("parsed request thinking not normalized: %s", parsed.Messages[0].Content)
	}
}

// TestForwardedAnthropicRequestHasThinkingField is an end-to-end check: a client
// request with a thinking block missing the "thinking" key must reach the
// upstream Anthropic provider with "thinking":"" inserted.
func TestForwardedAnthropicRequestHasThinkingField(t *testing.T) {
	SetupTestLogger(t)

	var capturedBody []byte
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		resp := AnthropicResponse{
			ID:      "msg_1",
			Type:    "message",
			Role:    "assistant",
			Model:   "claude-opus-4-7",
			Content: []AnthropicContentBlock{{Type: "text", Text: "ok"}},
			Usage:   &AnthropicUsage{InputTokens: 1, OutputTokens: 1},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	config := &Config{
		Provider:       constants.ProviderAnthropic,
		ProviderKeySet: ProviderKeySet{AnthropicAPIKey: "test-anthropic-key"},
		ProviderURL:    mockServer.URL,
	}
	s := &Server{
		config:    config,
		endpoints: &Endpoints{Anthropic: mockServer.URL},
		mapper:    NewModelMapper(config),
		client:    retry.NewClient(5*time.Second, logger.GetLogger()),
	}

	body := `{"model":"claude-opus-4-7","max_tokens":10,"messages":[{"role":"assistant","content":[{"signature":"SIG","type":"thinking"},{"type":"text","text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()

	s.handleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(string(capturedBody), `"thinking":""`) {
		t.Fatalf("forwarded body missing inserted thinking field:\n%s", capturedBody)
	}
	if !strings.Contains(string(capturedBody), `"signature":"SIG"`) {
		t.Fatalf("forwarded body dropped signature:\n%s", capturedBody)
	}
}
