package core

import "testing"

func TestParseOpenAIResponseWithTools_NormalizesJSONStringArguments(t *testing.T) {
	data := []byte(`{
		"choices": [
			{
				"message": {
					"content": "Checking weather",
					"tool_calls": [
						{
							"id": "call_1",
							"type": "function",
							"function": {
								"name": "get_weather",
								"arguments": "{\"location\":\"Chicago\"}"
							}
						}
					]
				}
			}
		]
	}`)

	text, toolCalls, err := parseOpenAIResponseWithTools(data, false)
	if err != nil {
		t.Fatalf("parseOpenAIResponseWithTools() error = %v", err)
	}
	if text != "Checking weather" {
		t.Fatalf("text = %q, want %q", text, "Checking weather")
	}
	if len(toolCalls) != 1 {
		t.Fatalf("len(toolCalls) = %d, want 1", len(toolCalls))
	}
	if string(toolCalls[0].Args) != `{"location":"Chicago"}` {
		t.Fatalf("toolCalls[0].Args = %q, want raw JSON object", string(toolCalls[0].Args))
	}
}
