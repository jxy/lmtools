package core

import (
	"context"
	"encoding/json"
	"io"
	"lmtools/internal/apifixtures"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAIResponsesLlamaCPPFixtures(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"response-llamacpp-responses-reasoning-tool", "response-llamacpp-responses-reasoning-answer"} {
		t.Run(id, func(t *testing.T) {
			stream, err := apifixtures.ReadCaseFile(suite.Root, id, "captures/openai-responses-stream.stream.txt")
			if err != nil {
				t.Fatal(err)
			}
			wantRaw, err := apifixtures.ReadCaseFile(suite.Root, id, "expected/stream_projection.json")
			if err != nil {
				t.Fatal(err)
			}
			var want struct {
				Text       string   `json:"text"`
				Reasoning  string   `json:"reasoning"`
				BlockTypes []string `json:"block_types"`
				ToolCalls  []struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"tool_calls"`
			}
			if err := json.Unmarshal(wantRaw, &want); err != nil {
				t.Fatal(err)
			}
			state := NewOpenAIResponsesStreamState()
			var text, reasoning strings.Builder
			var calls []ToolCall
			var starts, ends int
			for _, line := range strings.Split(string(stream), "\n") {
				chunk, completed, done, err := state.ParseLine(line)
				if err != nil {
					t.Fatalf("ParseLine(%s): %v", line, err)
				}
				if !done && len(completed) > 0 {
					t.Fatalf("tool calls emitted before terminal response: %#v", completed)
				}
				text.WriteString(chunk)
				calls = append(calls, completed...)
				for _, event := range state.drainResponseStreamEvents() {
					switch event.Type {
					case ResponseStreamReasoningStart:
						starts++
					case ResponseStreamReasoningDelta:
						reasoning.WriteString(event.Text)
					case ResponseStreamReasoningEnd:
						ends++
						if event.Incomplete {
							t.Fatal("completed reasoning marked incomplete")
						}
					}
				}
			}
			if text.String() != want.Text || reasoning.String() != want.Reasoning {
				t.Fatalf("display projection mismatch: answer %q, reasoning %q", text.String(), reasoning.String())
			}
			if starts != 1 || ends != 1 {
				t.Fatalf("reasoning starts=%d ends=%d, want one block", starts, ends)
			}
			if len(calls) != len(want.ToolCalls) {
				t.Fatalf("calls = %#v, want %d", calls, len(want.ToolCalls))
			}
			for i, call := range calls {
				gotArgs, _ := apifixtures.CanonicalJSON(call.Args)
				wantArgs, _ := apifixtures.CanonicalJSON(want.ToolCalls[i].Args)
				if call.ID != want.ToolCalls[i].ID || call.Name != want.ToolCalls[i].Name || string(gotArgs) != string(wantArgs) {
					t.Fatalf("call = %#v, want %#v", call, want.ToolCalls[i])
				}
			}
			blocks := state.Blocks()
			if len(blocks) != len(want.BlockTypes) {
				t.Fatalf("blocks = %#v", blocks)
			}
			for i, kind := range want.BlockTypes {
				switch value := blocks[i].(type) {
				case ReasoningBlock:
					if kind != "reasoning" || ReasoningTextForDisplay(value) != want.Reasoning {
						t.Fatalf("reasoning block = %#v", value)
					}
				case TextBlock:
					if kind != "message" || value.Text != want.Text {
						t.Fatalf("text block = %#v", value)
					}
				case ToolUseBlock:
					if kind != "function_call" || value.ID != calls[0].ID || string(value.Input) != string(calls[0].Args) {
						t.Fatalf("tool block = %#v", value)
					}
				default:
					t.Fatalf("unexpected block %T", value)
				}
			}
			if state.IncompleteReason() != "" || state.Usage() == nil {
				t.Fatalf("completion reason=%q usage=%#v", state.IncompleteReason(), state.Usage())
			}
			buffered, err := apifixtures.ReadCaseFile(suite.Root, id, "captures/openai-responses.response.json")
			if err != nil {
				t.Fatal(err)
			}
			response, err := parseOpenAIResponses(buffered, false)
			if err != nil || response.Text != want.Text || len(response.ToolCalls) != len(calls) || !reflect.DeepEqual(response.Blocks, blocks) {
				t.Fatalf("buffered/stream mismatch: response=%#v blocks=%#v error=%v", response, blocks, err)
			}
		})
	}
}

func parseResponsesTestEvents(t *testing.T, state *OpenAIResponsesStreamState, events ...string) (string, []ToolCall, []ResponseStreamEvent) {
	t.Helper()
	var text strings.Builder
	var calls []ToolCall
	var display []ResponseStreamEvent
	for _, event := range events {
		chunk, emitted, _, err := state.ParseLine("data: " + event)
		if err != nil {
			t.Fatalf("ParseLine(%s): %v", event, err)
		}
		text.WriteString(chunk)
		calls = append(calls, emitted...)
		display = append(display, state.drainResponseStreamEvents()...)
	}
	return text.String(), calls, display
}

func TestOpenAIResponsesStreamInterleavedCallsKeepIdentityAndOrder(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(map[bool]string{false: "item ID", true: "index zero"}[indexed], func(t *testing.T) {
			state := NewOpenAIResponsesStreamState()
			events := []string{
				`{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning"}}`,
				`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"","status":"in_progress"}}`,
				`{"type":"response.output_item.added","item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_2","name":"patch","input":"","status":"in_progress"}}`,
				`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"q\":"}`,
				`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"raw "}`,
				`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"x\"}"}`,
				`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"input"}`,
				`{"type":"response.output_item.done","item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_2","name":"patch"}}`,
				`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
				`{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup"}}`,
			}
			if indexed {
				// The reasoning item has no index; the real zero belongs to a
				// distinct function call, and its deltas still use item_id.
				events[1] = strings.Replace(events[1], `"item":`, `"output_index":0,"item":`, 1)
				events[2] = strings.Replace(events[2], `"item":`, `"output_index":1,"item":`, 1)
			}
			_, calls, _ := parseResponsesTestEvents(t, state, events...)
			if len(calls) != 0 {
				t.Fatalf("calls emitted before terminal: %#v", calls)
			}
			_, calls, _ = parseResponsesTestEvents(t, state, `{"type":"response.completed"}`)
			if len(calls) != 2 || calls[0].ID != "call_1" || calls[1].ID != "call_2" || string(calls[0].Args) != `{"q":"x"}` || calls[1].Input != "raw input" {
				t.Fatalf("calls = %#v", calls)
			}
			_, repeated, _ := parseResponsesTestEvents(t, state, `{"type":"response.completed"}`, `[DONE]`)
			if len(repeated) != 0 {
				t.Fatalf("repeated terminal calls = %#v", repeated)
			}
		})
	}
}

func TestOpenAIResponsesStreamTerminalSnapshotCompletesAndReorders(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	text, calls, display := parseResponsesTestEvents(t, state,
		`{"type":"response.output_item.added","item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"second"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_2","delta":"{\"partial\":"}`,
		`{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","encrypted_content":"SECRET"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Visible"}],"encrypted_content":"SECRET"},{"id":"fc_1","type":"function_call","call_id":"call_1","name":"first","arguments":"{}"},{"id":"fc_2","type":"function_call","call_id":"call_2","name":"second","arguments":"{\"full\":true}"},{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Done"}]}]}}`)
	if text != "Done" || len(calls) != 2 || calls[0].Name != "first" || calls[1].Name != "second" || string(calls[1].Args) != `{"full":true}` {
		t.Fatalf("snapshot text=%q calls=%#v", text, calls)
	}
	if len(display) != 3 || display[0].Type != ResponseStreamReasoningStart || display[1].Text != "Visible" || display[2].Type != ResponseStreamReasoningEnd {
		t.Fatalf("display = %#v", display)
	}
	blocks := state.Blocks()
	if len(blocks) != 4 {
		t.Fatalf("blocks=%#v", blocks)
	}
	if reasoning, ok := blocks[0].(ReasoningBlock); !ok || reasoning.EncryptedContent != "SECRET" {
		t.Fatalf("reasoning = %#v", blocks[0])
	}
}

func TestOpenAIResponsesStreamSummaryPartsDoNotRepeat(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	_, _, display := parseResponsesTestEvents(t, state,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"SECRET"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"First "}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_1","summary_index":0,"text":"First part"}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs_1","summary_index":0,"part":{"type":"summary_text","text":"First part"}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":1,"delta":"Second part"}`,
		`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"First part"},{"type":"summary_text","text":"Second part"}],"encrypted_content":"SECRET"}}`,
		`{"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"First part"},{"type":"summary_text","text":"Second part"}],"encrypted_content":"SECRET"}]}}`)
	var visible strings.Builder
	var starts, ends int
	for _, event := range display {
		switch event.Type {
		case ResponseStreamReasoningStart:
			starts++
		case ResponseStreamReasoningDelta:
			visible.WriteString(event.Text)
		case ResponseStreamReasoningEnd:
			ends++
		}
	}
	if visible.String() != "First part\nSecond part" || starts != 1 || ends != 1 {
		t.Fatalf("reasoning %q starts=%d ends=%d", visible.String(), starts, ends)
	}
}

func TestOpenAIResponsesStreamIncompleteAndInterruptedCalls(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`,
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`,
		`{"type":"response.incomplete"}`,
		`[DONE]`,
		``,
	} {
		t.Run(terminal, func(t *testing.T) {
			state := NewOpenAIResponsesStreamState()
			_, calls, _ := parseResponsesTestEvents(t, state,
				`{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning"}}`,
				`{"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"Partial thinking"}`,
				`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup"}}`,
				`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{}"}`)
			if terminal != "" {
				_, calls, _ = parseResponsesTestEvents(t, state, terminal)
			}
			if len(calls) != 0 || state.IncompleteReason() == "" {
				t.Fatalf("calls=%#v incomplete reason=%q", calls, state.IncompleteReason())
			}
			blocks := state.Blocks()
			if reasoning, ok := blocks[0].(ReasoningBlock); !ok || ReasoningTextForDisplay(reasoning) != "Partial thinking" {
				t.Fatalf("reasoning block=%#v", blocks[0])
			}
		})
	}
}

