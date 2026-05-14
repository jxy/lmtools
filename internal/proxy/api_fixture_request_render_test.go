package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/apifixtures"
	"lmtools/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIFixtureRequestRendering(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	caseFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_CASE"))
	providerFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_PROVIDER"))

	for _, listedCase := range suite.Manifest.Cases {
		meta, err := apifixtures.LoadCaseMeta(suite.Root, listedCase.ID)
		if err != nil {
			t.Fatalf("LoadCaseMeta(%q) error = %v", listedCase.ID, err)
		}
		if !apifixtures.MatchesFilters(meta, caseFilter, providerFilter) {
			continue
		}
		if !apifixtures.StringSliceContains(meta.Kinds, "request") {
			continue
		}

		t.Run(meta.ID, func(t *testing.T) {
			var typed TypedRequest
			ingress, err := apifixtures.ReadCaseFile(suite.Root, meta.ID, "ingress.json")
			if err != nil {
				t.Fatalf("ReadCaseFile(ingress.json) error = %v", err)
			}

			switch meta.IngressFamily {
			case "openai":
				var req OpenAIRequest
				if err := json.Unmarshal(ingress, &req); err != nil {
					t.Fatalf("unmarshal OpenAI ingress: %v", err)
				}
				typed = OpenAIRequestToTyped(&req)
			case "openai-responses":
				var req OpenAIResponsesRequest
				if err := json.Unmarshal(ingress, &req); err != nil {
					t.Fatalf("unmarshal OpenAI Responses ingress: %v", err)
				}
				typed, err = OpenAIResponsesRequestToTyped(context.Background(), &req)
				if err != nil {
					t.Fatalf("OpenAIResponsesRequestToTyped() error = %v", err)
				}
			case "anthropic":
				var req AnthropicRequest
				if err := json.Unmarshal(ingress, &req); err != nil {
					t.Fatalf("unmarshal Anthropic ingress: %v", err)
				}
				typed = AnthropicRequestToTyped(&req)
			default:
				t.Fatalf("unsupported ingress_family %q", meta.IngressFamily)
			}

			assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/typed.json", projectTypedRequest(typed))

			for _, provider := range apifixtures.RequestRenderTargets(meta) {
				switch provider {
				case "openai":
					openAIReq, err := TypedToOpenAIRequest(typed, meta.Models["openai"])
					if err != nil {
						t.Fatalf("TypedToOpenAIRequest() error = %v", err)
					}
					assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/render/openai.request.json", openAIReq)
				case "openai-responses":
					responsesReq, err := TypedToOpenAIResponsesRequest(typed, meta.Models["openai-responses"])
					if err != nil {
						t.Fatalf("TypedToOpenAIResponsesRequest() error = %v", err)
					}
					assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/render/openai-responses.request.json", responsesReq)
				case "anthropic":
					anthReq, err := TypedToAnthropicRequest(typed, meta.Models["anthropic"])
					if err != nil {
						t.Fatalf("TypedToAnthropicRequest() error = %v", err)
					}
					assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/render/anthropic.request.json", anthReq)
				case "google":
					googleReq, err := TypedToGoogleRequest(typed, meta.Models["google"], nil)
					if err != nil {
						t.Fatalf("TypedToGoogleRequest() error = %v", err)
					}
					assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/render/google.request.json", googleReq)
				case "argo":
					argoUser := meta.ArgoUser
					if argoUser == "" {
						argoUser = apifixtures.DefaultArgoUser
					}
					argoReq, err := TypedToArgoRequest(typed, meta.Models["argo"], argoUser)
					if err != nil {
						t.Fatalf("TypedToArgoRequest() error = %v", err)
					}
					assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/render/argo.request.json", argoReq)
				default:
					t.Fatalf("unsupported render target %q", provider)
				}
			}
		})
	}
}

