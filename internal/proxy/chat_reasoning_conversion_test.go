package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/apifixtures"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// chatReasoningCapture reads a Chat Completions capture of a fixture case.
func chatReasoningCapture(t *testing.T, caseID, name string) []byte {
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

// chatReasoningServer runs apiproxy -provider openai -chat-completions in
// front of an upstream that answers with the buffered or the streamed capture,
// as each request asks. It returns the Chat requests the upstream received.
func chatReasoningServer(t *testing.T, buffered, streamed []byte) (http.Handler, func() []map[string]interface{}) {
	t.Helper()
	var mu sync.Mutex
	var requests []map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("upstream request: %v", err)
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(streamed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buffered)
	}))
	t.Cleanup(upstream.Close)
	handler, cleanup := NewTestServer(t, &Config{
		Provider:           constants.ProviderOpenAI,
		ProviderURL:        upstream.URL + "/v1",
		ProviderKeySet:     ProviderKeySet{OpenAIAPIKey: "token"},
		ChatCompletions:    true,
		MaxRequestBodySize: fixtureMaxBodySize,
	})
	t.Cleanup(cleanup)
	return handler, func() []map[string]interface{} {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]interface{}(nil), requests...)
	}
}

func postChatReasoning(t *testing.T, handler http.Handler, path, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, body:\n%s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

// streamedChatReasoning joins the reasoning deltas of a Chat stream capture.
func streamedChatReasoning(t *testing.T, stream []byte) string {
	t.Helper()
	var reasoning strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(stream))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("capture chunk: %v", err)
		}
		for _, choice := range chunk.Choices {
			reasoning.WriteString(choice.Delta.ReasoningContent + choice.Delta.Reasoning)
		}
	}
	return reasoning.String()
}

type sseEvent struct {
	name string
	data map[string]interface{}
}

func sseEvents(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := NewTestSSEScanner(strings.NewReader(body))
	for scanner.Scan() {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(scanner.Data()), &data); err != nil {
			t.Fatalf("event %s: %v", scanner.Event(), err)
		}
		events = append(events, sseEvent{name: scanner.Event(), data: data})
	}
	return events
}