func TestParseOpenAIResponsesIncompleteMetadata(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter", "provider_reason"} {
		data := []byte(`{"status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"message","content":[{"type":"output_text","text":"Partial"}]}]}`)
		response, err := parseOpenAIResponses(data, false)
		if err != nil || response.IncompleteReason != reason || response.Text != "Partial" || len(response.ToolCalls) != 0 {
			t.Fatalf("incomplete response = %#v error=%v", response, err)
		}
	}
}

func TestBuildOpenAIResponsesMaxOutputTokens(t *testing.T) {
	messages := []TypedMessage{
		NewTextMessage("user", "Look up x"),
		{Role: "assistant", Blocks: []Block{ToolUseBlock{ID: "call_1", Name: "lookup", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Blocks: []Block{ToolResultBlock{ToolUseID: "call_1", Content: "x"}}},
	}
	for _, stream := range []bool{false, true} {
		for _, followup := range []bool{false, true} {
			for _, limit := range []int{0, 8} {
				cfg := RequestOptions{Provider: "openai", ProviderURL: "https://example.test/v1", Model: "gemma-4-26b-a4b", OpenAIResponses: true, StreamChat: stream, MaxTokens: limit, ToolEnabled: true}
				var body []byte
				var err error
				if followup {
					_, body, err = BuildToolResultRequest(cfg, cfg.Model, "", nil, messages)
				} else {
					_, body, err = BuildRequest(cfg, "Look up x")
				}
				if err != nil {
					t.Fatal(err)
				}
				var request map[string]interface{}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatal(err)
				}
				value, exists := request["max_output_tokens"]
				if (limit > 0 && value != float64(limit)) || (limit == 0 && exists) || request["stream"] != stream {
					t.Fatalf("stream=%v followup=%v limit=%d request=%s", stream, followup, limit, body)
				}
			}
		}
	}
}

func TestOpenAIResponsesInputSkipsChatReasoningContent(t *testing.T) {
	items := OpenAIResponsesInput([]TypedMessage{{Role: "assistant", Blocks: []Block{
		ReasoningBlock{Provider: "openai", Type: "reasoning_content", Text: "Chat-only thinking"}, TextBlock{Text: "Answer"},
	}}})
	if len(items) != 1 || items[0].(map[string]interface{})["type"] != "message" {
		t.Fatalf("Responses input = %#v", items)
	}
}

func TestOpenAIResponsesStreamInterleavedReasoningHasOneSection(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	_, _, events := parseResponsesTestEvents(t, state,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"One "}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_2","delta":"Two "}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"done"}`,
		`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"One done"}],"encrypted_content":"SECRET"}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_2","delta":"done"}`,
		`{"type":"response.output_item.done","item":{"id":"rs_2","type":"reasoning","content":[{"type":"reasoning_text","text":"Two done"}],"encrypted_content":"SECRET"}}`,
		`{"type":"response.completed"}`)
	var visible strings.Builder
	var starts, ends int
	for _, event := range events {
		switch event.Type {
		case ResponseStreamReasoningStart:
			starts++
		case ResponseStreamReasoningDelta:
			visible.WriteString(event.Text)
		case ResponseStreamReasoningEnd:
			ends++
		}
	}
	if starts != 1 || ends != 1 || visible.String() != "One Two donedone" {
		t.Fatalf("visible=%q starts=%d ends=%d", visible.String(), starts, ends)
	}
	blocks := state.Blocks()
	if len(blocks) != 2 || ReasoningTextForDisplay(blocks[0].(ReasoningBlock)) != "One done" || ReasoningTextForDisplay(blocks[1].(ReasoningBlock)) != "Two done" {
		t.Fatalf("interleaved blocks=%#v", blocks)
	}
}

