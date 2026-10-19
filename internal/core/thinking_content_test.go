package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestThinkingContentAndReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var response Response
			var err error
			if stream {
				var events []ResponseStreamEvent
				response, events, err = parseOpenAIChatFixtureStream(t, readOpenAIChatFixture(t, "response-openai-thinking-content", "openai-stream.stream.txt"))
				assertOpenAIChatReasoningEvents(t, response, events, false)
			} else {
				response, err = parseOpenAIResponseDetailed(readOpenAIChatFixture(t, "response-openai-thinking-content", "openai.response.json"), false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Text != "AUDIT_OK" || response.IncompleteReason != "" || len(response.Blocks) != 2 {
				t.Fatalf("response=%+v", response)
			}
			reasoning, ok := response.Blocks[0].(ReasoningBlock)
			if !ok || reasoning.Type != openAIThinkingChunkType || reasoning.Text == "" {
				t.Fatalf("reasoning=%#v", response.Blocks[0])
			}
			messages := []TypedMessage{{Role: "assistant", Blocks: response.Blocks}}
			wire := MarshalOpenAIMessagesForRequest(ToOpenAITyped(messages))
			encoded, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "reasoning_content") {
				t.Fatalf("wrong dialect: %s", encoded)
			}
			var decoded []OpenAIMessage
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			replay := FromOpenAITyped(decoded)
			if len(replay) != 1 || len(replay[0].Blocks) != 2 || replay[0].Blocks[1].(TextBlock).Text != "AUDIT_OK" || replay[0].Blocks[0].(ReasoningBlock).Text != reasoning.Text {
				t.Fatalf("replayed=%#v", replay)
			}
		})
	}
}

func TestOpenAIChatContentUnion(t *testing.T) {
	for _, tc := range []struct {
		raw, text, reason string
		bad               bool
	}{
		{`null`, "", "", false},
		{`""`, "", "", false},
		{`[]`, "", "", false},
		{`"hello"`, "hello", "", false},
		{`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "ab", "", false},
		{`[{"type":"thinking","thinking":[{"type":"text","text":"one"},{"type":"text","text":"two"}],"closed":true,"signature":"opaque"}]`, "", "onetwo", false},
		{`[{"type":"unknown","text":"lost"}]`, "", "", true},
		{`[{"type":"thinking","thinking":[{"type":"unknown"}]}]`, "", "", true},
		{`[{"type":"text","text":123}]`, "", "", true},
		{`{}`, "", "", true},
		{`12`, "", "", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			blocks, err := parseOpenAIChatContent([]byte(tc.raw))
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v", err)
			}
			if tc.bad {
				return
			}
			if got := openAIChatContentText(blocks); got != tc.text {
				t.Fatalf("text=%q", got)
			}
			if tc.reason != "" {
				b := blocks[0].(ReasoningBlock)
				if b.Text != tc.reason || ReasoningTextForDisplay(b) != tc.reason {
					t.Fatalf("reasoning=%+v", b)
				}
				rendered := openAIThinkingContent(b).ToMap()
				if rendered["signature"] != "opaque" {
					t.Fatalf("metadata lost: %#v", rendered)
				}
			}
		})
	}
}

func TestThinkingContentMixedChunkEventOrder(t *testing.T) {
	data := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"a"},{"type":"thinking","thinking":[{"type":"text","text":"b"}],"closed":true},{"type":"text","text":"c"}]},"finish_reason":"stop"}]}

data: [DONE]

`
	response, events, err := parseOpenAIChatFixtureStream(t, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "ac" {
		t.Fatal(response.Text)
	}
	var got []ResponseStreamEventType
	for _, event := range events {
		got = append(got, event.Type)
	}
	want := []ResponseStreamEventType{ResponseStreamTextDelta, ResponseStreamReasoningStart, ResponseStreamReasoningDelta, ResponseStreamReasoningEnd, ResponseStreamTextDelta}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%v", got)
	}
}

func TestOpenAIInvalidStreamCannotAuthorizeTools(t *testing.T) {
	for _, bad := range []string{`{bad json}`, `{"choices":[{"delta":{"content":42}}]}`, `{"choices":[{"delta":{"content":[{"type":"unsupported"}]}}]}`} {
		for _, ending := range []string{"\n\n", ""} {
			t.Run(bad+fmt.Sprint(len(ending)), func(t *testing.T) {
				prefix := `data: {"choices":[{"index":0,"delta":{"content":"partial","tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":"universal_command","arguments":"{\"command\":[\"/usr/bin/true\"]}"}}]}}]}` + "\n\n"
				data := prefix + "data:" + bad + ending
				if ending != "" {
					data += "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
				}
				response, _, err := parseOpenAIChatFixtureStream(t, []byte(data))
				if err == nil || response.IncompleteReason != "invalid_stream" || response.Text != "partial" || len(response.ToolCalls) != 0 {
					t.Fatalf("response=%+v err=%v", response, err)
				}
				for _, b := range response.Blocks {
					if _, ok := b.(ToolUseBlock); ok {
						t.Fatalf("unsafe stored call: %+v", b)
					}
				}
			})
		}
	}
}

func TestOpenAIInvalidToolArgumentsStayIncomplete(t *testing.T) {
	data := `data: {"choices":[{"delta":{"content":"partial","tool_calls":[{"index":0,"id":"call1","function":{"name":"run","arguments":"not json"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	response, _, err := parseOpenAIChatFixtureStream(t, []byte(data))
	if err == nil || response.IncompleteReason != "invalid_stream" || len(response.ToolCalls) > 0 || response.Text != "partial" {
		t.Fatalf("response=%+v error=%v", response, err)
	}
}

func TestThinkingContentMetadataSurvivesStreamReplay(t *testing.T) {
	data := `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"one","annotation":"kept"}],"closed":true,"signature":"opaque","counter":9007199254740993}]}}]}

