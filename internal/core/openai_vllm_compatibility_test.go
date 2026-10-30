package core

import (
	"reflect"
	"strings"
	"testing"
)

// The captures in response-openai-vllm-reasoning come from ALCF's Minerva
// cluster: vLLM behind the ALCF inference gateway.

func TestParseOpenAIChatVLLMReasoningField(t *testing.T) {
	for _, mode := range []string{"buffered", "stream"} {
		t.Run(mode, func(t *testing.T) {
			var response Response
			var err error
			if mode == "buffered" {
				response, err = parseOpenAIResponseDetailed(readOpenAIChatFixture(t, "response-openai-vllm-reasoning", "openai.response.json"), false)
			} else {
				var events []ResponseStreamEvent
				response, events, err = parseOpenAIChatFixtureStream(t, readOpenAIChatFixture(t, "response-openai-vllm-reasoning", "openai-stream.stream.txt"))
				assertOpenAIChatReasoningEvents(t, response, events, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Text != "437" || len(response.Blocks) != 2 {
				t.Fatalf("response = %+v, want reasoning then answer", response)
			}
			reasoning, ok := response.Blocks[0].(ReasoningBlock)
			if !ok || reasoning.Provider != "openai" || reasoning.Type != "chat_reasoning" || !strings.Contains(reasoning.Text, "437") {
				t.Fatalf("reasoning = %#v", response.Blocks[0])
			}
			messages := []TypedMessage{{Role: "assistant", Blocks: response.Blocks}}
			wire := ToOpenAITyped(messages)
			rendered := MarshalOpenAIMessagesForRequest(wire)[0].(map[string]interface{})
			if rendered["content"] != "437" || rendered["reasoning"] != reasoning.Text || rendered["reasoning_content"] != nil {
				t.Fatalf("replayed message = %#v, want the reasoning field back", rendered)
			}
			if replayed := FromOpenAITyped(wire); !reflect.DeepEqual(replayed, messages) {
				t.Fatalf("typed round trip = %#v, want %#v", replayed, messages)
			}
			if input := OpenAIResponsesInput(messages); len(input) != 1 {
				t.Fatalf("Responses input = %#v, want the answer alone", input)
			}
		})
	}
}

func TestOpenAIChatReasoningContentWinsOverTheReasoningField(t *testing.T) {
	buffered, err := parseOpenAIResponseDetailed([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"4","reasoning_content":"Two and two.","reasoning":"Two and two."}}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	streamed, _, err := parseOpenAIChatFixtureStream(t, []byte(
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"Two and two.","reasoning":"Two and two."}}]}`+"\n\n"+
			`data: {"choices":[{"index":0,"delta":{"content":"4"},"finish_reason":"stop"}]}`+"\n\n"+
			"data: [DONE]\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Block{openAIChatReasoningBlock("Two and two."), TextBlock{Text: "4"}}
	for _, response := range []Response{buffered, streamed} {
		if !reflect.DeepEqual(response.Blocks, want) {
			t.Fatalf("blocks = %#v, want %#v", response.Blocks, want)
		}
	}
}