func TestOpenAIResponsesStreamSnapshotRemovesOmittedCall(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	_, calls, _ := parseResponsesTestEvents(t, state,
		`{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"output":[]}}`)
	if len(calls) != 0 || len(state.Blocks()) != 0 {
		t.Fatalf("calls=%#v blocks=%#v, want explicit empty snapshot", calls, state.Blocks())
	}
}

func TestOpenAIResponsesStreamConflictingSnapshotIsFatal(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	text, _, _ := parseResponsesTestEvents(t, state, `{"type":"response.output_text.delta","delta":"Observed"}`)
	if text != "Observed" {
		t.Fatalf("text=%q", text)
	}
	_, calls, _, err := state.ParseLine(`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"Different"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}]}}`)
	if !isFatalStreamError(err) || len(calls) != 0 {
		t.Fatalf("calls=%#v error=%v, want fatal conflict with no calls", calls, err)
	}
}

func TestOpenAIResponsesStreamRejectsChangingExplicitIndex(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	parseResponsesTestEvents(t, state, `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call"}}`)
	_, calls, _, err := state.ParseLine(`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","name":"lookup","arguments":"{}"}}`)
	if !isFatalStreamError(err) || len(calls) != 0 {
		t.Fatalf("calls=%#v error=%v, want fatal identity conflict", calls, err)
	}
}

