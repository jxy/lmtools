package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSendDoneNoOp tests that SendDone doesn't send [DONE] for Anthropic format
func TestSendDoneNoOp(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Call SendDone
	err = handler.SendDone()
	if err != nil {
		t.Fatalf("SendDone returned error: %v", err)
	}

	// Check that nothing was written
	body := recorder.Body.String()
	if strings.Contains(body, "[DONE]") {
		t.Error("SendDone should not send [DONE] for Anthropic format")
	}
	if body != "" {
		t.Errorf("SendDone should not write anything, but got: %s", body)
	}
}

// TestToolUseStreamingFormat tests that tool_use blocks have proper format
func TestToolUseStreamingFormat(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Send tool use start
	err = handler.SendToolUseStart(0, "toolu_123", "get_weather")
	if err != nil {
		t.Fatalf("SendToolUseStart failed: %v", err)
	}

	// Send tool input delta (should start with empty string)
	err = handler.SendToolInputDelta(0, "")
	if err != nil {
		t.Fatalf("SendToolInputDelta (empty) failed: %v", err)
	}

	// Send actual JSON content
	err = handler.SendToolInputDelta(0, "{\"location\": \"NYC\"}")
	if err != nil {
		t.Fatalf("SendToolInputDelta failed: %v", err)
	}

	// Parse the output
	body := recorder.Body.String()
	events := parseSimpleSSEEvents(body)

	// Verify the tool_use block has input field
	var foundInputField bool
	var foundEmptyDelta bool

	for i, event := range events {
		if event.Event == "content_block_start" && strings.Contains(event.Data, "tool_use") {
			// Check for empty input field
			if strings.Contains(event.Data, "\"input\":{}") {
				foundInputField = true
			}
		}
		if event.Event == "content_block_delta" && i == 1 {
			// First delta should be empty
			if strings.Contains(event.Data, "\"partial_json\":\"\"") {
				foundEmptyDelta = true
			}
		}
	}

	if !foundInputField {
		t.Error("Tool use content_block_start should have empty input field")
	}
	if !foundEmptyDelta {
		t.Error("First tool delta should have empty partial_json")
	}
}

// TestStreamToolBlockWithServer tests the streamToolBlock function
func TestStreamToolBlockWithServer(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Create server
	server := &Server{
		config: &Config{},
	}

	// Create a tool block
	toolBlock := AnthropicContentBlock{
		Type: "tool_use",
		ID:   "toolu_test",
		Name: "calculator",
		Input: map[string]interface{}{
			"operation": "add",
			"a":         5,
			"b":         3,
		},
	}

	// Stream the tool block
	err = server.streamToolBlock(ctx, toolBlock, 0, handler)
	if err != nil {
		t.Fatalf("streamToolBlock failed: %v", err)
	}

	// Parse events
	body := recorder.Body.String()
	events := parseSimpleSSEEvents(body)

	// Validate
	if len(events) < 3 {
		t.Fatalf("Expected at least 3 events, got %d", len(events))
	}

	// First should be content_block_start with empty input
	if events[0].Event != "content_block_start" {
		t.Error("First event should be content_block_start")
	}

	var startData ContentBlockStartEvent
	if err := json.Unmarshal([]byte(events[0].Data), &startData); err == nil {
		// Check that the content block has the expected fields
		if startData.ContentBlock.Input == nil {
			t.Error("input field should not be nil")
		}
		// Input should be a non-nil map/object for tool_use blocks
	}

	// Second should be empty delta; parse JSON and verify
	if events[1].Event != "content_block_delta" {
		t.Error("Second event should be content_block_delta")
	}
	var deltaEvt ContentBlockDeltaEvent
	if err := json.Unmarshal([]byte(events[1].Data), &deltaEvt); err != nil {
		t.Fatalf("Failed to unmarshal second event: %v", err)
	}
	if deltaEvt.Delta.Type != "input_json_delta" {
		t.Errorf("Expected delta type input_json_delta, got %q", deltaEvt.Delta.Type)
	}
	if deltaEvt.Delta.PartialJSON == nil || *deltaEvt.Delta.PartialJSON != "" {
		t.Error("First delta should have empty partial_json")
	}

	// Last should be content_block_stop
	if events[len(events)-1].Event != "content_block_stop" {
		t.Error("Last event should be content_block_stop")
	}
}

// TestCompleteWithoutDone tests that Complete doesn't send [DONE]
func TestCompleteWithoutDone(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Send message start
	err = handler.SendMessageStart()
	if err != nil {
		t.Fatalf("SendMessageStart failed: %v", err)
	}

	// Complete the stream
	err = handler.Complete("end_turn")
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	// Check output
	body := recorder.Body.String()
	if strings.Contains(body, "[DONE]") {
		t.Error("Complete should not send [DONE] for Anthropic format")
	}

	// Should have message_stop
	if !strings.Contains(body, "message_stop") {
		t.Error("Complete should send message_stop")
	}
}

