package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"net/http/httptest"
	"strings"
	"testing"
)

// End-to-end: Argo content with single-quoted embedded tool_use containing doubled backslashes
// converts to Anthropic blocks and streams input_json_delta that reconstructs
// to JSON where the "content" string has actual newlines (not literal \n).
func TestArgoEmbeddedBackslashes_ConvertAndStream(t *testing.T) {
	converter := newLegacyArgoTestConverter()

	// Simulate Argo response (post JSON-decoding), with single-quoted embedded tool_use
	embedded := "Intro:{'id': 'toolu_vrtx_01TsVcJnKKDvVtJdKqYCDKzr', 'input': {'content': 'package core\\n\\nimport (\\n\\t\"testing\"\\n)'}, 'name': 'Write', 'type': 'tool_use'}"
	argo := &ArgoChatResponse{Response: map[string]interface{}{
		"content":    embedded,
		"tool_calls": []interface{}{},
	}}

	req := &AnthropicRequest{Model: "claude-3-sonnet-20240229", Messages: []AnthropicMessage{{Role: core.RoleUser, Content: json.RawMessage(`"check"`)}}, Tools: []AnthropicTool{{Name: "Write"}}}
	anth := converter.ConvertArgoToAnthropicWithRequest(argo, req.Model, req)
	if anth == nil {
		t.Fatal("converter returned nil")
		return
	}

	// Ensure we have a tool_use block and that input.content decodes to real newlines
	var found bool
	for _, b := range anth.Content {
		if b.Type == "tool_use" && b.Name == "Write" {
			found = true
			if s, ok := b.Input["content"].(string); ok {
				if !strings.Contains(s, "\n\n") {
					t.Errorf("expected real newlines in content, got: %q", s)
				}
				if strings.Contains(s, "\\n") {
					t.Errorf("did not expect literal \\n in content, got: %q", s)
				}
			} else {
				t.Fatalf("content not string: %T", b.Input["content"])
			}
		}
	}
	if !found {
		t.Fatalf("tool_use Write not found; content: %+v", anth.Content)
	}

	// Now stream and reconstruct the input JSON from partial_json chunks
	rec := httptest.NewRecorder()
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(rec, anth.Model, ctx)
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler: %v", err)
	}
	srv := NewMinimalTestServer(t, &Config{})
	if err := srv.streamArgoResponseContent(ctx, anth, handler); err != nil {
		t.Fatalf("streamArgoResponseContent: %v", err)
	}

	// Parse SSE and extract partial_json chunks
	var chunks []string
	lines := strings.Split(rec.Body.String(), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var evt map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		if evt["type"] == "content_block_delta" {
			if d, ok := evt["delta"].(map[string]interface{}); ok && d["type"] == "input_json_delta" {
				if s, ok := d["partial_json"].(string); ok {
					chunks = append(chunks, s)
				}
			}
		}
	}
	reconstructed := strings.Join(chunks, "")
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(reconstructed), &parsed); err != nil {
		t.Fatalf("reconstructed JSON invalid: %v; json=%s", err, reconstructed)
	}
	if c, ok := parsed["content"].(string); ok {
		if !strings.Contains(c, "\n\n") {
			t.Errorf("expected real newlines after reconstruction, got: %q", c)
		}
		if strings.Contains(c, "\\n") {
			t.Errorf("unexpected literal \\n after reconstruction: %q", c)
		}
	} else {
		t.Fatalf("parsed content not a string: %T", parsed["content"])
	}
}
