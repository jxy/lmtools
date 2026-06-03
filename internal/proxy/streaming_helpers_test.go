package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestForwardSSERecordsPreservesCommentOnlyHeartbeat(t *testing.T) {
	recorder := httptest.NewRecorder()
	err := forwardSSERecords(context.Background(), recorder, strings.NewReader(": ping\n\n"), func(data string) string {
		return data
	})
	if err != nil {
		t.Fatalf("forwardSSERecords() error = %v", err)
	}
	if got, want := recorder.Body.String(), ": ping\n\n"; got != want {
		t.Fatalf("forwarded heartbeat = %q, want %q", got, want)
	}
}

func TestEnsureAnthropicTextPreamble(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-sonnet", context.Background())
	if err != nil {
		t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
	}

	if err := ensureAnthropicTextPreamble(handler); err != nil {
		t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
	}

	events := parseSimpleSSEEvents(recorder.Body.String())
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Event != "message_start" {
		t.Fatalf("first event = %q, want message_start", events[0].Event)
	}
	if events[1].Event != "content_block_start" {
		t.Fatalf("second event = %q, want content_block_start", events[1].Event)
	}
}