func TestOpenAIResponsesStreamReasoningEndsBeforeAnswer(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	parseResponsesTestEvents(t, state,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","delta":"Thinking"}`,
		`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Thinking"}]}}`)
	answer, calls, done, err := state.ParseLine(`data: {"type":"response.output_text.delta","delta":"Answer"}`)
	if err != nil || answer != "Answer" || len(calls) != 0 || done {
		t.Fatalf("answer=%q calls=%#v done=%v error=%v", answer, calls, done, err)
	}
	// The streaming driver drains these events before emitting the returned
	// answer. A normal transition must close the presenter's section first.
	events := state.drainResponseStreamEvents()
	if len(events) != 1 || events[0].Type != ResponseStreamReasoningEnd || events[0].Incomplete {
		t.Fatalf("events before answer=%#v, want completed reasoning end", events)
	}
}

func TestParseOpenAIResponsesIncompleteItemMarksResponse(t *testing.T) {
	response, err := parseOpenAIResponses([]byte(`{"status":"completed","output":[{"type":"function_call","status":"incomplete","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}]}`), false)
	if err != nil || response.IncompleteReason == "" || len(response.ToolCalls) != 0 {
		t.Fatalf("response=%#v error=%v, want incomplete item blocked", response, err)
	}
}

func TestParseOpenAIResponsesMalformedKnownFieldsFail(t *testing.T) {
	_, err := parseOpenAIResponses([]byte(`{"output":[{"type":"message","content":{"text":"wrong shape"}}]}`), false)
	if err == nil {
		t.Fatal("malformed message content accepted")
	}
}

