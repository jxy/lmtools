package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSSEFieldValue(t *testing.T) {
	tests := []struct {
		line      string
		field     string
		want      string
		wantFound bool
	}{
		{line: "data:x", field: "data", want: "x", wantFound: true},
		{line: "data: x", field: "data", want: "x", wantFound: true},
		{line: "data:  x", field: "data", want: " x", wantFound: true},
		{line: "event: message", field: "data", wantFound: false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, found := sseFieldValue(tt.line, tt.field)
			if found != tt.wantFound || got != tt.want {
				t.Fatalf("sseFieldValue(%q, %q) = %q, %v; want %q, %v", tt.line, tt.field, got, found, tt.want, tt.wantFound)
			}
		})
	}
}

func TestConsumeSSEStreamAcceptsNoSpaceFieldsAndResetsEvent(t *testing.T) {
	input := strings.Join([]string{
		"event:message_start",
		`data:{"type":"message_start"}`,
		"",
		`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		"",
	}, "\n")

	var got []string
	err := consumeSSEStream(strings.NewReader(input), func(event string, data json.RawMessage) error {
		got = append(got, event+":"+string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("consumeSSEStream() error = %v", err)
	}

	want := []string{
		`message_start:{"type":"message_start"}`,
		`:{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
	}
	if len(got) != len(want) {
		t.Fatalf("len(events) = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSSEWriterSplitsMultilineData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: "", want: "event: message\ndata: \n\n"},
		{name: "single", data: "first", want: "event: message\ndata: first\n\n"},
		{name: "multi", data: "first\nsecond", want: "event: message\ndata: first\ndata: second\n\n"},
		{name: "trailing newline", data: "first\n", want: "event: message\ndata: first\ndata: \n\n"},
		{name: "crlf", data: "first\r\nsecond\r\n", want: "event: message\ndata: first\ndata: second\ndata: \n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer, err := NewSSEWriter(recorder, context.Background())
			if err != nil {
				t.Fatalf("NewSSEWriter() error = %v", err)
			}
			if err := writer.WriteEvent("message", tt.data); err != nil {
				t.Fatalf("WriteEvent() error = %v", err)
			}
			if got := recorder.Body.String(); got != tt.want {
				t.Fatalf("SSE payload = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConsumeSSEStreamPreservesEventAcrossCommentHeartbeat(t *testing.T) {
	input := strings.Join([]string{
		"event: content_block_delta",
		": ping",
		"",
		`data: {"type":"content_block_delta"}`,
		"",
	}, "\n")

	var got []string
	err := consumeSSEStream(strings.NewReader(input), func(event string, data json.RawMessage) error {
		got = append(got, event+":"+string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("consumeSSEStream() error = %v", err)
	}
	want := []string{`content_block_delta:{"type":"content_block_delta"}`}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestConsumeSSEStream(t *testing.T) {
	input := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start"}`,
		"",
		"event: ping",
		`data: {"type":"ping"}`,
		"",
	}, "\n")

	var got []string
	err := consumeSSEStream(strings.NewReader(input), func(event string, data json.RawMessage) error {
		got = append(got, event+":"+string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("consumeSSEStream() error = %v", err)
	}

	want := []string{
		`message_start:{"type":"message_start"}`,
		`ping:{"type":"ping"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("len(events) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConsumeSSEStreamBuffersMultiLineData(t *testing.T) {
	input := strings.Join([]string{
		"event: message_delta",
		`data: {"type":"message_delta",`,
		`data: "delta":{"stop_reason":"end_turn"}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
	}, "\n")

	var got []string
	err := consumeSSEStream(strings.NewReader(input), func(event string, data json.RawMessage) error {
		got = append(got, event+":"+string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("consumeSSEStream() error = %v", err)
	}

	want := []string{
		"message_delta:" + "{\"type\":\"message_delta\",\n\"delta\":{\"stop_reason\":\"end_turn\"}}",
		`message_stop:{"type":"message_stop"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("len(events) = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A converted stream opens its text block with the first text. A reply that
// only calls tools then has no empty text block in front of them, and one
// with neither leaves no block open. Blocks are numbered in the order they
// start, and every one that starts is stopped once.
func TestAnthropicStreamOpensTheTextBlockWithTheFirstText(t *testing.T) {
	googleCall := `{"functionCall":{"name":"lookup","args":{"q":1}}}`
	for _, tt := range []struct {
		name   string
		render func([]byte, string) ([]byte, error)
		stream string
		want   []string
	}{
		{
			name:   "openai tool call only",
			render: renderAnthropicStreamFromOpenAI,
			stream: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n",
			want:   []string{"start tool_use 0", "stop 0"},
		},
		{
			name:   "openai text only",
			render: renderAnthropicStreamFromOpenAI,
			stream: `data: {"choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n",
			want:   []string{"start text 0", "stop 0"},
		},
		{
			name:   "openai neither",
			render: renderAnthropicStreamFromOpenAI,
			stream: `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n",
		},
		{
			name:   "google function call only",
			render: renderAnthropicStreamFromGoogle,
			stream: `data: {"candidates":[{"content":{"parts":[` + googleCall + `]},"finishReason":"STOP"}]}` + "\n\n",
			want:   []string{"start tool_use 0", "stop 0"},
		},
		{
			name:   "google text, then a function call",
			render: renderAnthropicStreamFromGoogle,
			stream: `data: {"candidates":[{"content":{"parts":[{"text":"Looking"},` + googleCall + `]},"finishReason":"STOP"}]}` + "\n\n",
			want:   []string{"start text 0", "stop 0", "start tool_use 1", "stop 1"},
		},
		{
			name:   "legacy argo text",
			render: renderAnthropicStreamFromArgo,
			stream: "Hello",
			want:   []string{"start text 0", "stop 0"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := tt.render([]byte(tt.stream), "test-model")
			if err != nil {
				t.Fatalf("render error = %v", err)
			}
			var blocks []string
			events := projectAnthropicStream(string(body))
			for _, event := range events {
				switch event["event"] {
				case "content_block_start":
					blocks = append(blocks, fmt.Sprintf("start %v %v", event["block_type"], event["index"]))
				case "content_block_stop":
					blocks = append(blocks, fmt.Sprintf("stop %v", event["index"]))
				}
			}
			if !reflect.DeepEqual(blocks, tt.want) {
				t.Fatalf("content blocks = %q, want %q\n%s", blocks, tt.want, body)
			}
			if last := events[len(events)-1]["event"]; last != "message_stop" {
				t.Fatalf("last event = %v, want message_stop\n%s", last, body)
			}
		})
	}
}