func TestAPIFixtureStreamingTranslations(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	caseFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_CASE"))
	providerFilter := strings.TrimSpace(os.Getenv("LMTOOLS_API_FIXTURE_PROVIDER"))

	for _, listedCase := range suite.Manifest.Cases {
		meta, err := apifixtures.LoadCaseMeta(suite.Root, listedCase.ID)
		if err != nil {
			t.Fatalf("LoadCaseMeta(%q) error = %v", listedCase.ID, err)
		}
		if !apifixtures.MatchesFilters(meta, caseFilter, providerFilter) {
			continue
		}
		if !apifixtures.StringSliceContains(meta.Kinds, "stream") {
			continue
		}

		t.Run(meta.ID, func(t *testing.T) {
			streamPath := filepath.Join("captures", meta.StreamSource+"-stream.stream.txt")
			rawStream, err := apifixtures.ReadCaseFile(suite.Root, meta.ID, streamPath)
			if err != nil {
				t.Fatalf("ReadCaseFile(%q) error = %v", streamPath, err)
			}

			switch {
			case meta.StreamSource == "openai" && meta.StreamTarget == "anthropic":
				recorder := newFlushableRecorder()
				handler, err := NewAnthropicStreamHandler(recorder, meta.Models["openai"], context.Background())
				if err != nil {
					t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
				}
				if err := ensureAnthropicTextPreamble(handler); err != nil {
					t.Fatalf("ensureAnthropicTextPreamble() error = %v", err)
				}
				parser := NewOpenAIStreamParser(handler)
				if err := parser.Parse(strings.NewReader(string(rawStream))); err != nil {
					t.Fatalf("OpenAIStreamParser.Parse() error = %v", err)
				}
				assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/stream_projection.json", projectAnthropicStream(recorder.Body.String()))

			case meta.StreamSource == "anthropic" && meta.StreamTarget == "openai":
				recorder := newFlushableRecorder()
				writer, err := NewOpenAIStreamWriter(recorder, meta.Models["openai"], context.Background(), WithIncludeUsage(true))
				if err != nil {
					t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
				}
				server := &Server{}
				if err := server.convertAnthropicStreamToOpenAI(context.Background(), strings.NewReader(string(rawStream)), writer); err != nil {
					t.Fatalf("convertAnthropicStreamToOpenAI() error = %v", err)
				}
				assertFixtureJSONEqual(t, suite.Root, meta.ID, "expected/stream_projection.json", projectOpenAIStream(recorder.Body.String()))

			default:
				t.Fatalf("unsupported stream translation %s -> %s", meta.StreamSource, meta.StreamTarget)
			}
		})
	}
}

func TestTypedToArgoRequestRejectsAudioBlocks(t *testing.T) {
	typed := TypedRequest{
		Messages: []core.TypedMessage{
			{
				Role: string(core.RoleUser),
				Blocks: []core.Block{
					core.TextBlock{Text: "Transcribe this"},
					core.AudioBlock{Data: "base64-audio", Format: "wav"},
				},
			},
		},
	}

	_, err := TypedToArgoRequest(typed, "gpt5mini", "fixture-user")
	if err == nil {
		t.Fatal("expected TypedToArgoRequest() error")
	}
	if got := err.Error(); got != "argo provider does not support audio input blocks" {
		t.Fatalf("TypedToArgoRequest() error = %q, want argo audio rejection", got)
	}
}

func assertFixtureJSONEqual(t *testing.T, root, caseID, rel string, actual interface{}) {
	t.Helper()

	wantBytes, err := apifixtures.ReadCaseFile(root, caseID, rel)
	if err != nil {
		t.Fatalf("ReadCaseFile(%q) error = %v", rel, err)
	}
	wantCanonical, err := apifixtures.CanonicalJSON(wantBytes)
	if err != nil {
		t.Fatalf("CanonicalJSON(want) error = %v", err)
	}

	actualBytes, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("json.Marshal(actual) error = %v", err)
	}
	actualCanonical, err := apifixtures.CanonicalJSON(actualBytes)
	if err != nil {
		t.Fatalf("CanonicalJSON(actual) error = %v", err)
	}

	if string(wantCanonical) != string(actualCanonical) {
		t.Fatalf("fixture mismatch for %s\nwant:\n%s\n\ngot:\n%s", rel, wantCanonical, actualCanonical)
	}
}

func projectTypedRequest(typed TypedRequest) map[string]interface{} {
	projected := map[string]interface{}{
		"messages": projectTypedMessages(typed.Messages),
		"stream":   typed.Stream,
	}
	if typed.System != "" {
		projected["system"] = typed.System
	}
	if typed.Developer != "" {
		projected["developer"] = typed.Developer
	}
	if typed.MaxTokens != nil {
		projected["max_tokens"] = *typed.MaxTokens
	}
	if typed.Temperature != nil {
		projected["temperature"] = *typed.Temperature
	}
	if typed.TopP != nil {
		projected["top_p"] = *typed.TopP
	}
	if len(typed.Stop) > 0 {
		projected["stop"] = typed.Stop
	}
	if typed.ReasoningEffort != "" {
		projected["reasoning_effort"] = typed.ReasoningEffort
	}
	if typed.Thinking != nil {
		projected["thinking"] = typed.Thinking
	}
	if typed.OutputConfig != nil {
		projected["output_config"] = typed.OutputConfig
	}
	if typed.ResponseFormat != nil {
		projected["response_format"] = typed.ResponseFormat
	}
	if len(typed.Metadata) > 0 {
		projected["metadata"] = typed.Metadata
	}
	if typed.ServiceTier != "" {
		projected["service_tier"] = typed.ServiceTier
	}
	if len(typed.Tools) > 0 {
		projected["tools"] = projectToolDefinitions(typed.Tools)
	}
	if typed.ToolChoice != nil {
		toolChoice := map[string]interface{}{
			"type": typed.ToolChoice.Type,
		}
		if typed.ToolChoice.Name != "" {
			toolChoice["name"] = typed.ToolChoice.Name
		}
		projected["tool_choice"] = toolChoice
	}
	return projected
}