func responsesStreamResponse(t *testing.T, events ...string) (Response, *recordingResponseOutput) {
	t.Helper()
	var stream strings.Builder
	for _, event := range events {
		stream.WriteString("data: " + event + "\n\n")
	}
	output := &recordingResponseOutput{}
	response, err := HandleResponseWithOptions(context.Background(), RequestOptions{
		Provider: "openai", Model: "gemma-test", OpenAIResponses: true, StreamChat: true,
	}, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(stream.String()))},
		&MockLogger{}, &MockNotifier{}, ResponseParseOptions{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	return response, output
}

func TestOpenAIResponsesEOFPreservesDisplayedAnswerInBlocksAndReplay(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		events []string
	}{
		{"item ID", []string{
			`{"type":"response.output_item.added","item":{"id":"msg_1","type":"message","content":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","delta":"Partial answer"}`,
		}},
		{"explicit zero", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"Partial answer"}`,
		}},
		{"unidentified delta with message", []string{
			`{"type":"response.output_item.added","item":{"id":"msg_1","type":"message","content":[]}}`,
			`{"type":"response.output_text.delta","delta":"Partial answer"}`,
		}},
		{"unidentified delta without message", []string{
			`{"type":"response.output_text.delta","delta":"Partial answer"}`,
		}},
		{"initial prefix", []string{
			`{"type":"response.output_item.added","item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Partial"}]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","delta":" answer"}`,
		}},
		{"refusal", []string{
			`{"type":"response.refusal.delta","item_id":"msg_1","delta":"Partial answer"}`,
		}},
		{"completed snapshot without deltas", []string{
			`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Partial answer"}]}}`,
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			events := append([]string{`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Thinking"}]}}`}, scenario.events...)
			response, output := responsesStreamResponse(t, events...)
			var displayed, canonical, replayed strings.Builder
			for _, event := range output.events {
				if event.Type == ResponseStreamTextDelta {
					displayed.WriteString(event.Text)
				}
			}
			for _, block := range response.Blocks {
				if text, ok := block.(TextBlock); ok {
					canonical.WriteString(text.Text)
				}
			}
			for _, rawItem := range OpenAIResponsesInput([]TypedMessage{{Role: "assistant", Blocks: response.Blocks}}) {
				item := rawItem.(map[string]interface{})
				if item["type"] == "message" {
					for _, part := range item["content"].([]map[string]interface{}) {
						replayed.WriteString(part["text"].(string))
					}
				}
			}
			if response.Text != "Partial answer" || response.IncompleteReason != "stream_interrupted" ||
				displayed.String() != response.Text || canonical.String() != response.Text || replayed.String() != response.Text {
				t.Fatalf("response=%#v displayed=%q canonical=%q replayed=%q", response, displayed.String(), canonical.String(), replayed.String())
			}
		})
	}
}

