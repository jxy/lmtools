package core

import "testing"

// A tool call's arguments stream silently, sometimes for seconds; the start
// event is what lets a presenter say what the model is writing meanwhile.
func TestStreamParsersAnnounceToolCallStart(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		state := &AnthropicStreamState{}
		for _, line := range []string{
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"universal_command","input":{}}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":[\"ls\"]}"}}`,
		} {
			if _, _, _, err := state.ParseLine(line); err != nil {
				t.Fatal(err)
			}
		}
		events := state.drainResponseStreamEvents()
		if len(events) != 1 || events[0].Type != ResponseStreamToolCallStart || events[0].Text != "universal_command" {
			t.Fatalf("events = %#v", events)
		}
	})
	t.Run("chat", func(t *testing.T) {
		state := NewOpenAIStreamState()
		for _, line := range []string{
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"view_image","arguments":""}}]}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.png\"}"}}]}}]}`,
		} {
			if _, _, _, err := state.ParseLine(line); err != nil {
				t.Fatal(err)
			}
		}
		events := state.drainResponseStreamEvents()
		if len(events) != 1 || events[0].Type != ResponseStreamToolCallStart || events[0].Text != "view_image" {
			t.Fatalf("events = %#v", events)
		}
	})
}