// Minerva's mistral sends its reasoning in the Chat field reasoning. A client
// of the converted routes gets it as thinking: an Anthropic thinking block
// ahead of the text, or a Responses reasoning item ahead of the message.
func TestConvertedRoutesCarryChatReasoning(t *testing.T) {
	const caseID = "response-openai-vllm-reasoning"
	buffered := chatReasoningCapture(t, caseID, "openai.response.json")
	streamed := chatReasoningCapture(t, caseID, "openai-stream.stream.txt")
	var capture struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(buffered, &capture); err != nil {
		t.Fatal(err)
	}
	reasoning, answer := capture.Choices[0].Message.Reasoning, capture.Choices[0].Message.Content
	streamedReasoning := streamedChatReasoning(t, streamed)
	if reasoning == "" || streamedReasoning == "" {
		t.Fatal("the captures carry no reasoning")
	}
	const signature = chatReasoningSignature + "chat_reasoning"
	handler, _ := chatReasoningServer(t, buffered, streamed)

	t.Run("messages", func(t *testing.T) {
		body := postChatReasoning(t, handler, "/v1/messages", `{"model":"mistral-large-4-nvfp4","max_tokens":256,"messages":[{"role":"user","content":"19 times 23?"}]}`)
		var resp struct {
			Content []map[string]interface{} `json:"content"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Content) != 2 || resp.Content[0]["type"] != "thinking" || resp.Content[0]["thinking"] != reasoning ||
			resp.Content[0]["signature"] != signature || resp.Content[1]["type"] != "text" || resp.Content[1]["text"] != answer {
			t.Fatalf("content = %#v, want the reasoning as thinking, then the answer", resp.Content)
		}
	})

	t.Run("streamed messages", func(t *testing.T) {
		body := postChatReasoning(t, handler, "/v1/messages", `{"model":"mistral-large-4-nvfp4","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"19 times 23?"}]}`)
		var thinking, text strings.Builder
		var order []string
		for _, event := range sseEvents(t, body) {
			index, _ := event.data["index"].(float64)
			switch event.name {
			case EventContentBlockStart:
				block, _ := event.data["content_block"].(map[string]interface{})
				order = append(order, "start "+block["type"].(string))
				if block["type"] == "thinking" && (index != 0 || block["thinking"] != "" || block["signature"] != "") {
					t.Fatalf("thinking start = %#v, want block 0 with empty thinking and signature", event.data)
				}
				if block["type"] == "text" && index != 1 {
					t.Fatalf("text start = %#v, want block 1", event.data)
				}
			case EventContentBlockDelta:
				delta, _ := event.data["delta"].(map[string]interface{})
				switch delta["type"] {
				case "thinking_delta":
					thinking.WriteString(delta["thinking"].(string))
				case "signature_delta":
					order = append(order, "signature")
					if index != 0 || delta["signature"] != signature {
						t.Fatalf("signature delta = %#v, want %q on block 0", event.data, signature)
					}
				case "text_delta":
					if index != 1 {
						t.Fatalf("text delta = %#v, want block 1", event.data)
					}
					text.WriteString(delta["text"].(string))
				}
			case EventContentBlockStop:
				order = append(order, "stop")
			}
		}
		if got, want := strings.Join(order, ", "), "start thinking, signature, stop, start text, stop"; got != want {
			t.Fatalf("blocks = %s, want %s\n%s", got, want, body)
		}
		if thinking.String() != streamedReasoning || !strings.Contains(text.String(), "437") {
			t.Fatalf("thinking = %q, text = %q", thinking.String(), text.String())
		}
	})

	t.Run("responses", func(t *testing.T) {
		body := postChatReasoning(t, handler, "/v1/responses", `{"model":"mistral-large-4-nvfp4","input":"19 times 23?"}`)
		var resp OpenAIResponsesResponse
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Output) != 2 || resp.Output[0].Type != "reasoning" || resp.Output[1].Type != "message" {
			t.Fatalf("output = %#v, want a reasoning item, then the message", resp.Output)
		}
		if got := responsesSummaryText(resp.Output[0]); got != reasoning {
			t.Fatalf("summary = %q, want %q", got, reasoning)
		}
	})

	t.Run("streamed responses", func(t *testing.T) {
		body := postChatReasoning(t, handler, "/v1/responses", `{"model":"mistral-large-4-nvfp4","stream":true,"input":"19 times 23?"}`)
		var deltas strings.Builder
		var summaryDone string
		var completed OpenAIResponsesResponse
		for _, event := range sseEvents(t, body) {
			switch event.name {
			case "response.reasoning_summary_text.delta":
				deltas.WriteString(event.data["delta"].(string))
			case "response.reasoning_summary_text.done":
				summaryDone, _ = event.data["text"].(string)
			case "response.completed":
				raw, _ := json.Marshal(event.data["response"])
				if err := json.Unmarshal(raw, &completed); err != nil {
					t.Fatal(err)
				}
			}
		}
		if deltas.String() != streamedReasoning || summaryDone != streamedReasoning {
			t.Fatalf("summary deltas = %q, done = %q, want %q", deltas.String(), summaryDone, streamedReasoning)
		}
		if len(completed.Output) != 2 || completed.Output[0].Type != "reasoning" || completed.Output[1].Type != "message" ||
			responsesSummaryText(completed.Output[0]) != streamedReasoning {
			t.Fatalf("completed output = %#v, want the reasoning item, then the message", completed.Output)
		}
	})
}

func responsesSummaryText(item OpenAIResponsesOutputItem) string {
	var text strings.Builder
	for _, part := range item.Summary {
		if part, ok := part.(map[string]interface{}); ok {
			text.WriteString(part["text"].(string))
		}
	}
	return text.String()
}

// llama.cpp sends reasoning_content, here before a tool call: the thinking
// block is block 0, the tool_use block 1, and no text block opens.
func TestConvertedStreamPutsChatReasoningBeforeAToolCall(t *testing.T) {
	const caseID = "response-openai-llamacpp-reasoning"
	streamed := chatReasoningCapture(t, caseID, "openai-reasoning-tool.stream.txt")
	handler, _ := chatReasoningServer(t, chatReasoningCapture(t, caseID, "openai.response.json"), streamed)
	body := postChatReasoning(t, handler, "/v1/messages", `{"model":"gemma","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"inspect"}],"tools":[{"name":"inspect","input_schema":{"type":"object"}}]}`)
	var blocks []string
	for _, event := range sseEvents(t, body) {
		switch event.name {
		case EventContentBlockStart:
			block, _ := event.data["content_block"].(map[string]interface{})
			blocks = append(blocks, block["type"].(string))
		case EventContentBlockDelta:
			delta, _ := event.data["delta"].(map[string]interface{})
			if delta["type"] == "signature_delta" && delta["signature"] != chatReasoningSignature+"reasoning_content" {
				t.Fatalf("signature delta = %#v", event.data)
			}
		}
	}
	if got := strings.Join(blocks, ", "); got != "thinking, tool_use" {
		t.Fatalf("blocks = %s, want thinking, tool_use\n%s", got, body)
	}
}

// Reasoning goes back to the backend in the field it came in: from an
// Anthropic client's history, from the reasoning items a Responses client
// sends back, and from a stored response that previous_response_id
// continues, whether it was buffered or streamed.
func TestChatReasoningGoesBackInItsField(t *testing.T) {
	const caseID = "response-openai-vllm-reasoning"
	buffered := chatReasoningCapture(t, caseID, "openai.response.json")
	streamed := chatReasoningCapture(t, caseID, "openai-stream.stream.txt")

	assistant := func(requests []map[string]interface{}) map[string]interface{} {
		t.Helper()
		last := requests[len(requests)-1]
		messages, _ := last["messages"].([]interface{})
		for _, message := range messages {
			if message, ok := message.(map[string]interface{}); ok && message["role"] == "assistant" {
				return message
			}
		}
		t.Fatalf("upstream request has no assistant message: %#v", last)
		return nil
	}

	t.Run("anthropic history", func(t *testing.T) {
		for _, tt := range []struct {
			signature, field string
		}{
			{chatReasoningSignature + "chat_reasoning", "reasoning"},
			{chatReasoningSignature + "reasoning_content", "reasoning_content"},
			{"Anthropic signature", ""},
		} {
			handler, requests := chatReasoningServer(t, buffered, streamed)
			postChatReasoning(t, handler, "/v1/messages", `{"model":"mistral-large-4-nvfp4","max_tokens":256,"messages":[
				{"role":"user","content":"19 times 23?"},
				{"role":"assistant","content":[{"type":"thinking","thinking":"Multiply.","signature":"`+tt.signature+`"},{"type":"text","text":"437"}]},
				{"role":"user","content":"And 437 plus 1?"}]}`)
			message := assistant(requests())
			for _, field := range []string{"reasoning", "reasoning_content"} {
				want := ""
				if field == tt.field {
					want = "Multiply."
				}
				if got, _ := message[field].(string); got != want {
					t.Fatalf("signature %q: assistant %s = %q, want %q; message = %#v", tt.signature, field, got, want, message)
				}
			}
		}
	})

	t.Run("responses history", func(t *testing.T) {
		handler, requests := chatReasoningServer(t, buffered, streamed)
		var resp OpenAIResponsesResponse
		if err := json.Unmarshal([]byte(postChatReasoning(t, handler, "/v1/responses", `{"model":"mistral-large-4-nvfp4","store":false,"input":"19 times 23?"}`)), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Output) == 0 || resp.Output[0].EncryptedContent != chatReasoningSignature+"chat_reasoning" {
			t.Fatalf("output = %#v, want a reasoning item carrying the marker", resp.Output)
		}
		// A client that keeps no state sends the output back as input.
		input := []interface{}{map[string]interface{}{"role": "user", "content": "19 times 23?"}}
		for _, item := range resp.Output {
			input = append(input, item)
		}
		input = append(input, map[string]interface{}{"role": "user", "content": "And 437 plus 1?"})
		body, err := json.Marshal(map[string]interface{}{"model": "mistral-large-4-nvfp4", "store": false, "input": input})
		if err != nil {
			t.Fatal(err)
		}
		postChatReasoning(t, handler, "/v1/responses", string(body))
		message := assistant(requests())
		if got, _ := message["reasoning"].(string); got != responsesSummaryText(resp.Output[0]) || got == "" {
			t.Fatalf("assistant reasoning = %q, want the item's summary; message = %#v", got, message)
		}
		if message["content"] != "437" {
			t.Fatalf("assistant content = %#v, want the answer alone", message["content"])
		}
	})

	for _, stream := range []bool{false, true} {
		name := "stored response"
		reasoning := ""
		if stream {
			name = "stored streamed response"
			reasoning = streamedChatReasoning(t, streamed)
		} else {
			var capture struct {
				Choices []struct {
					Message struct {
						Reasoning string `json:"reasoning"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(buffered, &capture); err != nil {
				t.Fatal(err)
			}
			reasoning = capture.Choices[0].Message.Reasoning
		}
		t.Run(name, func(t *testing.T) {
			handler, requests := chatReasoningServer(t, buffered, streamed)
			first := postChatReasoning(t, handler, "/v1/responses", `{"model":"mistral-large-4-nvfp4","input":"19 times 23?","stream":`+fmt.Sprint(stream)+`}`)
			id := ""
			if stream {
				for _, event := range sseEvents(t, first) {
					if event.name == "response.completed" {
						response, _ := event.data["response"].(map[string]interface{})
						id, _ = response["id"].(string)
					}
				}
			} else {
				var resp OpenAIResponsesResponse
				if err := json.Unmarshal([]byte(first), &resp); err != nil {
					t.Fatal(err)
				}
				id = resp.ID
			}
			if id == "" {
				t.Fatalf("no response id in:\n%s", first)
			}
			postChatReasoning(t, handler, "/v1/responses", `{"model":"mistral-large-4-nvfp4","previous_response_id":"`+id+`","input":"And 437 plus 1?"}`)
			message := assistant(requests())
			if got, _ := message["reasoning"].(string); got != reasoning {
				t.Fatalf("assistant reasoning = %q, want %q; message = %#v", got, reasoning, message)
			}
			if _, ok := message["reasoning_content"]; ok {
				t.Fatalf("assistant message = %#v, want no reasoning_content", message)
			}
		})
	}
}

// describeAnthropicStream lists the content events of an Anthropic stream.
func describeAnthropicStream(events []sseEvent) string {
	var parts []string
	for _, event := range events {
		number, _ := event.data["index"].(float64)
		index := int(number)
		switch event.name {
		case EventContentBlockStart:
			block, _ := event.data["content_block"].(map[string]interface{})
			parts = append(parts, fmt.Sprintf("start %s %d", block["type"], index))
		case EventContentBlockDelta:
			delta, _ := event.data["delta"].(map[string]interface{})
			switch delta["type"] {
			case "thinking_delta":
				parts = append(parts, fmt.Sprintf("thinking %s %d", delta["thinking"], index))
			case "signature_delta":
				parts = append(parts, fmt.Sprintf("signature %d", index))
			case "text_delta":
				parts = append(parts, fmt.Sprintf("text %s %d", delta["text"], index))
			case "input_json_delta":
				parts = append(parts, fmt.Sprintf("input %d", index))
			}
		case EventContentBlockStop:
			parts = append(parts, fmt.Sprintf("stop %d", index))
		case EventMessageDelta:
			delta, _ := event.data["delta"].(map[string]interface{})
			parts = append(parts, fmt.Sprintf("end %s", delta["stop_reason"]))
		}
	}
	return strings.Join(parts, ", ")
}

// The thinking block is the reply's first block, as Anthropic's is, and ends
// with its signature before the next block starts or the reply ends.
// Reasoning that arrives once the text began has no block to go in.
func TestAnthropicStreamPutsChatReasoningFirst(t *testing.T) {
	for _, tt := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{
			name: "reasoning after text",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"reasoning_content":"A"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"B"}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"C"}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"D"},"finish_reason":"stop"}]}`,
			},
			want: "start thinking 0, thinking A 0, signature 0, stop 0, start text 1, text B 1, text D 1, stop 1, end end_turn",
		},
		{
			name: "reasoning up to the token limit",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"reasoning":"A"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
			},
			want: "start thinking 0, thinking A 0, signature 0, stop 0, end max_tokens",
		},
		{
			name: "reasoning before a tool call",
			chunks: []string{
				`{"choices":[{"index":0,"delta":{"reasoning_content":"A"}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"inspect","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			},
			want: "start thinking 0, thinking A 0, signature 0, stop 0, start tool_use 1, input 1, stop 1, end tool_use",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newFlushableRecorder()
			handler, err := NewAnthropicStreamHandler(recorder, "gemma", context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var stream strings.Builder
			for _, chunk := range tt.chunks {
				stream.WriteString("data: " + chunk + "\n\n")
			}
			stream.WriteString("data: [DONE]\n\n")
			if err := NewOpenAIStreamParser(handler).Parse(strings.NewReader(stream.String())); err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got := describeAnthropicStream(sseEvents(t, recorder.Body.String())); got != tt.want {
				t.Fatalf("stream = %s\nwant     %s", got, tt.want)
			}
		})
	}
}

// The Chat reasoning apiproxy gave an Anthropic client goes back to an OpenAI
// backend, so converting it drops nothing; other thinking is still reported.
func TestChatReasoningThinkingIsNoDroppedField(t *testing.T) {
	convert := func(signature string) string {
		return captureWarnLogs(t, func() {
			req := &AnthropicRequest{
				Model:     "gpt-5",
				MaxTokens: 100,
				Messages: []AnthropicMessage{{
					Role:    core.RoleAssistant,
					Content: json.RawMessage(`[{"type":"thinking","thinking":"Multiply.","signature":"` + signature + `"},{"type":"text","text":"437"}]`),
				}},
			}
			if _, err := ConvertAnthropicToOpenAI(context.Background(), req); err != nil {
				t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
			}
		})
	}
	const dropped = `Dropping Anthropic field "content[].thinking"`
	if logs := convert(chatReasoningSignature + "chat_reasoning"); strings.Contains(logs, dropped) {
		t.Fatalf("Chat reasoning reported as dropped:\n%s", logs)
	}
	if logs := convert("Anthropic signature"); !strings.Contains(logs, dropped) {
		t.Fatalf("Anthropic thinking not reported as dropped:\n%s", logs)
	}
}
