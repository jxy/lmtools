package core

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/apifixtures"
	"net/http"
	"strings"
	"testing"
)

// streamedThoughtSignatures reads, from the raw stream, the signature each
// tool call came with in extra_content, by call index.
func streamedThoughtSignatures(t *testing.T, stream string) []string {
	t.Helper()
	var signatures []string
	for _, line := range strings.Split(stream, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index        int                         `json:"index"`
						ExtraContent *OpenAIToolCallExtraContent `json:"extra_content"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("stream chunk %q: %v", data, err)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				for len(signatures) <= call.Index {
					signatures = append(signatures, "")
				}
				if signature := openAIToolCallThoughtSignature(call.ExtraContent); signature != "" {
					signatures[call.Index] = signature
				}
			}
		}
	}
	return signatures
}

// repliedThoughtSignatures reads, from the raw reply, the signature each
// tool call came with in extra_content.
func repliedThoughtSignatures(t *testing.T, reply []byte) []string {
	t.Helper()
	var decoded struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ExtraContent *OpenAIToolCallExtraContent `json:"extra_content"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(reply, &decoded); err != nil {
		t.Fatal(err)
	}
	var signatures []string
	for _, call := range decoded.Choices[0].Message.ToolCalls {
		signatures = append(signatures, openAIToolCallThoughtSignature(call.ExtraContent))
	}
	return signatures
}

// A Gemini thought signature that comes with a tool call on the Chat
// Completions wire stays with that call, streamed or not: on the parsed call,
// in a reasoning block just in front of it, and in the next request, in the
// call's extra_content. Gemini 3 and later reject a call of the current turn
// sent back without it, and a backend need not fold it into the call's ID
// as Argo does. Only the first of Gemini's parallel calls carries one.
func TestOpenAIChatToolCallsKeepGeminiThoughtSignatures(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	stream, err := apifixtures.ReadCaseFile(suite.Root, "stream-argo-openai-tool-call-to-anthropic", "captures/argo-openai-stream.stream.txt")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := apifixtures.ReadCaseFile(suite.Root, "response-argo-openai-parallel-tool-calls", "captures/argo-openai.response.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name        string
		stream      bool
		contentType string
		body        []byte
		want        []string
	}{
		{"streamed", true, "text/event-stream", stream, streamedThoughtSignatures(t, string(stream))},
		{"not streamed, three parallel calls", false, "application/json", reply, repliedThoughtSignatures(t, reply)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.want) == 0 || tt.want[0] == "" {
				t.Fatalf("capture has no signed first call: %q", tt.want)
			}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{tt.contentType}}, Body: io.NopCloser(strings.NewReader(string(tt.body)))}
			response, err := HandleResponseWithOptions(context.Background(), newStreamTestRequestConfig("openai", tt.stream, false, ""), resp, &MockLogger{logDir: t.TempDir()}, &MockNotifier{}, ResponseParseOptions{Output: textResponseOutput{out: &strings.Builder{}}})
			if err != nil {
				t.Fatalf("HandleResponseWithOptions() error = %v", err)
			}
			if len(response.ToolCalls) != len(tt.want) {
				t.Fatalf("tool calls = %d, want %d", len(response.ToolCalls), len(tt.want))
			}
			for i, call := range response.ToolCalls {
				if call.ThoughtSignature != tt.want[i] {
					t.Fatalf("call %d thought signature = %.24q, want %.24q", i, call.ThoughtSignature, tt.want[i])
				}
			}

			uses := 0
			for i, block := range response.Blocks {
				use, ok := block.(ToolUseBlock)
				if !ok {
					continue
				}
				signed := false
				if i > 0 {
					if reasoning, ok := response.Blocks[i-1].(ReasoningBlock); ok {
						signed = reasoning.Provider == "google" && reasoning.Type == "thought_signature" && reasoning.Signature == tt.want[uses]
					}
				}
				if signed != (tt.want[uses] != "") {
					t.Fatalf("tool use %s: signature block in front = %v, want %v; blocks = %+v", use.ID, signed, tt.want[uses] != "", response.Blocks)
				}
				uses++
			}

			_, rendered := ConvertBlocksToOpenAIContentTyped(response.Blocks)
			if len(rendered) != len(tt.want) {
				t.Fatalf("rendered tool calls = %d, want %d", len(rendered), len(tt.want))
			}
			for i, call := range rendered {
				if got := openAIToolCallThoughtSignature(call.ExtraContent); got != tt.want[i] {
					t.Fatalf("rendered call %d extra_content signature = %.24q, want %.24q", i, got, tt.want[i])
				}
				data, err := json.Marshal(call)
				if err != nil {
					t.Fatal(err)
				}
				if has := strings.Contains(string(data), `"extra_content":{"google":{"thought_signature":`); has != (tt.want[i] != "") {
					t.Fatalf("rendered call %d = %s, want extra_content %v", i, data, tt.want[i] != "")
				}
			}
		})
	}
}

// A signature in front of text, or of a custom tool call, belongs to that
// block, and the Chat Completions wire has no place for it there: it does not
// move on to the next function call.
func TestOpenAIChatToolCallLeavesAnotherBlocksSignatureBehind(t *testing.T) {
	signature := ReasoningBlock{Provider: "google", Type: "thought_signature", Signature: "other-signature"}
	call := ToolUseBlock{ID: "call_1", Name: "run", Input: json.RawMessage(`{}`)}
	for name, blocks := range map[string][]Block{
		"text":        {signature, TextBlock{Text: "Looking it up."}, call},
		"custom call": {signature, ToolUseBlock{ID: "call_0", Type: "custom", Name: "freeform", InputString: "x"}, call},
	} {
		t.Run(name, func(t *testing.T) {
			_, calls := ConvertBlocksToOpenAIContentTyped(blocks)
			last := calls[len(calls)-1]
			if last.ID != "call_1" || last.ExtraContent != nil {
				t.Fatalf("calls = %+v, want call_1 last, without extra_content", calls)
			}
		})
	}
}