func TestOpenAIResponsesEOFPreservesReasoningPrefixAndSuffix(t *testing.T) {
	for _, field := range []string{"content", "summary"} {
		t.Run(field, func(t *testing.T) {
			partType, eventType := "reasoning_text", "response.reasoning_text.delta"
			if field == "summary" {
				partType, eventType = "summary_text", "response.reasoning_summary_text.delta"
			}
			response, output := responsesStreamResponse(t,
				`{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","`+field+`":[{"type":"`+partType+`","text":"Initial"}]}}`,
				`{"type":"`+eventType+`","item_id":"rs_1","delta":" suffix"}`)
			var displayed strings.Builder
			for _, event := range output.events {
				if event.Type == ResponseStreamReasoningDelta {
					displayed.WriteString(event.Text)
				}
			}
			if len(response.Blocks) != 1 {
				t.Fatalf("blocks=%#v", response.Blocks)
			}
			reasoning := response.Blocks[0].(ReasoningBlock)
			input, err := json.Marshal(OpenAIResponsesInput([]TypedMessage{{Role: "assistant", Blocks: response.Blocks}}))
			if err != nil || displayed.String() != "Initial suffix" || ReasoningTextForDisplay(reasoning) != displayed.String() || !strings.Contains(string(input), "Initial suffix") {
				t.Fatalf("displayed=%q block=%#v replay=%s error=%v", displayed.String(), reasoning, input, err)
			}
		})
	}
}

func TestOpenAIResponsesDoneOnlyAnswerEmitsOnceAtCompletion(t *testing.T) {
	for _, terminal := range []string{`[DONE]`, `{"type":"response.completed"}`, `{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Snapshot answer"}]}]}}`} {
		response, output := responsesStreamResponse(t,
			`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Snapshot answer"}]}}`, terminal)
		var displayed strings.Builder
		for _, event := range output.events {
			if event.Type == ResponseStreamTextDelta {
				displayed.WriteString(event.Text)
			}
		}
		if response.Text != "Snapshot answer" || displayed.String() != response.Text || len(response.Blocks) != 1 {
			t.Fatalf("terminal=%s response=%#v displayed=%q", terminal, response, displayed.String())
		}
	}
}

func TestOpenAIResponsesFinalSnapshotReplacesUnidentifiedTextBuffer(t *testing.T) {
	response, output := responsesStreamResponse(t,
		`{"type":"response.output_text.delta","delta":"Answer"}`,
		`{"type":"response.completed","response":{"output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"Answer"}]}]}}`)
	var displayed strings.Builder
	for _, event := range output.events {
		if event.Type == ResponseStreamTextDelta {
			displayed.WriteString(event.Text)
		}
	}
	if response.Text != "Answer" || displayed.String() != response.Text || len(response.Blocks) != 1 {
		t.Fatalf("response=%#v displayed=%q", response, displayed.String())
	}
}

func TestOpenAIResponsesTextDeltaCannotReplaceToolItem(t *testing.T) {
	state := NewOpenAIResponsesStreamState()
	parseResponsesTestEvents(t, state, `{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","name":"lookup","call_id":"call_1"}}`)
	_, calls, _, err := state.ParseLine(`data: {"type":"response.output_text.delta","item_id":"fc_1","delta":"wrong item"}`)
	if !isFatalStreamError(err) || len(calls) != 0 {
		t.Fatalf("calls=%#v error=%v, want an item type conflict", calls, err)
	}
}