func projectTypedMessages(messages []core.TypedMessage) []map[string]interface{} {
	projected := make([]map[string]interface{}, 0, len(messages))
	for _, message := range messages {
		projected = append(projected, map[string]interface{}{
			"role":   message.Role,
			"blocks": projectBlocks(message.Blocks),
		})
	}
	return projected
}

func projectBlocks(blocks []core.Block) []map[string]interface{} {
	projected := make([]map[string]interface{}, 0, len(blocks))
	for _, block := range blocks {
		switch value := block.(type) {
		case core.TextBlock:
			projected = append(projected, map[string]interface{}{
				"type": "text",
				"text": value.Text,
			})
		case core.ToolUseBlock:
			projected = append(projected, map[string]interface{}{
				"type":  "tool_use",
				"id":    value.ID,
				"name":  value.Name,
				"input": rawJSONToInterfaceForTest(value.Input),
			})
		case core.ToolResultBlock:
			blockMap := map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": value.ToolUseID,
				"content":     value.Content,
				"is_error":    value.IsError,
			}
			if value.Name != "" {
				blockMap["name"] = value.Name
			}
			projected = append(projected, blockMap)
		case core.ReasoningBlock:
			projected = append(projected, map[string]interface{}{
				"type":              "reasoning",
				"provider":          value.Provider,
				"reasoning_type":    value.Type,
				"text":              value.Text,
				"signature":         value.Signature,
				"encrypted_content": value.EncryptedContent,
			})
		case core.ImageBlock:
			projected = append(projected, map[string]interface{}{
				"type":   "image",
				"url":    value.URL,
				"detail": value.Detail,
			})
		case core.AudioBlock:
			projected = append(projected, map[string]interface{}{
				"type":     "audio",
				"id":       value.ID,
				"data":     value.Data,
				"format":   value.Format,
				"url":      value.URL,
				"duration": value.Duration,
			})
		case core.FileBlock:
			projected = append(projected, map[string]interface{}{
				"type":    "file",
				"file_id": value.FileID,
			})
		}
	}
	return projected
}

func projectToolDefinitions(tools []core.ToolDefinition) []map[string]interface{} {
	projected := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		projected = append(projected, map[string]interface{}{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.InputSchema,
		})
	}
	return projected
}

func rawJSONToInterfaceForTest(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return map[string]interface{}{}
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	return decoded
}

func projectAnthropicStream(raw string) []map[string]interface{} {
	lines := strings.Split(raw, "\n")
	var currentEvent string
	var projected []map[string]interface{}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "" {
				continue
			}
			var decoded map[string]interface{}
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				continue
			}

			entry := map[string]interface{}{
				"event": currentEvent,
			}
			if index, ok := decoded["index"]; ok {
				entry["index"] = index
			}
			if delta, ok := decoded["delta"].(map[string]interface{}); ok {
				if deltaType, ok := delta["type"]; ok {
					entry["delta_type"] = deltaType
				}
				if text, ok := delta["text"]; ok {
					entry["text"] = text
				}
				if partialJSON, ok := delta["partial_json"]; ok {
					entry["partial_json"] = partialJSON
				}
				if stopReason, ok := delta["stop_reason"]; ok {
					entry["stop_reason"] = stopReason
				}
			}
			if block, ok := decoded["content_block"].(map[string]interface{}); ok {
				if blockType, ok := block["type"]; ok {
					entry["block_type"] = blockType
				}
				if name, ok := block["name"]; ok {
					entry["name"] = name
				}
			}
			if message, ok := decoded["message"].(map[string]interface{}); ok {
				entry["role"] = message["role"]
				entry["model"] = message["model"]
			}
			if usage, ok := decoded["usage"]; ok {
				entry["usage"] = usage
			}
			projected = append(projected, entry)
		}
	}

	return projected
}

func projectOpenAIStream(raw string) []map[string]interface{} {
	lines := strings.Split(raw, "\n")
	projected := make([]map[string]interface{}, 0)

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			projected = append(projected, map[string]interface{}{"done": true})
			continue
		}

		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			continue
		}

		entry := map[string]interface{}{}
		if usage, ok := decoded["usage"]; ok && usage != nil {
			entry["usage"] = usage
		}
		if choices, ok := decoded["choices"].([]interface{}); ok && len(choices) > 0 {
			choice, _ := choices[0].(map[string]interface{})
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				entry["delta"] = delta
			}
			if finishReason, ok := choice["finish_reason"]; ok && finishReason != nil {
				entry["finish_reason"] = finishReason
			}
		}
		projected = append(projected, entry)
	}

	return projected
}
