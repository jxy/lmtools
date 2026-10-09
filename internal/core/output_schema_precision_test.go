package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func TestOpenAIOutputSchemaPlacementAndNumberPrecision(t *testing.T) {
	for _, responses := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("responses=%t/stream=%t", responses, stream), func(t *testing.T) {
				cfg := newProviderSpecTestConfig("openai", "test-model", "http://localhost:9931/v1")
				cfg.OpenAIResponses = responses
				cfg.JSONSchema = json.RawMessage(`{"type":"object","properties":{"answer":{"const":9007199254740993,"maximum":1e309,"minimum":-1e2147483648}},"required":["answer"]}`)
				_, body, err := BuildChatRequest(cfg, []TypedMessage{NewTextMessage("user", "Use the schema")}, ChatBuildOptions{Stream: stream})
				if err != nil {
					t.Fatal(err)
				}
				var request map[string]json.RawMessage
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatal(err)
				}
				var format map[string]json.RawMessage
				if responses {
					var text map[string]json.RawMessage
					if err := json.Unmarshal(request["text"], &text); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(text["format"], &format); err != nil {
						t.Fatal(err)
					}
					if _, exists := request["response_format"]; exists {
						t.Fatal("Responses request carried Chat response_format")
					}
				} else {
					var responseFormat map[string]json.RawMessage
					if err := json.Unmarshal(request["response_format"], &responseFormat); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(responseFormat["json_schema"], &format); err != nil {
						t.Fatal(err)
					}
					if _, exists := request["text"]; exists {
						t.Fatal("Chat request carried Responses text configuration")
					}
				}
				for _, number := range []string{`"const":9007199254740993`, `"maximum":1e309`, `"minimum":-1e2147483648`} {
					if !bytes.Contains(format["schema"], []byte(number)) {
						t.Fatalf("schema lost the exact number %s: %s", number, format["schema"])
					}
				}
			})
		}
	}
}

func TestResponsesReasoningReplayPreservesUnknownNumbers(t *testing.T) {
	block := ReasoningBlock{Provider: "openai", Raw: json.RawMessage(`{"type":"reasoning","summary":[],"vendor_counter":9007199254740993}`)}
	rendered, err := json.Marshal(openAIResponsesReasoningItem(block))
	if err != nil || !bytes.Contains(rendered, []byte(`"vendor_counter":9007199254740993`)) {
		t.Fatalf("replayed reasoning = %s, err=%v", rendered, err)
	}
}