func TestOpenAIResponsesStreamIncompleteNonToolItemsBlockCalls(t *testing.T) {
	for _, kind := range []string{"message", "reasoning"} {
		for _, status := range []string{"incomplete", "in_progress"} {
			for _, terminal := range []string{"omitted output", "snapshot", "marker"} {
				t.Run(kind+"/"+status+"/"+terminal, func(t *testing.T) {
					contentType := "output_text"
					if kind == "reasoning" {
						contentType = "reasoning_text"
					}
					item := `{"id":"item_1","type":"` + kind + `","status":"` + status + `","content":[{"type":"` + contentType + `","text":"Partial"}]}`
					tool := `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}`
					events := []string{
						`{"type":"response.output_item.done","item":` + item + `}`,
						`{"type":"response.output_item.done","item":` + tool + `}`,
					}
					switch terminal {
					case "omitted output":
						events = append(events, `{"type":"response.completed","response":{"status":"completed"}}`)
					case "snapshot":
						events = append(events, `{"type":"response.completed","response":{"status":"completed","output":[`+item+`,`+tool+`]}}`)
					case "marker":
						events = append(events, `[DONE]`)
					}
					response, _ := responsesStreamResponse(t, events...)
					if response.IncompleteReason != "incomplete" || len(response.ToolCalls) != 0 {
						t.Fatalf("reason=%q calls=%#v, want incomplete item to block calls", response.IncompleteReason, response.ToolCalls)
					}
					if response.Text == "" && len(response.Blocks) == 0 {
						t.Fatal("partial content was discarded")
					}
					for _, block := range response.Blocks {
						if _, ok := block.(ToolUseBlock); ok {
							t.Fatal("incomplete response retained a pending tool block")
						}
					}
				})
			}
		}
	}
}

func TestOpenAIResponsesStreamRejectedTerminalCannotReleaseCalls(t *testing.T) {
	tool := `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}`
	for _, tt := range []struct{ name, event string }{
		// The first output item is valid and would update the state before the
		// second item's known field fails to decode.
		{"malformed later item", `{"type":"response.completed","response":{"status":"completed","output":[` + tool + `,{"id":"fc_2","type":"function_call","arguments":{}}]}}`},
		{"malformed completion metadata", `{"type":"response.completed","response":{"status":123}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := NewOpenAIResponsesStreamState()
			observed := `{"type":"response.output_item.done","item":` + tool + `}`
			parseResponsesTestEvents(t, state, observed)
			_, calls, _, err := state.ParseLine("data: " + tt.event)
			if !isFatalStreamError(err) || len(calls) != 0 {
				t.Fatalf("calls=%#v error=%v, want rejected terminal to fail", calls, err)
			}
			for _, marker := range []string{`[DONE]`, `{"type":"response.completed"}`} {
				_, calls, _, _ = state.ParseLine("data: " + marker)
				if len(calls) != 0 || state.IncompleteReason() == "" {
					t.Fatalf("marker=%s calls=%#v reason=%q, want rejection to remain terminal", marker, calls, state.IncompleteReason())
				}
			}

			// Exercise RunStream too: ordinary parsing errors are recoverable
			// there, so the Responses state must identify this as fatal.
			stream := "data: " + observed + "\n\ndata: " + tt.event + "\n\ndata: [DONE]\n\n"
			response, err := HandleResponseWithOptions(context.Background(), RequestOptions{
				Provider: "openai", Model: "gemma-test", OpenAIResponses: true, StreamChat: true,
			}, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(stream))},
				&MockLogger{logDir: t.TempDir()}, &MockNotifier{}, ResponseParseOptions{Output: &recordingResponseOutput{}})
			if err == nil || len(response.ToolCalls) != 0 {
				t.Fatalf("response=%#v error=%v, want malformed terminal to stop the stream", response, err)
			}
		})
	}
}