data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"two"}],"closed":true},{"type":"text","text":"answer"}]},"finish_reason":"stop"}]}

data: [DONE]

`
	response, events, err := parseOpenAIChatFixtureStream(t, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	assertOpenAIChatReasoningEvents(t, response, events, false)
	for _, event := range events {
		if strings.Contains(event.Text, "opaque") {
			t.Fatal("signature exposed")
		}
	}
	wire := MarshalOpenAIMessagesForRequest(ToOpenAITyped([]TypedMessage{{Role: "assistant", Blocks: response.Blocks}}))
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"annotation":"kept"`, `"counter":9007199254740993`, `"signature":"opaque"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("lost %s in %s", want, raw)
		}
	}
	var invalid OpenAIContent
	if err := json.Unmarshal([]byte(`{"type":"thinking","thinking":[{"type":"unknown"}]}`), &invalid); err == nil {
		t.Fatal("invalid thinking silently accepted into history")
	}
}

func TestOpenAIStreamIgnoresEmptyHeartbeatEvents(t *testing.T) {
	data := ": keepalive\n\ndata:\n\ndata: \n\nevent: ping\ndata: {\"type\":\"ping\"}\n\ndata:{\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata:[DONE]\n\n"
	response, _, err := parseOpenAIChatFixtureStream(t, []byte(data))
	if err != nil || response.Text != "OK" || response.IncompleteReason != "" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestThinkingContentAfterTextStaysOpenAcrossChunks(t *testing.T) {
	data := `data: {"choices":[{"delta":{"content":[{"type":"text","text":"prefix"},{"type":"thinking","thinking":[{"type":"text","text":"one"}],"closed":true}]}}]}

data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"two"}],"closed":true},{"type":"text","text":"suffix"}]},"finish_reason":"stop"}]}

data: [DONE]

`
	response, events, err := parseOpenAIChatFixtureStream(t, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "prefixsuffix" {
		t.Fatal(response.Text)
	}
	var starts, ends int
	for _, event := range events {
		if event.Type == ResponseStreamReasoningStart {
			starts++
		}
		if event.Type == ResponseStreamReasoningEnd {
			ends++
		}
		if (event.Type == ResponseStreamReasoningStart || event.Type == ResponseStreamReasoningEnd) && event.ReasoningType != "thinking" {
			t.Fatalf("wrong reasoning dialect: %+v", event)
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("starts=%d ends=%d", starts, ends)
	}
}
