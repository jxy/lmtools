package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

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