// New test: verify Anthropic simulated streaming never splits input_json_delta at a backslash escape
func TestAnthropicStreaming_NoSplitInPartialJSON(t *testing.T) {
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLevel("debug"),
		logger.WithFormat("text"),
		logger.WithStderr(true),
		logger.WithFile(false),
	); err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Prepare Argo response with tool_use input containing various escapes
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ArgoChatResponse{
			Response: map[string]interface{}{
				"content": "Beginning tool call...\n",
				"tool_calls": []map[string]interface{}{
					{
						"id":   "toolu_vrtx_01TESTANTH",
						"type": "function",
						"function": map[string]interface{}{
							"name": "Edit",
							// Anthropic expects JSON object for tool input
							"arguments": map[string]interface{}{
								"file_path":  "/path/with spaces/and\\slashes.txt",
								"new_string": "line1\nline2 with tab\t and quote \" and backslash \\",
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("Failed to encode response: %v", err)
		}
	}))
	defer mockArgo.Close()

	// Server config
	config := &Config{
		ArgoUser:     "testuser",
		ArgoEnv:      mockArgo.URL,
		ArgoBaseURL:  mockArgo.URL,
		PingInterval: 1 * time.Second,
	}
	mapper := NewModelMapper(config)
	server := &Server{
		config:    config,
		mapper:    mapper,
		converter: NewConverter(mapper),
		client:    retry.NewClient(10*time.Minute, logger.GetLogger()),
	}

	// Prepare SSE recorder and handler
	recorder := httptest.NewRecorder()
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-opus-4-1-20250805", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Anthropic-style request routed to Argo with tools (forces simulated streaming)
	anthReq := &AnthropicRequest{
		Model:  "claude-opus-4-1-20250805",
		Stream: true,
		Tools:  []AnthropicTool{{Name: "Edit"}},
		Messages: []AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"Do edit"`),
		}},
		MaxTokens: 100,
	}

	// Execute simulated streaming with pings
	if err := server.streamFromArgo(context.Background(), anthReq, handler); err != nil {
		t.Fatalf("streamFromArgo failed: %v", err)
	}

	// Verify SSE output contains no partial_json splits at dangling escapes
	output := recorder.Body.String()
	lines := strings.Split(output, "\n")
	var partials []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var evt ContentBlockDeltaEvent
		if err := json.Unmarshal([]byte(data), &evt); err == nil {
			if evt.Delta.Type == "input_json_delta" && evt.Delta.PartialJSON != nil {
				partials = append(partials, *evt.Delta.PartialJSON)
			}
		}
	}

	if len(partials) == 0 {
		t.Fatalf("No input_json_delta partials found in output")
	}

	// Check each partial for invalid ends
	for i, p := range partials {
		if len(p) == 0 {
			continue
		}
		if p[len(p)-1] == '\\' {
			t.Fatalf("Partial %d ends with dangling backslash: %q", i, p)
		}
	}

	// Reconstruct full input and ensure valid JSON
	full := strings.Join(partials, "")
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(full), &parsed); err != nil {
		t.Fatalf("Concatenated partial_json did not parse: %v\nJSON: %s", err, full)
	}
}

// TestAnthropicStreamingExampleFormat reproduces the documented Anthropic streaming example with tool calls
func TestAnthropicStreamingExampleFormat(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	model := "claude-sonnet-4-5-20250929"
	handler, err := NewAnthropicStreamHandler(recorder, model, ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	handler.state.InputTokens = 472
	handler.state.OutputTokens = 2

	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("SendMessageStart failed: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("SendContentBlockStart(text) failed: %v", err)
	}
	if err := handler.SendPing(); err != nil {
		t.Fatalf("SendPing failed: %v", err)
	}

	chunks := []string{"Okay", ",", " let", "'s", " check", " the", " weather", " for", " San", " Francisco", ",", " CA", ":"}
	for _, c := range chunks {
		if err := handler.SendTextDelta(c); err != nil {
			t.Fatalf("SendTextDelta %q failed: %v", c, err)
		}
	}

	if err := handler.SendContentBlockStop(0); err != nil {
		t.Fatalf("SendContentBlockStop(text) failed: %v", err)
	}

	toolID := "toolu_01T1x1fJ34qAmk2tNTrN7Up6"
	if err := handler.SendToolUseStart(1, toolID, "get_weather"); err != nil {
		t.Fatalf("SendToolUseStart failed: %v", err)
	}

	jsonChunks := []string{
		"",
		`{"location":`,
		` "San`,
		` Francisc`,
		`o,`,
		` CA"`,
		`, `,
		`"unit": "fah`,
		`renheit"}`,
	}
	for _, jc := range jsonChunks {
		if err := handler.SendToolInputDelta(1, jc); err != nil {
			t.Fatalf("SendToolInputDelta %q failed: %v", jc, err)
		}
	}

	if err := handler.SendContentBlockStop(1); err != nil {
		t.Fatalf("SendContentBlockStop(tool) failed: %v", err)
	}

	handler.state.OutputTokens = 89
	// Use FinishStream instead of manual sequence
	if err := handler.FinishStream("tool_use", &AnthropicUsage{
		InputTokens:  0,
		OutputTokens: handler.state.OutputTokens,
	}); err != nil {
		t.Fatalf("FinishStream failed: %v", err)
	}

	out := recorder.Body.String()
	events := parseSimpleSSEEvents(out)

	var (
		sawMessageStart   bool
		sawTextBlockStart bool
		textDeltas        []string
		sawToolStart      bool
		sawEmptyToolDelta bool
		sawToolStop       bool
		sawMessageDelta   bool
		sawMessageStop    bool
	)

	for _, e := range events {
		switch e.Event {
		case "message_start":
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(e.Data), &m); err != nil {
				t.Fatalf("unmarshal message_start: %v", err)
			}
			msg, _ := m["message"].(map[string]interface{})
			if msg == nil {
				t.Fatalf("message_start missing message")
			}
			if gotModel, _ := msg["model"].(string); gotModel != model {
				t.Errorf("expected model %s, got %s", model, gotModel)
			}
			if usage, _ := msg["usage"].(map[string]interface{}); usage != nil {
				if it, ok := usage["input_tokens"].(float64); !ok || int(it) != 472 {
					t.Errorf("expected input_tokens 472, got %v", usage["input_tokens"])
				}
			} else {
				t.Errorf("message_start missing usage")
			}
			sawMessageStart = true

		case "content_block_start":
			var cbs map[string]interface{}
			if err := json.Unmarshal([]byte(e.Data), &cbs); err != nil {
				t.Fatalf("unmarshal content_block_start: %v", err)
			}
			cb, _ := cbs["content_block"].(map[string]interface{})
			if cb == nil {
				t.Fatalf("content_block_start missing content_block")
			}
			if typ, _ := cb["type"].(string); typ == "text" {
				// Require explicit empty text field
				if txt, ok := cb["text"].(string); !ok || txt != "" {
					t.Errorf("text block should include empty text field, got %v", cb["text"])
				}
				sawTextBlockStart = true
			} else if typ == "tool_use" {
				if id, _ := cb["id"].(string); id != toolID {
					t.Errorf("unexpected tool id: %v", id)
				}
				if name, _ := cb["name"].(string); name != "get_weather" {
					t.Errorf("unexpected tool name: %v", name)
				}
				if in, ok := cb["input"].(map[string]interface{}); !ok || in == nil {
					t.Errorf("tool_use should include input object, got %T", cb["input"])
				}
				sawToolStart = true
			}

		case "content_block_delta":
			var delta ContentBlockDeltaEvent
			if err := json.Unmarshal([]byte(e.Data), &delta); err != nil {
				t.Fatalf("unmarshal content_block_delta: %v", err)
			}
			switch delta.Delta.Type {
			case "text_delta":
				textDeltas = append(textDeltas, delta.Delta.Text)
			case "input_json_delta":
				if delta.Delta.PartialJSON != nil && *delta.Delta.PartialJSON == "" {
					sawEmptyToolDelta = true
				}
			}

		case "content_block_stop":
			sawToolStop = true // we have two stops, but presence is fine

		case "message_delta":
			var md MessageDeltaEvent
			if err := json.Unmarshal([]byte(e.Data), &md); err != nil {
				t.Fatalf("unmarshal message_delta: %v", err)
			}
			if md.Delta.StopReason != "tool_use" {
				t.Errorf("expected stop_reason tool_use, got %q", md.Delta.StopReason)
			}
			if md.Usage == nil || md.Usage.OutputTokens != 89 {
				if md.Usage == nil {
					t.Errorf("message_delta missing usage")
				} else {
					t.Errorf("expected output_tokens 89, got %d", md.Usage.OutputTokens)
				}
			}
			sawMessageDelta = true

		case "message_stop":
			sawMessageStop = true
		}
	}

	if !sawMessageStart {
		t.Error("missing message_start")
	}
	if !sawTextBlockStart {
		t.Error("missing text content_block_start with empty text")
	}
	if len(textDeltas) < 2 || textDeltas[0] != "Okay" || textDeltas[1] != "," {
		t.Errorf("unexpected first text deltas: %v", textDeltas)
	}
	if !sawToolStart {
		t.Error("missing tool_use content_block_start")
	}
	if !sawEmptyToolDelta {
		t.Error("missing initial empty input_json_delta partial_json")
	}
	if !sawToolStop {
		t.Error("missing content_block_stop")
	}
	if !sawMessageDelta {
		t.Error("missing message_delta with usage and stop_reason")
	}
	if !sawMessageStop {
		t.Error("missing message_stop")
	}
}

// SimpleSSEEvent for testing
type SimpleSSEEvent struct {
	Event string
	Data  string
}

// parseSimpleSSEEvents parses SSE formatted text into events
func parseSimpleSSEEvents(body string) []SimpleSSEEvent {
	var events []SimpleSSEEvent
	lines := strings.Split(body, "\n")

	var currentEvent SimpleSSEEvent
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			currentEvent.Event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			currentEvent.Data = strings.TrimPrefix(line, "data: ")
		} else if line == "" && currentEvent.Event != "" {
			events = append(events, currentEvent)
			currentEvent = SimpleSSEEvent{}
		}
	}

	// Add last event if exists
	if currentEvent.Event != "" || currentEvent.Data != "" {
		events = append(events, currentEvent)
	}

	return events
}
