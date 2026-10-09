package core

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/apifixtures"
	"os"
	"reflect"
	"strings"
	"testing"
)

func readOpenAIChatFixture(t *testing.T, caseID, name string) []byte {
	t.Helper()
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatal(err)
	}
	data, err := apifixtures.ReadCaseFile(suite.Root, caseID, "captures/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parseOpenAIChatFixtureStream(t *testing.T, data []byte) (Response, []ResponseStreamEvent, error) {
	t.Helper()
	log, err := os.CreateTemp(t.TempDir(), "stream-*.log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	output := &recordingResponseOutput{}
	response, err := handleOpenAIStreamWithTools(context.Background(), io.NopCloser(strings.NewReader(string(data))), log, output, nil)
	return response, output.events, err
}

func TestParseOpenAIChatLlamaCPPReasoning(t *testing.T) {
	for _, mode := range []string{"buffered", "stream"} {
		t.Run(mode, func(t *testing.T) {
			var response Response
			var err error
			if mode == "buffered" {
				response, err = parseOpenAIResponse(readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai.response.json"), false)
			} else {
				var events []ResponseStreamEvent
				response, events, err = parseOpenAIChatFixtureStream(t, readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-stream.stream.txt"))
				assertOpenAIChatReasoningEvents(t, response, events, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Text != "437" || response.IncompleteReason != "" {
				t.Fatalf("response = %+v", response)
			}
			if len(response.Blocks) != 2 {
				t.Fatalf("blocks = %#v, want reasoning then answer", response.Blocks)
			}
			reasoning, ok := response.Blocks[0].(ReasoningBlock)
			if !ok || reasoning.Provider != "openai" || reasoning.Type != "reasoning_content" || !strings.Contains(reasoning.Text, "437") {
				t.Fatalf("reasoning = %#v", response.Blocks[0])
			}
			if answer, ok := response.Blocks[1].(TextBlock); !ok || answer.Text != response.Text {
				t.Fatalf("answer = %#v", response.Blocks[1])
			}
			messages := []TypedMessage{{Role: "assistant", Blocks: response.Blocks}}
			wire := ToOpenAITyped(messages)
			rendered := MarshalOpenAIMessagesForRequest(wire)[0].(map[string]interface{})
			if rendered["content"] != "437" || rendered["reasoning_content"] != reasoning.Text {
				t.Fatalf("replayed message = %#v", rendered)
			}
			if replayed := FromOpenAITyped(wire); !reflect.DeepEqual(replayed, messages) {
				t.Fatalf("typed round trip = %#v, want %#v", replayed, messages)
			}
		})
	}
}

func assertOpenAIChatReasoningEvents(t *testing.T, response Response, events []ResponseStreamEvent, incomplete bool) {
	t.Helper()
	var reasoning, answer strings.Builder
	var starts, ends int
	var open bool
	for _, event := range events {
		switch event.Type {
		case ResponseStreamReasoningStart:
			if open {
				t.Fatal("reasoning started before the previous block closed")
			}
			open = true
			starts++
		case ResponseStreamReasoningDelta:
			if !open {
				t.Fatal("reasoning delta outside its block")
			}
			reasoning.WriteString(event.Text)
		case ResponseStreamReasoningEnd:
			if !open || event.Incomplete != incomplete {
				t.Fatalf("reasoning end = %+v, open=%v, want incomplete=%v", event, open, incomplete)
			}
			open = false
			ends++
		case ResponseStreamTextDelta:
			if open {
				t.Fatal("answer delta inside a reasoning block")
			}
			answer.WriteString(event.Text)
		}
	}
	if starts != 1 || ends != 1 || open {
		t.Fatalf("reasoning starts=%d ends=%d open=%v", starts, ends, open)
	}
	if len(response.Blocks) == 0 {
		t.Fatal("missing reasoning block")
	}
	block, ok := response.Blocks[0].(ReasoningBlock)
	if !ok || reasoning.String() != block.Text || answer.String() != response.Text {
		t.Fatalf("event projection reasoning=%q answer=%q, response=%+v", reasoning.String(), answer.String(), response)
	}
}

func TestParseOpenAIChatLlamaCPPTokenLimit(t *testing.T) {
	buffered, err := parseOpenAIResponse(readOpenAIChatFixture(t, "response-openai-llamacpp-length", "openai.response.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	streamed, events, err := parseOpenAIChatFixtureStream(t, readOpenAIChatFixture(t, "response-openai-llamacpp-length", "openai-stream.stream.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []Response{buffered, streamed} {
		if response.Text != "" || response.IncompleteReason != "max_output_tokens" || len(response.Blocks) != 1 {
			t.Fatalf("limited response = %+v", response)
		}
	}
	assertOpenAIChatReasoningEvents(t, streamed, events, true)
}

func TestOpenAIChatLlamaCPPStreamUsage(t *testing.T) {
	response, _, err := parseOpenAIChatFixtureStream(t, readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-usage.stream.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "READY" {
		t.Fatalf("answer = %q", response.Text)
	}
	assertUsageEquals(t, response.Usage, &Usage{
		InputTokens: intPtr(20), OutputTokens: intPtr(71), TotalTokens: intPtr(91), CacheReadInputTokens: intPtr(7),
	})
	if response.Usage.ReasoningTokens != nil {
		t.Fatal("reasoning token count was invented")
	}
}

func TestOpenAIChatReasoningBeforeToolPreservesArguments(t *testing.T) {
	data := readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-reasoning-tool.stream.txt")
	for _, doneMarker := range []bool{true, false} {
		name := "done_marker"
		if !doneMarker {
			name = "finish_reason_then_eof"
		}
		t.Run(name, func(t *testing.T) {
			stream := data
			if !doneMarker {
				stream = []byte(strings.ReplaceAll(string(stream), "data: [DONE]\n", ""))
			}
			response, events, err := parseOpenAIChatFixtureStream(t, stream)
			if err != nil {
				t.Fatal(err)
			}
			if response.IncompleteReason != "" || len(response.ToolCalls) != 1 || len(response.Blocks) != 2 {
				t.Fatalf("tool response = %+v", response)
			}
			call := response.ToolCalls[0]
			if call.ID != "call_fixture" || call.Name != "inspect" || string(call.Args) != `{"value":9007199254740993}` {
				t.Fatalf("tool call = %+v", call)
			}
			assertOpenAIChatReasoningEvents(t, response, events, false)
		})
	}
}

func TestOpenAIChatInterleavedChoiceReasoning(t *testing.T) {
	response, events, err := parseOpenAIChatFixtureStream(t, readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-interleaved-reasoning.stream.txt"))
	if err != nil || response.IncompleteReason != "" || response.Text != "First answer.Second answer." {
		t.Fatalf("interleaved response = %+v, error=%v", response, err)
	}
	var open bool
	var visible strings.Builder
	for _, event := range events {
		switch event.Type {
		case ResponseStreamReasoningStart:
			if open {
				t.Fatal("nested reasoning sections")
			}
			open = true
		case ResponseStreamReasoningDelta:
			if !open {
				t.Fatal("reasoning delta outside a section")
			}
			visible.WriteString(event.Text)
		case ResponseStreamReasoningEnd:
			if !open || event.Incomplete {
				t.Fatalf("invalid reasoning ending = %+v", event)
			}
			open = false
		case ResponseStreamTextDelta:
			if open {
				t.Fatal("answer inside a reasoning section")
			}
		}
	}
	if open || visible.String() != "First reasoning.Second reasoning. More." {
		t.Fatalf("open=%v reasoning=%q", open, visible.String())
	}
	if len(response.Blocks) != 5 {
		t.Fatalf("blocks = %#v", response.Blocks)
	}
	first, firstOK := response.Blocks[0].(ReasoningBlock)
	second, secondOK := response.Blocks[1].(ReasoningBlock)
	third, thirdOK := response.Blocks[3].(ReasoningBlock)
	if !firstOK || !secondOK || !thirdOK || first.Text != "First reasoning." || second.Text != "Second reasoning." || third.Text != " More." {
		t.Fatalf("reasoning choices merged: %#v", response.Blocks)
	}
}

func TestOpenAIChatInterleavedChoiceBlockOrderMatchesOutput(t *testing.T) {
	data := readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-interleaved-text.stream.txt")
	response, _, err := parseOpenAIChatFixtureStream(t, data)
	if err != nil || response.IncompleteReason != "" || response.Text != "ABC" {
		t.Fatalf("interleaved response=%+v error=%v", response, err)
	}
	var answer strings.Builder
	for _, block := range response.Blocks {
		if text, ok := block.(TextBlock); ok {
			answer.WriteString(text.Text)
		}
	}
	if answer.String() != response.Text {
		t.Fatalf("block answer=%q, output=%q", answer.String(), response.Text)
	}
}

func TestOpenAIChatErrorClosesReasoning(t *testing.T) {
	response, events, err := parseOpenAIChatFixtureStream(t, readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-reasoning-error.stream.txt"))
	if err == nil || !strings.Contains(err.Error(), "fixture upstream failed") || response.IncompleteReason != "stream_interrupted" {
		t.Fatalf("error response = %+v, error=%v", response, err)
	}
	assertOpenAIChatReasoningEvents(t, response, events, true)
}

func TestOpenAIChatIncompletePreservesPartialAnswer(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		buffered := strings.ReplaceAll(string(readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai.response.json")), `"finish_reason":"stop"`, `"finish_reason":"`+reason+`"`)
		response, err := parseOpenAIResponse([]byte(buffered), false)
		if err != nil || response.Text != "437" || response.IncompleteReason != openAIChatIncompleteReason(reason) {
			t.Fatalf("buffered response=%+v error=%v", response, err)
		}
		stream := strings.ReplaceAll(string(readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-stream.stream.txt")), `"finish_reason":"stop"`, `"finish_reason":"`+reason+`"`)
		response, events, err := parseOpenAIChatFixtureStream(t, []byte(stream))
		if err != nil || response.Text != "437" || response.IncompleteReason != openAIChatIncompleteReason(reason) {
			t.Fatalf("streamed response=%+v error=%v", response, err)
		}
		// The reasoning completed before the partial answer was cut short.
		assertOpenAIChatReasoningEvents(t, response, events, false)
	}
}

func TestToOpenAIChatReplaysOnlyChatReasoning(t *testing.T) {
	messages := []TypedMessage{{Role: "assistant", Blocks: []Block{
		ReasoningBlock{Provider: "openai", Type: "reasoning", Text: "Responses summary", EncryptedContent: "opaque"},
		ReasoningBlock{Provider: "anthropic", Type: "thinking", Text: "Anthropic thinking", Signature: "signature"},
		openAIChatReasoningBlock("Chat thinking"),
		TextBlock{Text: "answer"},
	}}}
	rendered := MarshalOpenAIMessagesForRequest(ToOpenAITyped(messages))[0].(map[string]interface{})
	if rendered["content"] != "answer" || rendered["reasoning_content"] != "Chat thinking" || len(rendered) != 3 {
		t.Fatalf("replay = %#v", rendered)
	}
}

func TestOpenAIChatInterruptedReasoning(t *testing.T) {
	data := readOpenAIChatFixture(t, "response-openai-llamacpp-length", "openai-stream.stream.txt")
	var unfinished strings.Builder
	for _, event := range strings.Split(string(data), "\n\n") {
		if strings.Contains(event, `"finish_reason":"length"`) || strings.Contains(event, "[DONE]") {
			break
		}
		unfinished.WriteString(event + "\n\n")
	}
	response, events, err := parseOpenAIChatFixtureStream(t, []byte(unfinished.String()))
	if err != nil {
		t.Fatal(err)
	}
	if response.IncompleteReason != "stream_interrupted" || len(response.ToolCalls) != 0 {
		t.Fatalf("interrupted response = %+v", response)
	}
	assertOpenAIChatReasoningEvents(t, response, events, true)
}

func TestOpenAIChatIncompleteToolArguments(t *testing.T) {
	data := readOpenAIChatFixture(t, "response-openai-llamacpp-reasoning", "openai-reasoning-tool.stream.txt")
	for _, reason := range []string{"length", "content_filter"} {
		stream := strings.ReplaceAll(string(data), `"tool_calls"}]}`, `"`+reason+`"}]}`)
		stream = strings.ReplaceAll(stream, "9007199254740993}", "9007")
		response, _, err := parseOpenAIChatFixtureStream(t, []byte(stream))
		if err != nil || response.IncompleteReason != openAIChatIncompleteReason(reason) || len(response.ToolCalls) != 0 {
			t.Fatalf("%s response=%+v error=%v", reason, response, err)
		}
		for _, block := range response.Blocks {
			if _, ok := block.(ToolUseBlock); ok {
				t.Fatal("incomplete tool call entered replay history")
			}
		}
	}
	malformed := strings.ReplaceAll(string(data), "9007199254740993}", "9007")
	if _, _, err := parseOpenAIChatFixtureStream(t, []byte(malformed)); err == nil {
		t.Fatal("completed tool call with malformed arguments succeeded")
	}
}

func TestOpenAIChatStreamUsageRequestedOnEveryRound(t *testing.T) {
	for _, provider := range []string{"openai", "argo"} {
		for _, stream := range []bool{false, true} {
			t.Run(provider+"/"+openAIChatStreamMode(stream), func(t *testing.T) {
				cfg := newProviderSpecTestConfig(provider, "gpt5", "https://example.test/v1")
				cfg.StreamChat = stream
				messages := []TypedMessage{NewTextMessage("user", "inspect")}
				_, initial, err := BuildChatRequest(cfg, messages, ChatBuildOptions{Stream: stream})
				if err != nil {
					t.Fatal(err)
				}
				messages = append(messages,
					TypedMessage{Role: "assistant", Blocks: []Block{ToolUseBlock{ID: "call_1", Name: "inspect", Input: json.RawMessage(`{}`)}}},
					TypedMessage{Role: "user", Blocks: []Block{ToolResultBlock{ToolUseID: "call_1", Content: "ok"}}},
				)
				_, followup, err := BuildToolResultRequest(cfg, cfg.Model, "", nil, messages)
				if err != nil {
					t.Fatal(err)
				}
				for _, body := range [][]byte{initial, followup} {
					payload := decodeRequestBody(t, body)
					options, exists := payload["stream_options"]
					if !stream {
						if exists {
							t.Fatalf("buffered request carried stream_options: %#v", options)
						}
						continue
					}
					if !reflect.DeepEqual(options, map[string]interface{}{"include_usage": true}) {
						t.Fatalf("stream_options = %#v", options)
					}
				}
			})
		}
	}
}

func openAIChatStreamMode(value bool) string {
	if value {
		return "stream"
	}
	return "buffered"
}
